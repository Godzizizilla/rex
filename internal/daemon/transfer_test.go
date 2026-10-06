package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rex/internal/config"
	"rex/internal/protocol"
)

func TestUploadShowsDiffAndWrites(t *testing.T) {
	h := newHarness(t, nil)
	dir := os.Getenv("REX_HOME")
	target := filepath.Join(dir, "app.conf")
	os.WriteFile(target, []byte("a=1\nb=2\nc=3\n"), 0600)

	c := h.submit(protocol.AgentMsg{Type: protocol.AgentUpload, Server: "prod", Desc: "t",
		LocalPath: "/local/app.conf", RemotePath: target, Data: []byte("a=1\nb=20\nc=3\n")})
	c.next()
	p := h.waitStatus(protocol.StatusPending)
	if !strings.Contains(p.Diff, "-b=2\n+b=20\n") || p.Size != 13 {
		t.Errorf("diff = %q size = %d", p.Diff, p.Size)
	}
	h.control(protocol.ControlCmd{Type: protocol.CtlApprove, ID: p.ID})
	if res := c.wait(); res.final.Type != protocol.RespCompleted || res.final.ExitCode != 0 {
		t.Fatalf("final = %+v", res.final)
	}
	if got, _ := os.ReadFile(target); string(got) != "a=1\nb=20\nc=3\n" {
		t.Errorf("remote file = %q", got)
	}

	// Uploading into a directory keeps the local file name.
	c = h.submit(protocol.AgentMsg{Type: protocol.AgentUpload, Server: "prod", Desc: "t",
		LocalPath: "/local/new.txt", RemotePath: dir + "/", Data: []byte("hi\n")})
	c.next()
	p = h.waitStatus(protocol.StatusPending)
	if !strings.HasPrefix(p.Diff, "--- /dev/null") {
		t.Errorf("new file diff = %q", p.Diff)
	}
	h.control(protocol.ControlCmd{Type: protocol.CtlApprove, ID: p.ID})
	c.wait()
	if got, _ := os.ReadFile(filepath.Join(dir, "new.txt")); string(got) != "hi\n" {
		t.Errorf("uploaded into dir = %q", got)
	}
}

func TestDownloadWhitelisted(t *testing.T) {
	h := newHarness(t, &config.Config{Whitelist: []config.WhitelistRule{
		{Type: "download", Pattern: "/tmp/rex-test*", Servers: []string{"*"}},
	}})
	src := filepath.Join(os.Getenv("REX_HOME"), "data.bin")
	payload := []byte{0, 1, 2, 255, '\n', 'x'}
	os.WriteFile(src, payload, 0600)
	res := h.submit(protocol.AgentMsg{Type: protocol.AgentDownload, Server: "prod", Desc: "t", RemotePath: src}).wait()
	if res.final.Type != protocol.RespCompleted || res.stdout != string(payload) {
		t.Fatalf("final = %+v stdout = %q", res.final, res.stdout)
	}
	// Not covered by the rule: needs review.
	if r := h.submit(protocol.AgentMsg{Type: protocol.AgentDownload, Server: "prod", Desc: "t", RemotePath: "/etc/shadow"}).next(); r.Type != protocol.RespQueued {
		t.Errorf("got %s, want queued", r.Type)
	}
}

const fakeForwarder = `#!/usr/bin/env python3
import socket, sys
port = int(sys.argv[1].split(":")[1])
s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("127.0.0.1", port)); s.listen()
while True:
    c, _ = s.accept(); c.sendall(b"fwd " + sys.argv[1].encode() + b"\n"); c.close()
`

func TestForwardLifecycle(t *testing.T) {
	h := newHarness(t, &config.Config{Whitelist: []config.WhitelistRule{
		{Type: "forward", Pattern: "localhost:5432", Servers: []string{"prod"}},
	}})
	fw := filepath.Join(os.Getenv("REX_HOME"), "forwarder")
	os.WriteFile(fw, []byte(fakeForwarder), 0700)
	t.Setenv("REX_TEST_FORWARDER", fw)

	res := h.submit(protocol.AgentMsg{Type: protocol.AgentForward, Server: "prod", Desc: "db", RemotePort: 5432}).wait()
	if res.final.Type != protocol.RespResult {
		t.Fatalf("final = %+v", res.final)
	}
	var f protocol.Forward
	b, _ := json.Marshal(res.final.Result)
	json.Unmarshal(b, &f)
	if f.LocalPort == 0 || f.RemoteHost != "localhost" || f.ExpiresAt <= f.CreatedAt {
		t.Fatalf("forward = %+v", f)
	}
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", f.LocalPort))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(conn)
	conn.Close()
	if want := fmt.Sprintf("fwd 127.0.0.1:%d:localhost:5432\n", f.LocalPort); string(got) != want {
		t.Errorf("forwarded data = %q, want %q", got, want)
	}

	list := h.submit(protocol.AgentMsg{Type: protocol.AgentForwardList}).next()
	if b, _ := json.Marshal(list.Result); !strings.Contains(string(b), `"local_port":`) {
		t.Errorf("forward list = %s", b)
	}

	if r := h.submit(protocol.AgentMsg{Type: protocol.AgentForwardClose, ForwardID: f.ID}).next(); r.Type != protocol.RespResult {
		t.Fatalf("close = %+v", r)
	}
	h.waitEvent(protocol.EvtState, func(ev protocol.ControlEvent) bool { return len(ev.State.Forwards) == 0 })
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", f.LocalPort)); err != nil {
			return
		} else {
			c.Close()
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Error("forward still listening after close")
}

func TestForwardNeedsReviewAndValidates(t *testing.T) {
	h := newHarness(t, nil)
	if r := h.submit(protocol.AgentMsg{Type: protocol.AgentForward, Server: "prod", Desc: "x", RemotePort: 8080}).next(); r.Type != protocol.RespQueued {
		t.Errorf("got %s, want queued", r.Type)
	}
	if r := h.submit(protocol.AgentMsg{Type: protocol.AgentForward, Server: "prod", Desc: "x", RemotePort: 70000}).next(); r.Type != protocol.RespError {
		t.Errorf("got %s, want error", r.Type)
	}
	if r := h.submit(protocol.AgentMsg{Type: protocol.AgentForward, Server: "prod", Desc: "x", RemotePort: 80, TTL: 100000}).next(); r.Type != protocol.RespError {
		t.Errorf("got %s, want error for a too long ttl", r.Type)
	}
}

func TestQueries(t *testing.T) {
	h := newHarness(t, &config.Config{
		Servers: map[string]config.ServerConfig{"prod": {Host: "p", Description: "Production"}, "dev": {Host: "d"}},
		Whitelist: []config.WhitelistRule{
			{Pattern: "tail *", Servers: []string{"*"}},
			{Pattern: "cat *", Servers: []string{"dev"}},
		},
	})
	r := h.submit(protocol.AgentMsg{Type: protocol.AgentServers}).next()
	b, _ := json.Marshal(r.Result)
	if !strings.Contains(string(b), `"name":"dev"`) || !strings.Contains(string(b), "Production") {
		t.Errorf("servers = %s", b)
	}
	r = h.submit(protocol.AgentMsg{Type: protocol.AgentPolicy, Server: "prod"}).next()
	var info protocol.PolicyInfo
	b, _ = json.Marshal(r.Result)
	json.Unmarshal(b, &info)
	if len(info.Whitelist) != 1 || info.Whitelist[0].Pattern != "tail *" || len(info.Blocklist) == 0 {
		t.Errorf("policy = %+v", info)
	}
	if r := h.submit(protocol.AgentMsg{Type: protocol.AgentPolicy, Server: "nope"}).next(); r.Type != protocol.RespError {
		t.Errorf("unknown server should fail, got %+v", r)
	}
}

func TestUnifiedDiff(t *testing.T) {
	old := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n"
	new := "1\n2\n3\n4\nfive\n6\n7\n8\n9\n10\n11\n12\nthirteen\n"
	got := unifiedDiff("a", "b", old, new)
	want := `--- a
+++ b
@@ -2,7 +2,7 @@
 2
 3
 4
-5
+five
 6
 7
 8
@@ -10,3 +10,4 @@
 10
 11
 12
+thirteen
`
	if got != want {
		t.Errorf("diff:\n%s\nwant:\n%s", got, want)
	}
	if d := unifiedDiff("a", "b", "", "x\n"); d != "--- a\n+++ b\n@@ -0,0 +1,1 @@\n+x\n" {
		t.Errorf("new file diff = %q", d)
	}
}
