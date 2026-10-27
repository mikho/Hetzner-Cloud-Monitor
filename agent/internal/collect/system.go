package collect

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

type System struct {
	RebootRequired bool     `json:"rebootRequired"`
	FailedUnits    []string `json:"failedUnits"`
}

// Updates describes package updates the server's package manager knows about.
// The agent never refreshes package lists or installs anything; it reads the
// cache the system's own timers keep fresh (apt-daily, dnf-makecache).
type Updates struct {
	Manager  string   `json:"manager"`
	Total    int      `json:"total"`
	Security int      `json:"security"`
	Packages []string `json:"packages"`
	// ListsTime is when the package lists were last refreshed, or 0 if unknown.
	ListsTime int64  `json:"listsTime"`
	CheckedAt int64  `json:"checkedAt"`
	Error     string `json:"error,omitempty"`
}

const maxPackages = 100

func readSystem(ctx context.Context) System {
	s := System{FailedUnits: []string{}}
	for _, p := range []string{"/run/reboot-required", "/var/run/reboot-required"} {
		if _, err := os.Stat(p); err == nil {
			s.RebootRequired = true
		}
	}
	out, err := run(ctx, 10*time.Second, "systemctl", "list-units", "--state=failed", "--no-legend", "--plain", "--no-pager")
	if err == nil {
		s.FailedUnits = parseFailedUnits(out)
	}
	return s
}

func parseFailedUnits(out []byte) []string {
	units := []string{}
	for _, line := range bytes.Split(out, []byte("\n")) {
		if f := strings.Fields(string(line)); len(f) > 0 {
			units = append(units, f[0])
		}
	}
	return units
}

func run(ctx context.Context, timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"}
	return cmd.Output()
}

func readUpdates(ctx context.Context) *Updates {
	id, like := osID(readString("/etc/os-release"))
	family := " " + id + " " + like + " "
	now := time.Now().Unix()
	switch {
	case strings.Contains(family, " debian ") || strings.Contains(family, " ubuntu "):
		u := &Updates{Manager: "apt", CheckedAt: now, Packages: []string{}}
		u.ListsTime = aptListsTime()
		// -s only simulates; it works without root and changes nothing.
		out, err := run(ctx, 2*time.Minute, "apt-get", "-s", "-o", "Debug::NoLocking=1", "dist-upgrade")
		if err != nil {
			u.Error = "apt-get -s failed: " + err.Error()
			return u
		}
		u.Total, u.Security, u.Packages = parseAptSimulation(out)
		return u
	case strings.Contains(family, " rhel ") || strings.Contains(family, " fedora ") || strings.Contains(family, " centos "):
		u := &Updates{Manager: "dnf", CheckedAt: now, Packages: []string{}}
		// Cache only: never download metadata, only read what dnf-makecache stored.
		out, err := run(ctx, 2*time.Minute, "dnf", "-q", "--cacheonly", "check-update")
		// check-update exits 100 when updates exist.
		if ee, ok := err.(*exec.ExitError); err != nil && !(ok && ee.ExitCode() == 100) {
			u.Error = "dnf check-update failed: " + err.Error()
			return u
		}
		u.Packages = parseDnfList(out)
		u.Total = len(u.Packages)
		if sec, err := run(ctx, 2*time.Minute, "dnf", "-q", "--cacheonly", "updateinfo", "list", "--security"); err == nil {
			u.Security = countDnfSecurity(sec)
		}
		if len(u.Packages) > maxPackages {
			u.Packages = u.Packages[:maxPackages]
		}
		return u
	}
	return nil
}

func aptListsTime() int64 {
	for _, p := range []string{"/var/lib/apt/periodic/update-success-stamp", "/var/lib/apt/lists"} {
		if st, err := os.Stat(p); err == nil {
			return st.ModTime().Unix()
		}
	}
	return 0
}

// parseAptSimulation reads "Inst" lines of `apt-get -s`, for example
// Inst libssl3 [3.0.2-0ubuntu1.15] (3.0.2-0ubuntu1.16 Ubuntu:22.04/jammy-security [amd64])
func parseAptSimulation(out []byte) (total, security int, pkgs []string) {
	pkgs = []string{}
	for _, line := range strings.Split(string(out), "\n") {
		rest, ok := strings.CutPrefix(line, "Inst ")
		if !ok {
			continue
		}
		total++
		name, _, _ := strings.Cut(rest, " ")
		if _, origin, ok := strings.Cut(rest, "("); ok && strings.Contains(strings.ToLower(origin), "-security") {
			security++
		}
		if len(pkgs) < maxPackages {
			pkgs = append(pkgs, name)
		}
	}
	sort.Strings(pkgs)
	return
}

// parseDnfList returns package names from `dnf check-update`, whose lines are
// "name.arch  version  repo".
func parseDnfList(out []byte) []string {
	pkgs := []string{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || strings.HasPrefix(line, " ") || f[0] == "Obsoleting" {
			continue
		}
		pkgs = append(pkgs, f[0])
	}
	return pkgs
}

// countDnfSecurity counts lines of `dnf updateinfo list --security`, whose
// lines are "ADVISORY  severity/Sec.  package".
func countDnfSecurity(out []byte) int {
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) == 3 && strings.HasSuffix(f[1], "Sec.") {
			n++
		}
	}
	return n
}
