package daemon

import (
	"bufio"
	"encoding/json"
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

// fakeSSH runs the remote command locally with sh, and emulates
// `ssh -N -L 127.0.0.1:lport:host:rport` by proxying with nc-free Go-less shell:
// forwards are handled by fakeForward in forward tests.
const fakeSSH = `#!/bin/sh
# Minimal ssh stand-in for tests: skip options, then "--", host, command.
forward=""
while [ $# -gt 0 ]; do
  case "$1" in
    --) shift; break ;;
    -L) forward="$2"; shift 2 ;;
    -N) shift ;;
    -o|-p) shift 2 ;;
    *) break ;;
  esac
done
host="$1"; shift
if [ -n "$forward" ]; then
  exec "$REX_TEST_FORWARDER" "$forward"
fi
exec sh -c "$*"
`

type harness struct {
	t      *testing.T
	d      *Daemon
	events chan protocol.ControlEvent
	ctlW   io.WriteCloser
	done   chan error
	seen   []protocol.ControlEvent // received but not yet matched
}

func newHarness(t *testing.T, cfg *config.Config) *harness {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "rex-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("REX_HOME", dir)

	bin := filepath.Join(dir, "bin")
	os.Mkdir(bin, 0700)
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(fakeSSH), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	if cfg == nil {
		cfg = &config.Config{}
	}
	if cfg.Servers == nil {
		cfg.Servers = map[string]config.ServerConfig{"prod": {Host: "deploy@prod"}}
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 5
	}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	return startHarness(t)
}

func startHarness(t *testing.T) *harness {
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	d := New(cfg)
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	ctlR, ctlW := io.Pipe()
	evR, evW := io.Pipe()
	h := &harness{t: t, d: d, events: make(chan protocol.ControlEvent, 1024), ctlW: ctlW, done: make(chan error, 1)}
	go func() { h.done <- d.ServeControl(ctlR, evW); evW.Close() }()
	go func() {
		dec := json.NewDecoder(evR)
		for {
			var ev protocol.ControlEvent
			if dec.Decode(&ev) != nil {
				close(h.events)
				return
			}
			h.events <- ev
		}
	}()
	h.waitEvent(protocol.EvtHello, nil)
	t.Cleanup(h.stop)
	return h
}

func (h *harness) stop() {
	h.ctlW.Close()
	h.d.Stop()
}

func (h *harness) control(cmd protocol.ControlCmd) {
	b, _ := json.Marshal(cmd)
	h.ctlW.Write(append(b, '\n'))
}

// waitEvent waits for an event of the given type matching pred.
func (h *harness) waitEvent(typ string, pred func(protocol.ControlEvent) bool) protocol.ControlEvent {
	h.t.Helper()
	match := func(ev protocol.ControlEvent) bool { return ev.Type == typ && (pred == nil || pred(ev)) }
	for i, ev := range h.seen {
		if match(ev) {
			h.seen = append(h.seen[:i], h.seen[i+1:]...)
			return ev
		}
	}
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-h.events:
			if !ok {
				h.t.Fatalf("control channel closed while waiting for %s", typ)
			}
			if match(ev) {
				return ev
			}
			h.seen = append(h.seen, ev)
		case <-timeout:
			h.t.Fatalf("timed out waiting for %s event", typ)
		}
	}
}

func (h *harness) waitStatus(status string) *protocol.Request {
	h.t.Helper()
	return h.waitEvent(protocol.EvtRequest, func(ev protocol.ControlEvent) bool {
		return ev.Request.Status == status
	}).Request
}

type client struct {
	t    *testing.T
	conn net.Conn
	dec  *json.Decoder
}

func (h *harness) submit(msg protocol.AgentMsg) *client {
	h.t.Helper()
	conn, err := net.Dial("unix", config.SocketPath())
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { conn.Close() })
	b, _ := json.Marshal(msg)
	conn.Write(append(b, '\n'))
	return &client{t: h.t, conn: conn, dec: json.NewDecoder(bufio.NewReader(conn))}
}

// result collects responses until a final one.
type result struct {
	stdout, stderr string
	final          protocol.AgentResp
	types          []string
}

func (c *client) wait() result {
	c.t.Helper()
	var res result
	c.conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	for {
		var r protocol.AgentResp
		if err := c.dec.Decode(&r); err != nil {
			c.t.Fatalf("reading response: %v (so far %v)", err, res.types)
		}
		res.types = append(res.types, r.Type)
		switch r.Type {
		case protocol.RespOutput:
			if r.Stream == "stderr" {
				res.stderr += string(r.Data)
			} else {
				res.stdout += string(r.Data)
			}
		case protocol.RespQueued, protocol.RespApproved:
		default:
			res.final = r
			return res
		}
	}
}

func (c *client) next() protocol.AgentResp {
	c.t.Helper()
	c.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var r protocol.AgentResp
	if err := c.dec.Decode(&r); err != nil {
		c.t.Fatalf("reading response: %v", err)
	}
	return r
}

func execMsg(cmd string) protocol.AgentMsg {
	return protocol.AgentMsg{Type: protocol.AgentExec, Server: "prod", Desc: "test", Command: cmd}
}

func TestWhitelistedRunsImmediatelyWithExactOutput(t *testing.T) {
	h := newHarness(t, &config.Config{Whitelist: []config.WhitelistRule{{Pattern: "printf *", Servers: []string{"*"}}}})
	res := h.submit(execMsg(`printf 'a\nno-newline'`)).wait()
	if res.final.Type != protocol.RespCompleted || res.final.ExitCode != 0 {
		t.Fatalf("final = %+v", res.final)
	}
	if res.stdout != "a\nno-newline" {
		t.Errorf("stdout = %q, want exact bytes without an added newline", res.stdout)
	}
	if strings.Contains(strings.Join(res.types, ","), protocol.RespQueued) {
		t.Errorf("whitelisted request should not be queued: %v", res.types)
	}
	r := h.waitStatus(protocol.StatusCompleted)
	if r.ApprovedBy != protocol.ApprovedWhitelist {
		t.Errorf("approved_by = %q", r.ApprovedBy)
	}
}

func TestApproveRunsAndPassesExitCode(t *testing.T) {
	h := newHarness(t, nil)
	c := h.submit(execMsg("echo out; echo err >&2; exit 3"))
	if r := c.next(); r.Type != protocol.RespQueued {
		t.Fatalf("got %s, want queued", r.Type)
	}
	pending := h.waitStatus(protocol.StatusPending)
	if pending.Deadline == 0 || pending.Eval == nil || len(pending.Eval.Commands) != 3 {
		t.Fatalf("pending request incomplete: %+v", pending)
	}
	h.control(protocol.ControlCmd{Type: protocol.CtlApprove, ID: pending.ID, Seq: 7})
	res := c.wait()
	if res.final.Type != protocol.RespCompleted || res.final.ExitCode != 3 {
		t.Fatalf("final = %+v", res.final)
	}
	if res.stdout != "out\n" || res.stderr != "err\n" {
		t.Errorf("stdout=%q stderr=%q", res.stdout, res.stderr)
	}
	done := h.waitStatus(protocol.StatusCompleted)
	if done.ExitCode != 3 || !strings.Contains(done.Output, "out") {
		t.Errorf("completed request = %+v", done)
	}
}

func TestRejectSendsFeedback(t *testing.T) {
	h := newHarness(t, nil)
	c := h.submit(execMsg("rm -rf /tmp/nothing"))
	c.next()
	p := h.waitStatus(protocol.StatusPending)
	h.control(protocol.ControlCmd{Type: protocol.CtlReject, ID: p.ID, Feedback: "use the staging server"})
	res := c.wait()
	if res.final.Type != protocol.RespRejected || res.final.Message != "use the staging server" {
		t.Fatalf("final = %+v", res.final)
	}
	h.control(protocol.ControlCmd{Type: protocol.CtlApprove, ID: p.ID, Seq: 9})
	ev := h.waitEvent(protocol.EvtResult, func(ev protocol.ControlEvent) bool { return ev.Seq == 9 })
	if ev.OK || ev.Error == "" {
		t.Errorf("approving a rejected request should fail: %+v", ev)
	}
}

func TestApprovalTimeout(t *testing.T) {
	h := newHarness(t, nil)
	msg := execMsg("uptime")
	msg.Timeout = 1
	res := h.submit(msg).wait()
	if res.final.Type != protocol.RespTimeout {
		t.Fatalf("final = %+v", res.final)
	}
}

func TestDisconnectWhilePendingCancels(t *testing.T) {
	h := newHarness(t, nil)
	c := h.submit(execMsg("touch /tmp/should-not-exist"))
	c.next()
	p := h.waitStatus(protocol.StatusPending)
	c.conn.Close()
	r := h.waitStatus(protocol.StatusCancelled)
	if r.ID != p.ID {
		t.Fatalf("cancelled #%d, want #%d", r.ID, p.ID)
	}
	h.control(protocol.ControlCmd{Type: protocol.CtlApprove, ID: p.ID, Seq: 3})
	if ev := h.waitEvent(protocol.EvtResult, func(ev protocol.ControlEvent) bool { return ev.Seq == 3 }); ev.OK {
		t.Error("a withdrawn request must not be approvable")
	}
}

func TestDisconnectWhileRunningKillsCommand(t *testing.T) {
	h := newHarness(t, &config.Config{Whitelist: []config.WhitelistRule{{Pattern: "sleep *", Servers: []string{"*"}}}})
	c := h.submit(execMsg("sleep 30"))
	h.waitStatus(protocol.StatusRunning)
	start := time.Now()
	c.conn.Close()
	h.waitStatus(protocol.StatusCancelled)
	if time.Since(start) > 5*time.Second {
		t.Errorf("command was not killed promptly")
	}
}

func TestStdinIsForwarded(t *testing.T) {
	h := newHarness(t, &config.Config{Whitelist: []config.WhitelistRule{{Pattern: "wc *", Servers: []string{"*"}}}})
	msg := execMsg("wc -l")
	msg.Stdin = []byte("1\n2\n3\n")
	res := h.submit(msg).wait()
	if strings.TrimSpace(res.stdout) != "3" {
		t.Errorf("stdout = %q", res.stdout)
	}
}

func TestShellFromStdinNeedsReviewAndShowsScript(t *testing.T) {
	h := newHarness(t, &config.Config{Whitelist: []config.WhitelistRule{{Pattern: "bash *", Servers: []string{"*"}}}})
	msg := execMsg("bash -s")
	msg.Stdin = []byte("rm -rf /opt/app\n")
	c := h.submit(msg)
	if r := c.next(); r.Type != protocol.RespQueued {
		t.Fatalf("bash -s with a script on stdin must be reviewed, got %s", r.Type)
	}
	p := h.waitStatus(protocol.StatusPending)
	if p.StdinPreview != "rm -rf /opt/app\n" || p.StdinSize != 16 {
		t.Errorf("stdin preview = %q size=%d", p.StdinPreview, p.StdinSize)
	}
}

func TestApproveWhitelistAddsRules(t *testing.T) {
	h := newHarness(t, nil)
	c := h.submit(execMsg("df -h | sort"))
	c.next()
	p := h.waitStatus(protocol.StatusPending)
	h.control(protocol.ControlCmd{Type: protocol.CtlApproveWhitelist, ID: p.ID, Indices: []int{0, 1}})
	cfgEv := h.waitEvent(protocol.EvtConfig, nil)
	if len(cfgEv.Config.Whitelist) != 2 || cfgEv.Config.Whitelist[0].Pattern != "df *" {
		t.Fatalf("whitelist = %+v", cfgEv.Config.Whitelist)
	}
	c.wait()
	saved, err := config.Load()
	if err != nil || len(saved.Whitelist) != 2 {
		t.Fatalf("saved whitelist = %+v, %v", saved, err)
	}
	// The same command now runs without review.
	res := h.submit(execMsg("df -h | sort")).wait()
	if res.types[0] == protocol.RespQueued {
		t.Errorf("command should be whitelisted now: %v", res.types)
	}
}

func TestUnknownServerAndMissingDesc(t *testing.T) {
	h := newHarness(t, nil)
	r := h.submit(protocol.AgentMsg{Type: protocol.AgentExec, Server: "nope", Desc: "x", Command: "ls"}).next()
	if r.Type != protocol.RespError || !strings.Contains(r.Message, "unknown server") {
		t.Errorf("got %+v", r)
	}
	r = h.submit(protocol.AgentMsg{Type: protocol.AgentExec, Server: "prod", Command: "ls"}).next()
	if r.Type != protocol.RespError || !strings.Contains(r.Message, "description") {
		t.Errorf("got %+v", r)
	}
}

func TestHistoryPersistsAcrossRestart(t *testing.T) {
	h := newHarness(t, &config.Config{Whitelist: []config.WhitelistRule{{Pattern: "echo *", Servers: []string{"*"}}}})
	h.submit(execMsg("echo one")).wait()
	h.waitStatus(protocol.StatusCompleted)
	pending := h.submit(execMsg("uptime"))
	pending.next()
	h.stop()
	<-h.done

	h2 := startHarness(t)
	hello := h2.d.hello()
	if len(hello.Requests) != 2 {
		t.Fatalf("restored %d requests, want 2", len(hello.Requests))
	}
	for _, r := range hello.Requests {
		if !r.Done() {
			t.Errorf("request #%d restored as %s", r.ID, r.Status)
		}
	}
	res := h2.submit(execMsg("echo two")).wait()
	if res.final.Type != protocol.RespCompleted {
		t.Fatalf("final = %+v", res.final)
	}
	if r := h2.waitStatus(protocol.StatusCompleted); r.ID != 3 {
		t.Errorf("new request id = %d, want 3 (ids continue after restart)", r.ID)
	}
}
