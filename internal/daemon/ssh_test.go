package daemon

import (
	"strings"
	"testing"

	"rex/internal/config"
)

func TestSSHArgsJump(t *testing.T) {
	srv := &config.ServerConfig{Host: "root@10.0.0.9", JumpHosts: []string{"root@1.2.3.4:2200", "ops@j"}}
	got := strings.Join(sshArgs(srv, false), " ")
	if !strings.Contains(got, "-J root@1.2.3.4:2200,ops@j") {
		t.Errorf("missing -J: %s", got)
	}
	if strings.Contains(strings.Join(sshArgs(&config.ServerConfig{Host: "h"}, false), " "), "-J") {
		t.Error("unexpected -J without jump hosts")
	}
}
