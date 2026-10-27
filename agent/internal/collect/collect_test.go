package collect

import "testing"

func TestParseAptSimulation(t *testing.T) {
	out := []byte(`NOTE: This is only a simulation!
Inst libssl3 [3.0.2-0ubuntu1.15] (3.0.2-0ubuntu1.16 Ubuntu:22.04/jammy-updates, Ubuntu:22.04/jammy-security [amd64])
Inst curl [7.81.0-1ubuntu1.15] (7.81.0-1ubuntu1.16 Ubuntu:22.04/jammy-updates [amd64])
Conf libssl3 (3.0.2-0ubuntu1.16 Ubuntu:22.04/jammy-updates [amd64])
Inst base-files [12.4] (12.4+deb12u5 Debian:12.5/stable, Debian-Security:12/stable-security [arm64])
`)
	total, sec, pkgs := parseAptSimulation(out)
	if total != 3 || sec != 2 {
		t.Fatalf("total=%d security=%d", total, sec)
	}
	if len(pkgs) != 3 || pkgs[0] != "base-files" {
		t.Fatalf("pkgs=%v", pkgs)
	}
}

func TestParseDnf(t *testing.T) {
	list := []byte(`
openssl.x86_64            1:3.0.7-27.el9          baseos
kernel.x86_64             5.14.0-427.el9          baseos
Obsoleting Packages
`)
	if got := parseDnfList(list); len(got) != 2 || got[0] != "openssl.x86_64" {
		t.Fatalf("got %v", got)
	}
	sec := []byte("RHSA-2024:1 Important/Sec. openssl-1:3.0.7-27.el9.x86_64\nRHBA-2024:2 bugfix kernel-5.14\n")
	if n := countDnfSecurity(sec); n != 1 {
		t.Fatalf("security=%d", n)
	}
}

func TestParseMeminfoAndCPU(t *testing.T) {
	m := parseMeminfo("MemTotal:  1000 kB\nMemAvailable: 250 kB\nSwapTotal: 0 kB\n")
	if m.Total != 1024000 || m.Available != 256000 {
		t.Fatalf("mem=%+v", m)
	}
	busy, total := cpuTimes("cpu  10 0 10 70 10 0 0 0 0 0\ncpu0 1 2 3\n")
	if busy != 20 || total != 100 {
		t.Fatalf("busy=%d total=%d", busy, total)
	}
	rx, tx := netBytes("Inter-|\n face |\n    lo: 100 0 0 0 0 0 0 0 100 0 0 0 0 0 0 0\n  eth0: 500 0 0 0 0 0 0 0 700 0 0 0 0 0 0 0\n")
	if rx != 500 || tx != 700 {
		t.Fatalf("rx=%d tx=%d", rx, tx)
	}
	if unescapeMount(`/mnt/my\040disk`) != "/mnt/my disk" {
		t.Fatal("unescape")
	}
}
