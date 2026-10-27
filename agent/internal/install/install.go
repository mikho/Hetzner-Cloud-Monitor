// Package install sets up and removes the agent. It runs as root, and only
// when the dashboard runs it over SSH with the credentials the user typed in.
// The running service never has these rights.
package install

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"time"

	"hdash-agent/internal/config"
)

// Options are what the dashboard passes on the command line. Nil fields keep
// the current value, so a re-run updates the binary without losing anything.
type Options struct {
	SecretFile string
	Listen     *string
	Port       *int
	AllowFrom  *[]string
	Docker     *bool
	RotateCert bool
}

// Result is printed as one line prefixed with ResultPrefix, for the dashboard
// to read back over SSH. It holds no secret.
type Result struct {
	Version         string `json:"version"`
	Cert            string `json:"cert"`
	Port            int    `json:"port"`
	Listen          string `json:"listen"`
	Docker          bool   `json:"docker"`
	DockerInstalled bool   `json:"dockerInstalled"`
	Arch            string `json:"arch"`
}

const ResultPrefix = "HDASH-RESULT "

func requireRoot() error {
	if os.Geteuid() != 0 {
		return errors.New("must run as root")
	}
	if st, err := os.Stat("/run/systemd/system"); err != nil || !st.IsDir() {
		return errors.New("systemd is required")
	}
	return nil
}

func Install(version, arch string, o Options) (*Result, error) {
	if err := requireRoot(); err != nil {
		return nil, err
	}
	cfg, err := config.Load(config.ConfigPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) && o.SecretFile == "" {
			return nil, err
		}
		cfg = &config.Config{Port: config.DefaultPort, AllowFrom: []string{}}
	}
	if o.SecretFile != "" {
		secret, err := readSecret(o.SecretFile)
		if err != nil {
			return nil, err
		}
		cfg.Secret = secret
	}
	if o.Listen != nil {
		cfg.Listen = *o.Listen
	}
	if o.Port != nil {
		cfg.Port = *o.Port
	}
	if o.AllowFrom != nil {
		cfg.AllowFrom = *o.AllowFrom
	}
	if o.Docker != nil {
		cfg.Docker = *o.Docker
	}
	dockerGroup := false
	if _, err := user.LookupGroup("docker"); err == nil {
		dockerGroup = true
	}
	if cfg.Docker && !dockerGroup {
		return nil, errors.New("docker monitoring asked for, but there is no docker group on this server")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	if err := ensureUser(); err != nil {
		return nil, err
	}
	u, err := user.Lookup(config.UserName)
	if err != nil {
		return nil, err
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)

	if err := copySelf(); err != nil {
		return nil, fmt.Errorf("copy binary: %w", err)
	}

	// /etc/hdash-agent: root may write, the agent may only read.
	if err := os.MkdirAll(config.ConfigDir, 0750); err != nil {
		return nil, err
	}
	if err := chownMode(config.ConfigDir, 0, gid, 0750); err != nil {
		return nil, err
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	if err := config.WriteFileAtomic(config.ConfigPath, b, 0640); err != nil {
		return nil, err
	}
	if err := os.Chown(config.ConfigPath, 0, gid); err != nil {
		return nil, err
	}

	// /var/lib/hdash-agent: the agent's own, nobody else's.
	if err := os.MkdirAll(config.StateDir, 0700); err != nil {
		return nil, err
	}
	if err := chownMode(config.StateDir, uid, gid, 0700); err != nil {
		return nil, err
	}
	if _, err := os.Stat(config.CertPath); err != nil || o.RotateCert {
		if err := makeCert(uid, gid); err != nil {
			return nil, fmt.Errorf("certificate: %w", err)
		}
	}
	certPEM, err := os.ReadFile(config.CertPath)
	if err != nil {
		return nil, err
	}

	if err := os.WriteFile(config.UnitPath, []byte(unitFile(cfg.Docker)), 0644); err != nil {
		return nil, err
	}
	for _, args := range [][]string{
		{"daemon-reload"},
		{"enable", config.ServiceName},
		{"restart", config.ServiceName},
	} {
		if out, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	return &Result{
		Version: version, Cert: string(certPEM), Port: cfg.Port, Listen: cfg.Listen,
		Docker: cfg.Docker, DockerInstalled: dockerGroup, Arch: arch,
	}, nil
}

// readSecret reads the pairing key and deletes the file right away.
func readSecret(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	b, err := io.ReadAll(io.LimitReader(f, 256))
	f.Close()
	os.Remove(path)
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(b))
	if k, err := hex.DecodeString(s); err != nil || len(k) != 32 {
		return "", errors.New("secret file must hold 64 hex characters")
	}
	return s, nil
}

func ensureUser() error {
	if _, err := user.Lookup(config.UserName); err == nil {
		return nil
	}
	shell := "/usr/sbin/nologin"
	if _, err := os.Stat(shell); err != nil {
		shell = "/sbin/nologin"
	}
	out, err := exec.Command("useradd", "--system", "--user-group", "--no-create-home",
		"--home-dir", "/nonexistent", "--shell", shell, config.UserName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("useradd: %v: %s", err, out)
	}
	return nil
}

func copySelf() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if self == config.BinPath {
		return nil
	}
	src, err := os.ReadFile(self)
	if err != nil {
		return err
	}
	if err := config.WriteFileAtomic(config.BinPath, src, 0755); err != nil {
		return err
	}
	return os.Chown(config.BinPath, 0, 0)
}

func chownMode(path string, uid, gid int, mode os.FileMode) error {
	if err := os.Chown(path, uid, gid); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

// makeCert creates a self-signed ECDSA P-256 certificate. The dashboard gets
// the certificate over SSH during install and trusts only this one.
func makeCert(uid, gid int) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	host, _ := os.Hostname()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "hdash-agent " + host},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"hdash-agent"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := config.WriteFileAtomic(config.KeyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		return err
	}
	if err := config.WriteFileAtomic(config.CertPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0644); err != nil {
		return err
	}
	for _, p := range []string{config.KeyPath, config.CertPath} {
		if err := os.Chown(p, uid, gid); err != nil {
			return err
		}
	}
	return nil
}

func Uninstall() error {
	if err := requireRoot(); err != nil {
		return err
	}
	exec.Command("systemctl", "disable", "--now", config.ServiceName).Run()
	var errs []error
	for _, p := range []string{config.UnitPath, config.BinPath, config.ConfigDir, config.StateDir} {
		if err := os.RemoveAll(p); err != nil {
			errs = append(errs, err)
		}
	}
	exec.Command("systemctl", "daemon-reload").Run()
	if _, err := user.Lookup(config.UserName); err == nil {
		if out, err := exec.Command("userdel", config.UserName).CombinedOutput(); err != nil {
			errs = append(errs, fmt.Errorf("userdel: %v: %s", err, out))
		}
	}
	return errors.Join(errs...)
}

func unitFile(docker bool) string {
	groups := ""
	if docker {
		// Opt-in only. Reading the Docker socket needs the docker group.
		groups = "SupplementaryGroups=docker\n"
	}
	return `[Unit]
Description=Hetzner Dashboard agent (read-only)
After=network-online.target docker.service
Wants=network-online.target

[Service]
User=` + config.UserName + `
Group=` + config.UserName + `
` + groups + `ExecStart=` + config.BinPath + ` run
Restart=always
RestartSec=5
StateDirectory=hdash-agent
StateDirectoryMode=0700
UMask=0077
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
ProtectProc=default
RestrictSUIDSGID=yes
RestrictRealtime=yes
RestrictNamespaces=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
RemoveIPC=yes
CapabilityBoundingSet=
AmbientCapabilities=
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
SystemCallArchitectures=native
SystemCallFilter=@system-service
SystemCallFilter=~@privileged @mount @swap @reboot @raw-io @module @debug @cpu-emulation @obsolete
SystemCallErrorNumber=EPERM

[Install]
WantedBy=multi-user.target
`
}
