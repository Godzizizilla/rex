package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestJump(t *testing.T) {
	t.Setenv("REX_HOME", t.TempDir())
	yml := `servers:
  bastion: {host: root@1.2.3.4, port: 2200}
  db: {host: root@10.0.0.9, jump: "@bastion"}
  db2: {host: root@10.0.0.10, jump: ["@db", "ops@x.example.com"]}
  direct: {host: root@5.6.7.8, jump: ops@j:22}
`
	os.WriteFile(ConfigPath(), []byte(yml), 0600)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	db, _ := cfg.ResolveServer("db")
	if len(db.JumpHosts) != 1 || db.JumpHosts[0] != "root@1.2.3.4:2200" {
		t.Errorf("db hops = %v", db.JumpHosts)
	}
	db2, _ := cfg.ResolveServer("db2")
	if want := "root@1.2.3.4:2200,root@10.0.0.9,ops@x.example.com"; strings.Join(db2.JumpHosts, ",") != want {
		t.Errorf("db2 hops = %v", db2.JumpHosts)
	}
	for name, bad := range map[string]string{
		"unknown": "servers:\n  a: {host: h, jump: \"@nope\"}\n",
		"loop":    "servers:\n  a: {host: h, jump: \"@b\"}\n  b: {host: h, jump: \"@a\"}\n",
		"option":  "servers:\n  a: {host: h, jump: \"-oProxyCommand=x\"}\n",
	} {
		os.WriteFile(ConfigPath(), []byte(bad), 0600)
		if _, err := Load(); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
