// Package config holds the agent's settings and file locations.
package config

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

const (
	ServiceName = "hdash-agent"
	UserName    = "hdash"
	BinPath     = "/usr/local/bin/hdash-agent"
	UnitPath    = "/etc/systemd/system/hdash-agent.service"
	ConfigDir   = "/etc/hdash-agent"
	ConfigPath  = ConfigDir + "/config.json"
	StateDir    = "/var/lib/hdash-agent"
	CertPath    = StateDir + "/tls.crt"
	KeyPath     = StateDir + "/tls.key"
	HistoryPath = StateDir + "/history.json"
	DefaultPort = 7411
)

type Config struct {
	// Secret is the 32-byte key shared with the dashboard backend, hex encoded.
	Secret string `json:"secret"`
	// Listen is the address to bind to. Empty means all addresses.
	Listen string `json:"listen"`
	Port   int    `json:"port"`
	// AllowFrom limits which source addresses may connect. Empty allows all.
	AllowFrom []string `json:"allowFrom"`
	// Docker is true when the user opted in to Docker monitoring.
	Docker bool `json:"docker"`
}

func (c *Config) Key() []byte {
	k, _ := hex.DecodeString(c.Secret)
	return k
}

func (c *Config) Validate() error {
	if k, err := hex.DecodeString(c.Secret); err != nil || len(k) != 32 {
		return errors.New("secret must be 64 hex characters")
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("bad port %d", c.Port)
	}
	if c.Listen != "" && net.ParseIP(c.Listen) == nil {
		return fmt.Errorf("listen must be an IP address, got %q", c.Listen)
	}
	for _, a := range c.AllowFrom {
		if _, err := ParseAllow(a); err != nil {
			return err
		}
	}
	return nil
}

// ParseAllow accepts a CIDR or a single IP address.
func ParseAllow(s string) (*net.IPNet, error) {
	if _, n, err := net.ParseCIDR(s); err == nil {
		return n, nil
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return nil, fmt.Errorf("allow-from: %q is not an IP address or CIDR", s)
	}
	bits := 128
	if ip.To4() != nil {
		ip, bits = ip.To4(), 32
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}, nil
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.Port == 0 {
		c.Port = DefaultPort
	}
	return &c, c.Validate()
}

// WriteFileAtomic writes to a temp file in the same directory and renames it,
// so a reader never sees a half-written file.
func WriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
