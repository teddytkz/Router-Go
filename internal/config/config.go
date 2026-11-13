// Package config resolves runtime settings from the environment.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const DefaultPort = 20128

// Where the listening port came from, highest priority first.
const (
	PortFromFlag     = "flag"     // --port
	PortFromEnv      = "env"      // PORT / ROUTER_GO_ADDR
	PortFromSettings = "settings" // saved in the dashboard
	PortFromDefault  = "default"
)

type Config struct {
	DataDir         string
	DBPath          string
	Host            string // bind host (HOSTNAME_BIND / ROUTER_GO_ADDR), empty = decided by the localOnly setting
	JWTSecret       []byte
	APIKeySecret    string
	MachineID       string
	InitialPassword string
	SecureCookie    bool
	TrustProxy      bool

	// Rebind moves the running server to another port (set by main). It binds the new port first, so a busy port is an error.
	Rebind func(port int) error
	// CheckUpdate asks the update server whether it publishes another binary (set by main); the message goes to the dashboard.
	CheckUpdate func() (diff bool, msg string, err error)

	mu         sync.Mutex
	port       int
	portSource string
	envPort    int
	localOnly  bool // Settings → "Local only": bind 127.0.0.1 instead of all interfaces
}

// PortInfo is the port the server is (about to be) listening on and why.
func (c *Config) PortInfo() (int, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.port, c.portSource
}

func (c *Config) SetPort(port int, source string) {
	c.mu.Lock()
	c.port, c.portSource = port, source
	c.mu.Unlock()
}

// SetLocalOnly records the saved "Local only" setting.
func (c *Config) SetLocalOnly(v bool) {
	c.mu.Lock()
	c.localOnly = v
	c.mu.Unlock()
}

func (c *Config) LocalOnly() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.localOnly
}

// BindHost is the interface to listen on: HOSTNAME_BIND / ROUTER_GO_ADDR win, then the localOnly setting, else all interfaces ("").
func (c *Config) BindHost() string {
	if c.Host != "" {
		return c.Host
	}
	if c.LocalOnly() {
		return "127.0.0.1" // shortcut: IPv4 loopback only, so http://[::1]:port is refused; upgrade if IPv6-only clients matter
	}
	return ""
}

// Addr is the listen address for the current port.
func (c *Config) Addr() string {
	p, _ := c.PortInfo()
	return net.JoinHostPort(c.BindHost(), strconv.Itoa(p))
}

// ValidPort accepts 1-65535.
func ValidPort(p int) bool { return p >= 1 && p <= 65535 }

// ResolvePort picks the listening port: --port, then the PORT/ROUTER_GO_ADDR environment, then the port saved in
// settings, then 20128. flagPort and saved are 0 when unset.
func (c *Config) ResolvePort(flagPort, saved int) error {
	switch {
	case flagPort != 0:
		if !ValidPort(flagPort) {
			return fmt.Errorf("invalid --port %d (want 1-65535)", flagPort)
		}
		c.SetPort(flagPort, PortFromFlag)
	case c.envPort != 0:
		c.SetPort(c.envPort, PortFromEnv)
	case ValidPort(saved):
		c.SetPort(saved, PortFromSettings)
	default:
		c.SetPort(DefaultPort, PortFromDefault)
	}
	return nil
}

// Load reads env vars. Data lives in ~/.router-go (%USERPROFILE%\.router-go on Windows).
func Load() (*Config, error) {
	dir := firstEnv("ROUTER_GO_DATA_DIR", "DATA_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(home, ".router-go")
	}
	if err := os.MkdirAll(filepath.Join(dir, "db"), 0o700); err != nil {
		return nil, err
	}
	secret, err := persistedSecret(filepath.Join(dir, "jwt-secret"), os.Getenv("JWT_SECRET"), 32)
	if err != nil {
		return nil, err
	}
	machine, err := persistedSecret(filepath.Join(dir, "machine-id"), "", 8) // 16 hex chars
	if err != nil {
		return nil, err
	}
	host, envPort := os.Getenv("HOSTNAME_BIND"), 0
	if addr := os.Getenv("ROUTER_GO_ADDR"); addr != "" {
		h, p, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("ROUTER_GO_ADDR %q: %w", addr, err)
		}
		host = h
		envPort, _ = strconv.Atoi(p)
	} else if p := firstEnv("PORT"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || !ValidPort(n) {
			return nil, fmt.Errorf("invalid PORT %q (want 1-65535)", p)
		}
		envPort = n
	}
	pw := strings.TrimSpace(os.Getenv("INITIAL_PASSWORD"))
	if pw == "" {
		pw = "123456"
	}
	apiSecret := os.Getenv("API_KEY_SECRET")
	if apiSecret == "" {
		apiSecret = "endpoint-proxy-api-key-secret" // same default as XRouter-Next, keeps old keys valid
	}
	return &Config{
		DataDir:         dir,
		DBPath:          filepath.Join(dir, "db", "data.sqlite"),
		Host:            host,
		envPort:         envPort,
		JWTSecret:       []byte(secret),
		APIKeySecret:    apiSecret,
		MachineID:       machine,
		InitialPassword: pw,
		SecureCookie:    os.Getenv("AUTH_COOKIE_SECURE") == "true",
		TrustProxy:      os.Getenv("TRUST_PROXY") == "true",
	}, nil
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// persistedSecret returns override if set, else the file content, else a fresh random hex value saved with 0600.
func persistedSecret(path, override string, nBytes int) (string, error) {
	if override != "" {
		return override, nil
	}
	if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		return strings.TrimSpace(string(b)), nil
	}
	buf := make([]byte, nBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	s := hex.EncodeToString(buf)
	return s, os.WriteFile(path, []byte(s), 0o600)
}

// Version is overridden at build time with -ldflags "-X router-go/internal/config.Version=...".
var Version = "0.1.0"
