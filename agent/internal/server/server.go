// Package server is the agent's HTTPS API. Every route is GET, every route
// needs a valid signature, and none of them changes anything on the server.
package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"hdash-agent/internal/auth"
	"hdash-agent/internal/collect"
	"hdash-agent/internal/config"
	"hdash-agent/internal/history"
)

const (
	failDelay   = 500 * time.Millisecond
	maxFails    = 5
	failWindow  = time.Minute
	blockFor    = 5 * time.Minute
	maxInflight = 16
)

type Server struct {
	verifier  *auth.Verifier
	allow     []*net.IPNet
	collector *collect.Collector
	history   *history.History

	mu    sync.Mutex
	fails map[string]*failure
	slots chan struct{}
}

type failure struct {
	count        int
	first, until time.Time
}

func New(cfg *config.Config, c *collect.Collector, h *history.History) *Server {
	s := &Server{
		verifier: auth.NewVerifier(cfg.Key()), collector: c, history: h,
		fails: map[string]*failure{}, slots: make(chan struct{}, maxInflight),
	}
	for _, a := range cfg.AllowFrom {
		n, _ := config.ParseAllow(a)
		s.allow = append(s.allow, n)
	}
	return s
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ip := remoteIP(r)
	if !s.allowed(ip) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	if s.blocked(ip) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many failed attempts"})
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "busy"})
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "only GET"})
		return
	}
	if err := s.verifier.Check(r, time.Now()); err != nil {
		s.fail(ip)
		time.Sleep(failDelay)
		// Same answer for every reason, so a probe learns nothing.
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	switch r.URL.Path {
	case "/v1/snapshot":
		s.snapshot(w, r)
	case "/v1/history":
		since, err := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
		if err != nil {
			since = 0
		}
		writeJSON(w, http.StatusOK, s.history.Query(since, time.Now().Unix()))
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	select {
	case <-s.collector.Ready():
	case <-time.After(20 * time.Second):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "first sample not ready"})
		return
	case <-r.Context().Done():
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(s.collector.Latest())
}

func remoteIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return nil
	}
	return net.ParseIP(host)
}

func (s *Server) allowed(ip net.IP) bool {
	if len(s.allow) == 0 {
		return true
	}
	for _, n := range s.allow {
		if ip != nil && n.Contains(ip) {
			return true
		}
	}
	return false
}

func (s *Server) blocked(ip net.IP) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.fails[ip.String()]
	return f != nil && time.Now().Before(f.until)
}

func (s *Server) fail(ip net.IP) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, f := range s.fails {
		if now.Sub(f.first) > failWindow && now.After(f.until) {
			delete(s.fails, k)
		}
	}
	key := ip.String()
	f := s.fails[key]
	if f == nil {
		f = &failure{first: now}
		s.fails[key] = f
	}
	f.count++
	if f.count >= maxFails {
		f.until = now.Add(blockFor)
		f.count = 0
		f.first = now
		log.Printf("blocking %s for %s after %d failed requests", key, blockFor, maxFails)
	}
}

// Serve runs the HTTPS listener until ctx is done.
func Serve(ctx context.Context, cfg *config.Config, handler http.Handler) error {
	cert, err := tls.LoadX509KeyPair(config.CertPath, config.KeyPath)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:    net.JoinHostPort(cfg.Listen, strconv.Itoa(cfg.Port)),
		Handler: handler,
		TLSConfig: &tls.Config{
			MinVersion:   tls.VersionTLS13,
			Certificates: []tls.Certificate{cert},
		},
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    8 << 10,
		ErrorLog:          log.New(discard{}, "", 0),
	}
	go func() {
		<-ctx.Done()
		sh, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sh)
	}()
	log.Printf("listening on %s", srv.Addr)
	if err := srv.ListenAndServeTLS("", ""); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// discard drops TLS handshake noise from scanners.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
