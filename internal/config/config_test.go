package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestExampleConfigLoads(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "../../docs/config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv("REX_HOME", dir)
	os.WriteFile(ConfigPath(), data, 0600)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers) != 2 || len(cfg.Whitelist) != 8 || cfg.AutoMaxMinutes() != 240 || cfg.ForwardDefaultTTL() != 30 {
		t.Errorf("unexpected config: %+v", cfg)
	}
	if srv, _ := cfg.ResolveServer("prod"); srv.Port != 22 {
		t.Errorf("default port = %d", srv.Port)
	}
	if cfg.Whitelist[5].Kind() != RuleDownload || cfg.Whitelist[0].Kind() != RuleExec {
		t.Errorf("rule kinds wrong")
	}
	// Saving keeps defaults out of the file.
	cfg2 := &Config{Servers: map[string]ServerConfig{"a": {Host: "h"}}, Timeout: 60}
	if err := Save(cfg2); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(ConfigPath())
	if want := "servers:\n    a:\n        host: h\ntimeout: 60\nwhitelist: []\n"; string(out) != want {
		t.Errorf("saved config:\n%s\nwant:\n%s", out, want)
	}
}
