package daemon

import (
	"strings"
	"testing"
	"time"

	"rex/internal/config"
	"rex/internal/protocol"
)

func (h *harness) startAuto(minutes int, servers ...string) {
	h.t.Helper()
	h.control(protocol.ControlCmd{Type: protocol.CtlAutoStart, Minutes: minutes, Servers: servers, Seq: 100})
	if ev := h.waitEvent(protocol.EvtResult, func(ev protocol.ControlEvent) bool { return ev.Seq == 100 }); !ev.OK {
		h.t.Fatalf("auto_start failed: %s", ev.Error)
	}
}

func TestAutoApprovesSafeCommands(t *testing.T) {
	h := newHarness(t, nil)
	h.startAuto(30)
	res := h.submit(execMsg("echo safe")).wait()
	if res.final.Type != protocol.RespCompleted || res.stdout != "safe\n" {
		t.Fatalf("got %+v stdout=%q", res.final, res.stdout)
	}
	if r := h.waitStatus(protocol.StatusCompleted); r.ApprovedBy != protocol.ApprovedAuto {
		t.Errorf("approved_by = %q", r.ApprovedBy)
	}
	st := h.waitEvent(protocol.EvtState, func(ev protocol.ControlEvent) bool { return ev.State.Auto.Approved == 1 })
	if !st.State.Auto.Active || st.State.Auto.Until == 0 {
		t.Errorf("state = %+v", st.State.Auto)
	}
}

func TestAutoHoldsRiskyCommands(t *testing.T) {
	h := newHarness(t, nil)
	h.startAuto(30)
	c := h.submit(execMsg("ls && sudo systemctl restart nginx"))
	if r := c.next(); r.Type != protocol.RespQueued {
		t.Fatalf("risky command should wait for a human, got %s", r.Type)
	}
	p := h.waitStatus(protocol.StatusPending)
	if len(p.HeldBy) == 0 || !strings.Contains(p.HeldBy[0], "systemctl restart") {
		t.Errorf("held_by = %v", p.HeldBy)
	}
	h.waitEvent(protocol.EvtState, func(ev protocol.ControlEvent) bool { return ev.State.Auto.Held == 1 })
}

func TestAutoOnlyCoversSelectedServers(t *testing.T) {
	h := newHarness(t, &config.Config{Servers: map[string]config.ServerConfig{
		"prod": {Host: "p"}, "staging": {Host: "s"},
	}})
	h.startAuto(30, "staging")
	c := h.submit(execMsg("echo hi")) // prod
	if r := c.next(); r.Type != protocol.RespQueued {
		t.Fatalf("prod is not covered, got %s", r.Type)
	}
	msg := execMsg("echo hi")
	msg.Server = "staging"
	if res := h.submit(msg).wait(); res.final.Type != protocol.RespCompleted {
		t.Fatalf("staging is covered, got %+v", res.final)
	}
}

func TestAutoStopAndLimits(t *testing.T) {
	h := newHarness(t, nil)
	h.control(protocol.ControlCmd{Type: protocol.CtlAutoStart, Minutes: 100000, Seq: 1})
	if ev := h.waitEvent(protocol.EvtResult, func(ev protocol.ControlEvent) bool { return ev.Seq == 1 }); ev.OK {
		t.Error("duration above max_minutes must be refused")
	}
	h.startAuto(30)
	h.control(protocol.ControlCmd{Type: protocol.CtlAutoStop})
	n := h.waitEvent(protocol.EvtNotice, nil)
	if !strings.Contains(n.Message, "stopped by user") {
		t.Errorf("notice = %q", n.Message)
	}
	if r := h.submit(execMsg("echo hi")).next(); r.Type != protocol.RespQueued {
		t.Errorf("after stop requests need review again, got %s", r.Type)
	}
}

func TestAutoCircuitBreakers(t *testing.T) {
	h := newHarness(t, &config.Config{AutoApprove: config.AutoApproveConfig{MaxConsecutiveFailures: 2, MaxPerMinute: 100}})
	h.startAuto(30)
	h.submit(execMsg("false")).wait()
	h.submit(execMsg("false")).wait()
	n := h.waitEvent(protocol.EvtNotice, nil)
	if !strings.Contains(n.Message, "failed in a row") {
		t.Errorf("notice = %q", n.Message)
	}

	h2 := newHarness(t, &config.Config{AutoApprove: config.AutoApproveConfig{MaxPerMinute: 2}})
	h2.startAuto(30)
	h2.submit(execMsg("true")).wait()
	h2.submit(execMsg("true")).wait()
	if r := h2.submit(execMsg("true")).next(); r.Type != protocol.RespQueued {
		t.Errorf("third request in a minute should trip the breaker, got %s", r.Type)
	}
	if n := h2.waitEvent(protocol.EvtNotice, nil); !strings.Contains(n.Message, "per minute") {
		t.Errorf("notice = %q", n.Message)
	}
}

func TestAutoExpires(t *testing.T) {
	h := newHarness(t, nil)
	h.startAuto(1)
	h.d.mu.Lock()
	started := h.d.auto.startedAt
	h.d.auto.until = time.Now().Add(-time.Second) // pretend the minute is over
	h.d.mu.Unlock()
	if r := h.submit(execMsg("echo hi")).next(); r.Type != protocol.RespQueued {
		t.Errorf("expired auto mode must not approve, got %s", r.Type)
	}
	h.d.mu.Lock()
	same := h.d.auto.startedAt.Equal(started)
	h.d.mu.Unlock()
	if !same {
		t.Error("unexpected restart of auto mode")
	}
}
