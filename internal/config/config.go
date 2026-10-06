package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Rule types. An empty type means RuleExec for backwards compatibility.
const (
	RuleExec     = "exec"     // pattern matches a simple command, e.g. "tail *"
	RuleDownload = "download" // pattern matches a remote path, e.g. "/var/log/*"
	RuleUpload   = "upload"   // pattern matches a remote path
	RuleForward  = "forward"  // pattern matches "host:port", e.g. "localhost:5432"
)

type ServerConfig struct {
	Host        string `yaml:"host"`                  // user@hostname or an ~/.ssh/config alias
	Port        int    `yaml:"port,omitempty"`        // SSH port (default 22)
	Description string `yaml:"description,omitempty"` // human-readable description
}

type WhitelistRule struct {
	Type    string   `yaml:"type,omitempty"` // exec (default), download, upload, forward
	Pattern string   `yaml:"pattern"`        // glob pattern, * matches anything
	Servers []string `yaml:"servers"`        // server aliases (["*"] for all)
	Note    string   `yaml:"note,omitempty"` // human-readable note
}

// Kind returns the normalized rule type.
func (r WhitelistRule) Kind() string {
	if r.Type == "" {
		return RuleExec
	}
	return r.Type
}

// AppliesTo reports whether the rule covers the given server alias.
func (r WhitelistRule) AppliesTo(server string) bool {
	for _, s := range r.Servers {
		if s == "*" || s == server {
			return true
		}
	}
	return false
}

// AutoApproveConfig tunes the temporary auto-approve mode.
type AutoApproveConfig struct {
	MaxMinutes              int      `yaml:"max_minutes,omitempty"`               // upper bound of one session (default 240)
	Blocklist               []string `yaml:"blocklist,omitempty"`                 // extra patterns that always need a human
	DisableDefaultBlocklist bool     `yaml:"disable_default_blocklist,omitempty"` // drop the built-in patterns
	MaxPerMinute            int      `yaml:"max_per_minute,omitempty"`            // circuit breaker: requests/minute (default 30)
	MaxConsecutiveFailures  int      `yaml:"max_consecutive_failures,omitempty"`  // circuit breaker: non-zero exits in a row (default 5)
}

// ForwardConfig tunes port forwarding.
type ForwardConfig struct {
	DefaultTTLMinutes int `yaml:"default_ttl_minutes,omitempty"` // default 30
	MaxTTLMinutes     int `yaml:"max_ttl_minutes,omitempty"`     // default 240
}

type Config struct {
	Servers     map[string]ServerConfig `yaml:"servers"`
	Timeout     int                     `yaml:"timeout"` // default approval timeout in seconds
	Whitelist   []WhitelistRule         `yaml:"whitelist"`
	AutoApprove AutoApproveConfig       `yaml:"auto_approve,omitempty"`
	Forward     ForwardConfig           `yaml:"forward,omitempty"`
}

// DefaultDir returns the rex config directory: $REX_HOME, or ~/.rex.
func DefaultDir() string {
	if dir := os.Getenv("REX_HOME"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".rex")
}

// SocketPath returns the path to the agent-facing Unix domain socket.
func SocketPath() string {
	return filepath.Join(DefaultDir(), "rex.sock")
}

// ConfigPath returns the path to the config file.
func ConfigPath() string {
	return filepath.Join(DefaultDir(), "config.yaml")
}

// HistoryPath returns the path to the persisted request history.
func HistoryPath() string {
	return filepath.Join(DefaultDir(), "history.jsonl")
}

// Load reads and parses the config file.
func Load() (*Config, error) {
	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		return nil, fmt.Errorf("failed to read config: %w (create it at %s)", err, ConfigPath())
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}
	cfg.applyDefaults()
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Timeout <= 0 {
		c.Timeout = 60
	}
	if c.Servers == nil {
		c.Servers = make(map[string]ServerConfig)
	}
}

func orDefault(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

// AutoMaxMinutes is the longest allowed auto-approve session.
func (c *Config) AutoMaxMinutes() int { return orDefault(c.AutoApprove.MaxMinutes, 240) }

// AutoMaxPerMinute is the auto-approve circuit breaker rate.
func (c *Config) AutoMaxPerMinute() int { return orDefault(c.AutoApprove.MaxPerMinute, 30) }

// AutoMaxFailures is the auto-approve circuit breaker failure streak.
func (c *Config) AutoMaxFailures() int { return orDefault(c.AutoApprove.MaxConsecutiveFailures, 5) }

// ForwardDefaultTTL is the default forward lifetime in minutes.
func (c *Config) ForwardDefaultTTL() int { return orDefault(c.Forward.DefaultTTLMinutes, 30) }

// ForwardMaxTTL is the longest allowed forward lifetime in minutes.
func (c *Config) ForwardMaxTTL() int { return orDefault(c.Forward.MaxTTLMinutes, 240) }

// Save writes the config back to disk atomically.
func Save(cfg *Config) error {
	dir := DefaultDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	tmp := ConfigPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, ConfigPath())
}

// Clone returns a deep copy of the config.
func (c *Config) Clone() *Config {
	n := *c
	n.Servers = make(map[string]ServerConfig, len(c.Servers))
	for k, v := range c.Servers {
		n.Servers[k] = v
	}
	n.Whitelist = make([]WhitelistRule, len(c.Whitelist))
	for i, r := range c.Whitelist {
		r.Servers = append([]string(nil), r.Servers...)
		n.Whitelist[i] = r
	}
	n.AutoApprove.Blocklist = append([]string(nil), c.AutoApprove.Blocklist...)
	return &n
}

// ServerNames returns the configured server aliases, sorted.
func (c *Config) ServerNames() []string {
	names := make([]string, 0, len(c.Servers))
	for k := range c.Servers {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// ResolveServer looks up a server alias and returns the config.
func (c *Config) ResolveServer(alias string) (*ServerConfig, error) {
	srv, ok := c.Servers[alias]
	if !ok {
		return nil, fmt.Errorf("unknown server %q, available: %s", alias, strings.Join(c.ServerNames(), ", "))
	}
	if srv.Port == 0 {
		srv.Port = 22
	}
	return &srv, nil
}
