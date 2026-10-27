// Package auth verifies signed requests from the dashboard backend.
//
// Every request carries a timestamp, a random nonce and an HMAC-SHA256
// signature over the method, the request URI, the timestamp and the nonce.
// The shared secret itself never travels over the network. The timestamp
// window and the nonce cache together stop replayed requests.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"
)

const (
	HeaderTime      = "X-Hdash-Timestamp"
	HeaderNonce     = "X-Hdash-Nonce"
	HeaderSignature = "X-Hdash-Signature"

	// MaxSkew is how far the backend's clock may be from the agent's.
	MaxSkew = 60 * time.Second
	// maxNonces bounds memory use. Only nonces of valid requests are stored,
	// so an attacker without the key cannot fill the cache.
	maxNonces = 20000
)

var nonceRe = regexp.MustCompile(`^[0-9a-f]{32,64}$`)

var (
	ErrMissing   = errors.New("missing signature headers")
	ErrClock     = errors.New("timestamp outside the allowed window")
	ErrSignature = errors.New("bad signature")
	ErrReplay    = errors.New("nonce already used")
)

// Message is the exact byte string that is signed.
func Message(method, uri, timestamp, nonce string) []byte {
	return []byte("hdash-agent-v1\n" + method + "\n" + uri + "\n" + timestamp + "\n" + nonce)
}

// Sign returns the hex signature for a request. Used by tests and by the
// backend's reference implementation.
func Sign(key []byte, method, uri, timestamp, nonce string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(Message(method, uri, timestamp, nonce))
	return hex.EncodeToString(mac.Sum(nil))
}

type Verifier struct {
	key  []byte
	mu   sync.Mutex
	seen map[string]time.Time
}

func NewVerifier(key []byte) *Verifier {
	return &Verifier{key: key, seen: map[string]time.Time{}}
}

func (v *Verifier) Check(r *http.Request, now time.Time) error {
	ts, nonce, sig := r.Header.Get(HeaderTime), r.Header.Get(HeaderNonce), r.Header.Get(HeaderSignature)
	if ts == "" || nonce == "" || sig == "" || !nonceRe.MatchString(nonce) {
		return ErrMissing
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return ErrMissing
	}
	if d := now.Sub(time.Unix(sec, 0)); d > MaxSkew || d < -MaxSkew {
		return ErrClock
	}
	given, err := hex.DecodeString(sig)
	if err != nil {
		return ErrSignature
	}
	mac := hmac.New(sha256.New, v.key)
	mac.Write(Message(r.Method, r.URL.RequestURI(), ts, nonce))
	if !hmac.Equal(given, mac.Sum(nil)) {
		return ErrSignature
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	if _, used := v.seen[nonce]; used {
		return ErrReplay
	}
	if len(v.seen) >= maxNonces {
		for n, at := range v.seen {
			if now.Sub(at) > 2*MaxSkew {
				delete(v.seen, n)
			}
		}
		if len(v.seen) >= maxNonces {
			return ErrReplay
		}
	}
	v.seen[nonce] = now
	return nil
}
