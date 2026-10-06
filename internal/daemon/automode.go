package daemon

import (
	"fmt"
	"time"

	"rex/internal/protocol"
)

// autoMode is the temporary auto-approve mode: for a limited time every
// request the policy finds no risk in is approved without a human. Requests
// that match the blocklist, cannot be analyzed, or target other servers stay
// pending as usual (and time out if nobody approves them).
//
// The mode lives only in memory: restarting rexd turns it off.
type autoMode struct {
	active     bool
	startedAt  time.Time
	until      time.Time
	servers    []string
	approved   int
	held       int
	failures   int         // consecutive non-zero exits of auto-approved requests
	recent     []time.Time // auto approvals within the last minute
	stopReason string
	timer      *time.Timer
}

func (a *autoMode) view() protocol.AutoState {
	st := protocol.AutoState{
		Active:     a.active,
		Servers:    a.servers,
		Approved:   a.approved,
		Held:       a.held,
		StopReason: a.stopReason,
	}
	if !a.startedAt.IsZero() {
		st.StartedAt = a.startedAt.UnixMilli()
		st.Until = a.until.UnixMilli()
	}
	return st
}

func (a *autoMode) covers(server string) bool {
	if len(a.servers) == 0 {
		return true
	}
	for _, s := range a.servers {
		if s == server {
			return true
		}
	}
	return false
}

// autoStart enables auto-approve for the given number of minutes.
func (d *Daemon) autoStart(minutes int, servers []string) error {
	d.mu.Lock()
	max := d.cfg.AutoMaxMinutes()
	if minutes <= 0 || minutes > max {
		d.mu.Unlock()
		return fmt.Errorf("duration must be between 1 and %d minutes", max)
	}
	for _, s := range servers {
		if _, err := d.cfg.ResolveServer(s); err != nil {
			d.mu.Unlock()
			return err
		}
	}
	if d.auto.timer != nil {
		d.auto.timer.Stop()
	}
	now := time.Now()
	d.auto = autoMode{
		active:    true,
		startedAt: now,
		until:     now.Add(time.Duration(minutes) * time.Minute),
		servers:   append([]string(nil), servers...),
	}
	started := d.auto.startedAt
	d.auto.timer = time.AfterFunc(time.Duration(minutes)*time.Minute, func() {
		d.mu.Lock()
		same := d.auto.active && d.auto.startedAt.Equal(started)
		d.mu.Unlock()
		if same {
			d.autoStop("time is up")
		}
	})
	d.mu.Unlock()

	d.emitState()
	return nil
}

// autoStop turns auto-approve off and reports what happened meanwhile.
func (d *Daemon) autoStop(reason string) {
	d.mu.Lock()
	if !d.auto.active {
		d.mu.Unlock()
		return
	}
	d.deactivateLocked(reason)
	approved, held := d.auto.approved, d.auto.held
	d.mu.Unlock()

	d.emitState()
	d.notice(fmt.Sprintf("Auto-approve stopped (%s): %d approved, %d held for review", reason, approved, held))
}

func (d *Daemon) deactivateLocked(reason string) {
	d.auto.active = false
	d.auto.stopReason = reason
	if d.auto.timer != nil {
		d.auto.timer.Stop()
		d.auto.timer = nil
	}
}

// autoDecideLocked returns ApprovedAuto if auto mode approves the request,
// otherwise the reasons it was held (nil if auto mode does not apply).
// The caller must emit a state event afterwards, since counters change.
func (d *Daemon) autoDecideLocked(req *protocol.Request) (string, []string) {
	a := &d.auto
	if !a.active {
		return "", nil
	}
	now := time.Now()
	if now.After(a.until) {
		return "", nil // the expiry timer is about to fire
	}
	if !a.covers(req.Server) {
		return "", []string{"auto-approve is not enabled for server " + req.Server}
	}
	if req.Eval == nil {
		a.held++
		return "", []string{"no policy evaluation"}
	}
	if len(req.Eval.Risks) > 0 {
		a.held++
		return "", req.Eval.Risks
	}

	// Circuit breaker: a burst of requests suggests an agent in a loop.
	cutoff := now.Add(-time.Minute)
	kept := a.recent[:0]
	for _, t := range a.recent {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	a.recent = kept
	if limit := d.cfg.AutoMaxPerMinute(); len(a.recent) >= limit {
		a.held++
		reason := fmt.Sprintf("more than %d requests per minute", limit)
		d.deactivateLocked(reason)
		go d.notice(fmt.Sprintf("Auto-approve stopped (%s): %d approved, %d held for review", reason, a.approved, a.held))
		return "", []string{"auto-approve stopped: " + reason}
	}

	a.recent = append(a.recent, now)
	a.approved++
	return protocol.ApprovedAuto, nil
}

// autoObserve watches the results of auto-approved requests and stops auto
// mode after too many consecutive failures.
func (d *Daemon) autoObserve(r protocol.Request) {
	if r.ApprovedBy != protocol.ApprovedAuto {
		return
	}
	d.mu.Lock()
	if !d.auto.active {
		d.mu.Unlock()
		return
	}
	if r.Status == protocol.StatusCompleted && r.ExitCode == 0 {
		d.auto.failures = 0
		d.mu.Unlock()
		return
	}
	d.auto.failures++
	limit := d.cfg.AutoMaxFailures()
	trip := d.auto.failures >= limit
	d.mu.Unlock()
	if trip {
		d.autoStop(fmt.Sprintf("%d auto-approved commands failed in a row", limit))
	}
}
