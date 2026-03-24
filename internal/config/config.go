package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type ServerConfig struct {
	Host        string `yaml:"host"`        // user@hostname
	Port        int    `yaml:"port"`        // SSH port (default 22)
	Description string `yaml:"description"` // human-readable description
}

type WhitelistRule struct {
	Pattern string   `yaml:"pattern"` // glob pattern for command
	Servers []string `yaml:"servers"` // server aliases (["*"] for all)
	Note    string   `yaml:"note"`    // human-readable note
}

type Config struct {
	Servers   map[string]ServerConfig `yaml:"servers"`
	Timeout   int                     `yaml:"timeout"` // default approval timeout in seconds
	Whitelist []WhitelistRule         `yaml:"whitelist"`
}

// DefaultDir returns the rex config directory (~/.rex).
func DefaultDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".rex")
}

// SocketPath returns the path to the Unix domain socket.
func SocketPath() string {
	return filepath.Join(DefaultDir(), "rex.sock")
}

// ConfigPath returns the path to the config file.
func ConfigPath() string {
	return filepath.Join(DefaultDir(), "config.yaml")
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
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60
	}
	if cfg.Servers == nil {
		cfg.Servers = make(map[string]ServerConfig)
	}
	return &cfg, nil
}

// Save writes the config back to disk.
func Save(cfg *Config) error {
	dir := DefaultDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(ConfigPath(), data, 0600)
}

// ResolveServer looks up a server alias and returns the config.
func (c *Config) ResolveServer(alias string) (*ServerConfig, error) {
	srv, ok := c.Servers[alias]
	if !ok {
		available := make([]string, 0, len(c.Servers))
		for k := range c.Servers {
			available = append(available, k)
		}
		return nil, fmt.Errorf("unknown server %q, available: %s", alias, strings.Join(available, ", "))
	}
	if srv.Port == 0 {
		srv.Port = 22
	}
	return &srv, nil
}

// MatchWhitelist checks if a command matches any whitelist rule for the given server.
func (c *Config) MatchWhitelist(server, command string) (bool, string) {
	for _, rule := range c.Whitelist {
		if !matchServers(rule.Servers, server) {
			continue
		}
		if globMatch(rule.Pattern, command) {
			return true, rule.Note
		}
	}
	return false, ""
}

func matchServers(patterns []string, server string) bool {
	for _, p := range patterns {
		if p == "*" || p == server {
			return true
		}
	}
	return false
}

// globMatch matches a pattern against a string where * matches any characters.
func globMatch(pattern, str string) bool {
	// Simple glob: split by * and check sequential containment
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == str
	}

	// First part must be prefix
	if !strings.HasPrefix(str, parts[0]) {
		return false
	}
	str = str[len(parts[0]):]

	// Middle parts must appear in order
	for i := 1; i < len(parts)-1; i++ {
		idx := strings.Index(str, parts[i])
		if idx < 0 {
			return false
		}
		str = str[idx+len(parts[i]):]
	}

	// Last part must be suffix
	return strings.HasSuffix(str, parts[len(parts)-1])
}
