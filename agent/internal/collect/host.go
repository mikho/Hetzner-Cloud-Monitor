package collect

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Host struct {
	Hostname string     `json:"hostname"`
	OS       string     `json:"os"`
	Kernel   string     `json:"kernel"`
	Arch     string     `json:"arch"`
	ServerID *int64     `json:"serverID"`
	BootTime int64      `json:"bootTime"`
	Cores    int        `json:"cores"`
	Load     [3]float64 `json:"load"`
}

type Memory struct {
	Total     uint64 `json:"total"`
	Available uint64 `json:"available"`
	SwapTotal uint64 `json:"swapTotal"`
	SwapFree  uint64 `json:"swapFree"`
}

type Disk struct {
	Mount       string `json:"mount"`
	Device      string `json:"device"`
	FS          string `json:"fs"`
	Total       uint64 `json:"total"`
	Used        uint64 `json:"used"`
	Available   uint64 `json:"available"`
	InodesTotal uint64 `json:"inodesTotal"`
	InodesUsed  uint64 `json:"inodesUsed"`
	ReadOnly    bool   `json:"readOnly"`
}

func readHost() Host {
	h := Host{Arch: runtime.GOARCH, Cores: runtime.NumCPU()}
	h.Hostname, _ = os.Hostname()
	h.Kernel = strings.TrimSpace(readString("/proc/sys/kernel/osrelease"))
	h.OS = osName(readString("/etc/os-release"))
	h.BootTime = bootTime(readString("/proc/stat"))
	h.Load = parseLoad(readString("/proc/loadavg"))
	return h
}

func readString(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

func osName(osRelease string) string {
	for _, line := range strings.Split(osRelease, "\n") {
		if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(v, `"'`)
		}
	}
	return "Linux"
}

func osID(osRelease string) (id string, like string) {
	for _, line := range strings.Split(osRelease, "\n") {
		if v, ok := strings.CutPrefix(line, "ID="); ok {
			id = strings.Trim(v, `"'`)
		}
		if v, ok := strings.CutPrefix(line, "ID_LIKE="); ok {
			like = strings.Trim(v, `"'`)
		}
	}
	return
}

func bootTime(stat string) int64 {
	for _, line := range strings.Split(stat, "\n") {
		if v, ok := strings.CutPrefix(line, "btime "); ok {
			n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			return n
		}
	}
	return 0
}

func parseLoad(s string) [3]float64 {
	var out [3]float64
	f := strings.Fields(s)
	for i := 0; i < 3 && i < len(f); i++ {
		out[i], _ = strconv.ParseFloat(f[i], 64)
	}
	return out
}

func readMemory() Memory {
	return parseMeminfo(readString("/proc/meminfo"))
}

func parseMeminfo(s string) Memory {
	var m Memory
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		n, _ := strconv.ParseUint(f[1], 10, 64)
		n *= 1024
		switch f[0] {
		case "MemTotal:":
			m.Total = n
		case "MemAvailable:":
			m.Available = n
		case "SwapTotal:":
			m.SwapTotal = n
		case "SwapFree:":
			m.SwapFree = n
		}
	}
	return m
}

// cpuTimes returns busy and total jiffies from the first line of /proc/stat.
func cpuTimes(stat string) (busy, total uint64) {
	line, _, _ := strings.Cut(stat, "\n")
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return 0, 0
	}
	for i, v := range f[1:] {
		n, _ := strconv.ParseUint(v, 10, 64)
		total += n
		// idle (3) and iowait (4) are not busy time
		if i != 3 && i != 4 {
			busy += n
		}
	}
	return busy, total
}

// netBytes sums received and sent bytes over all interfaces except loopback.
func netBytes(dev string) (rx, tx uint64) {
	sc := bufio.NewScanner(strings.NewReader(dev))
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "lo" || strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "br-") {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		r, _ := strconv.ParseUint(f[0], 10, 64)
		t, _ := strconv.ParseUint(f[8], 10, 64)
		rx += r
		tx += t
	}
	return
}

var realFS = map[string]bool{
	"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true, "zfs": true,
	"f2fs": true, "jfs": true, "reiserfs": true, "vfat": true, "exfat": true,
}

func readDisks() []Disk {
	disks := []Disk{}
	seen := map[string]bool{}
	for _, line := range strings.Split(readString("/proc/self/mounts"), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || !realFS[f[2]] || seen[f[0]] {
			continue
		}
		// Snap and similar loop images are always full and say nothing.
		if strings.HasPrefix(f[0], "/dev/loop") {
			continue
		}
		mount := unescapeMount(f[1])
		var st syscall.Statfs_t
		if syscall.Statfs(mount, &st) != nil || st.Blocks == 0 {
			continue
		}
		seen[f[0]] = true
		bs := uint64(st.Bsize)
		d := Disk{
			Mount: mount, Device: f[0], FS: f[2],
			Total:       st.Blocks * bs,
			Used:        (st.Blocks - st.Bfree) * bs,
			Available:   st.Bavail * bs,
			InodesTotal: st.Files,
			ReadOnly:    hasOption(f[3], "ro"),
		}
		if st.Files > 0 {
			d.InodesUsed = st.Files - st.Ffree
		}
		disks = append(disks, d)
	}
	return disks
}

func hasOption(opts, want string) bool {
	for _, o := range strings.Split(opts, ",") {
		if o == want {
			return true
		}
	}
	return false
}

// unescapeMount decodes the octal escapes (\040 for a space) in /proc/mounts.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b bytes.Buffer
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// hetznerServerID asks Hetzner's metadata service which server this is.
// Returns nil on other providers.
func hetznerServerID(ctx context.Context) *int64 {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://169.254.169.254/hetzner/v1/metadata/instance-id", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 64))
	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return nil
	}
	return &n
}
