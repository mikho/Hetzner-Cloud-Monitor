// Package collect reads the server's state. Everything here only reads:
// files under /proc, /etc and /run, statfs, `systemctl list-units`,
// `apt-get -s` / `dnf --cacheonly`, and GET requests to Docker.
package collect

import (
	"context"
	"encoding/json"
	"log"
	"math"
	"sync"
	"time"
)

const (
	Interval       = 60 * time.Second
	updatesEvery   = 30 * time.Minute
	serverIDEvery  = time.Hour
	cpuWindowCount = 5
)

type Snapshot struct {
	Version string  `json:"version"`
	Time    int64   `json:"time"`
	Host    Host    `json:"host"`
	CPU     float64 `json:"cpu"`
	// CPU5 is the average of the last five one-minute CPU readings.
	CPU5    float64  `json:"cpu5"`
	Memory  Memory   `json:"memory"`
	Disks   []Disk   `json:"disks"`
	Network Network  `json:"network"`
	System  System   `json:"system"`
	Updates *Updates `json:"updates"`
	Docker  *Docker  `json:"docker"`
}

type Network struct {
	RxBytesPerSec float64 `json:"rxBytesPerSec"`
	TxBytesPerSec float64 `json:"txBytesPerSec"`
}

// Sample is one point of history.
type Sample struct {
	T      int64   `json:"t"`
	CPU    float64 `json:"cpu"`
	Memory float64 `json:"memory"`
	Swap   float64 `json:"swap"`
	Disk   float64 `json:"disk"`
	Load   float64 `json:"load"`
	Rx     float64 `json:"rx"`
	Tx     float64 `json:"tx"`
}

type Collector struct {
	version string
	docker  bool
	onTick  func(Sample)

	mu     sync.RWMutex
	latest []byte
	ready  chan struct{}

	prevBusy, prevTotal uint64
	prevRx, prevTx      uint64
	prevAt              time.Time
	cpuWindow           []float64
	serverID            *int64
	serverIDAt          time.Time
	updates             *Updates
	updatesAt           time.Time
}

func NewCollector(version string, docker bool, onTick func(Sample)) *Collector {
	return &Collector{version: version, docker: docker, onTick: onTick, ready: make(chan struct{})}
}

func (c *Collector) Ready() <-chan struct{} { return c.ready }

func (c *Collector) Latest() []byte {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.latest
}

func (c *Collector) Run(ctx context.Context) {
	// A short first interval so CPU and network rates exist right away.
	c.prime()
	select {
	case <-time.After(3 * time.Second):
	case <-ctx.Done():
		return
	}
	c.tick(ctx)
	close(c.ready)
	t := time.NewTicker(Interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			c.tick(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (c *Collector) prime() {
	c.prevBusy, c.prevTotal = cpuTimes(readString("/proc/stat"))
	c.prevRx, c.prevTx = netBytes(readString("/proc/net/dev"))
	c.prevAt = time.Now()
}

func (c *Collector) tick(ctx context.Context) {
	now := time.Now()
	s := Snapshot{Version: c.version, Time: now.Unix()}
	s.Host = readHost()
	s.Memory = readMemory()
	s.Disks = readDisks()
	s.System = readSystem(ctx)

	busy, total := cpuTimes(readString("/proc/stat"))
	if dt := total - c.prevTotal; total > c.prevTotal && dt > 0 {
		s.CPU = round1(float64(busy-c.prevBusy) / float64(dt) * 100)
	}
	c.prevBusy, c.prevTotal = busy, total
	c.cpuWindow = append(c.cpuWindow, s.CPU)
	if len(c.cpuWindow) > cpuWindowCount {
		c.cpuWindow = c.cpuWindow[1:]
	}
	sum := 0.0
	for _, v := range c.cpuWindow {
		sum += v
	}
	s.CPU5 = round1(sum / float64(len(c.cpuWindow)))

	rx, tx := netBytes(readString("/proc/net/dev"))
	if secs := now.Sub(c.prevAt).Seconds(); secs > 0 && rx >= c.prevRx && tx >= c.prevTx {
		s.Network = Network{RxBytesPerSec: math.Round(float64(rx-c.prevRx) / secs), TxBytesPerSec: math.Round(float64(tx-c.prevTx) / secs)}
	}
	c.prevRx, c.prevTx, c.prevAt = rx, tx, now

	if c.serverID == nil && now.Sub(c.serverIDAt) > serverIDEvery {
		c.serverID = hetznerServerID(ctx)
		c.serverIDAt = now
	}
	s.Host.ServerID = c.serverID

	if now.Sub(c.updatesAt) > updatesEvery {
		c.updates = readUpdates(ctx)
		c.updatesAt = now
	}
	s.Updates = c.updates

	s.Docker = readDocker(ctx, c.docker)
	if s.Docker != nil {
		for i := range s.Docker.Containers {
			// Docker reports the host's memory as the limit when there is none.
			if l := s.Docker.Containers[i].MemoryLimit; l != nil && *l >= s.Memory.Total {
				s.Docker.Containers[i].MemoryLimit = nil
			}
		}
	}

	b, err := json.Marshal(s)
	if err != nil {
		log.Printf("snapshot: %v", err)
		return
	}
	c.mu.Lock()
	c.latest = b
	c.mu.Unlock()

	if c.onTick != nil {
		c.onTick(sampleOf(&s))
	}
}

func sampleOf(s *Snapshot) Sample {
	smp := Sample{T: s.Time, CPU: s.CPU, Rx: s.Network.RxBytesPerSec, Tx: s.Network.TxBytesPerSec}
	if s.Memory.Total > 0 {
		smp.Memory = round3(1 - float64(s.Memory.Available)/float64(s.Memory.Total))
	}
	if s.Memory.SwapTotal > 0 {
		smp.Swap = round3(1 - float64(s.Memory.SwapFree)/float64(s.Memory.SwapTotal))
	}
	for _, d := range s.Disks {
		if d.Total > 0 {
			smp.Disk = math.Max(smp.Disk, round3(float64(d.Used)/float64(d.Total)))
		}
	}
	if s.Host.Cores > 0 {
		smp.Load = round3(s.Host.Load[0] / float64(s.Host.Cores))
	}
	return smp
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
