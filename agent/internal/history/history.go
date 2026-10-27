// Package history keeps recent samples so charts fill in even when the
// dashboard was not polling: one-minute samples for 24 hours and five-minute
// averages for 7 days. It is saved to disk now and then and read on start.
package history

import (
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"

	"hdash-agent/internal/collect"
	"hdash-agent/internal/config"
)

const (
	fineKeep   = 24 * time.Hour
	coarseKeep = 7 * 24 * time.Hour
	coarseStep = 300
	saveEvery  = 10 * time.Minute
)

type History struct {
	path string

	mu      sync.Mutex
	Fine    []collect.Sample `json:"fine"`
	Coarse  []collect.Sample `json:"coarse"`
	bucket  []collect.Sample
	savedAt time.Time
}

type Response struct {
	Time    int64            `json:"time"`
	Step    int              `json:"step"`
	Samples []collect.Sample `json:"samples"`
}

func Open(path string) *History {
	h := &History{path: path, savedAt: time.Now()}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, h); err != nil {
			log.Printf("history: ignoring %s: %v", path, err)
		}
	}
	return h
}

func (h *History) Add(s collect.Sample) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Fine = append(h.Fine, s)
	h.Fine = trim(h.Fine, s.T-int64(fineKeep.Seconds()))

	if len(h.bucket) > 0 && h.bucket[0].T/coarseStep != s.T/coarseStep {
		h.Coarse = append(h.Coarse, average(h.bucket))
		h.Coarse = trim(h.Coarse, s.T-int64(coarseKeep.Seconds()))
		h.bucket = h.bucket[:0]
	}
	h.bucket = append(h.bucket, s)

	if time.Since(h.savedAt) > saveEvery {
		h.saveLocked()
	}
}

func (h *History) Save() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.saveLocked()
}

func (h *History) saveLocked() {
	h.savedAt = time.Now()
	b, err := json.Marshal(h)
	if err != nil {
		return
	}
	if err := config.WriteFileAtomic(h.path, b, 0600); err != nil {
		log.Printf("history: %v", err)
	}
}

func (h *History) Query(since, now int64) Response {
	h.mu.Lock()
	defer h.mu.Unlock()
	src, step := h.Coarse, coarseStep
	if since >= now-int64(fineKeep.Seconds()) {
		src, step = h.Fine, 60
	}
	out := []collect.Sample{}
	for _, s := range src {
		if s.T >= since {
			out = append(out, s)
		}
	}
	return Response{Time: now, Step: step, Samples: out}
}

func trim(s []collect.Sample, before int64) []collect.Sample {
	i := 0
	for i < len(s) && s[i].T < before {
		i++
	}
	return s[i:]
}

func average(b []collect.Sample) collect.Sample {
	var a collect.Sample
	for _, s := range b {
		a.CPU += s.CPU
		a.Memory += s.Memory
		a.Swap += s.Swap
		a.Disk += s.Disk
		a.Load += s.Load
		a.Rx += s.Rx
		a.Tx += s.Tx
	}
	n := float64(len(b))
	a.T = b[0].T / coarseStep * coarseStep
	a.CPU, a.Memory, a.Swap, a.Disk, a.Load, a.Rx, a.Tx = a.CPU/n, a.Memory/n, a.Swap/n, a.Disk/n, a.Load/n, a.Rx/n, a.Tx/n
	return a
}
