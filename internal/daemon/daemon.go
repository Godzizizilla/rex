// Package daemon implements rexd: it accepts requests from agents on a Unix
// socket, evaluates them against the policy, waits for a human decision
// (delivered over the control channel) and executes approved requests over ssh.
package daemon

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"rex/internal/config"
	"rex/internal/policy"
	"rex/internal/protocol"
)

const (
	maxEntries     = 500      // requests kept in memory
	tailSize       = 64 << 10 // output tail kept per request
	emitInterval   = 500 * time.Millisecond
	stdinPreviewSz = 4 << 10
)

// Daemon manages the request queue, approvals and execution.
type Daemon struct {
	mu       sync.Mutex
	cfg      *config.Config
	entries  []*entry // oldest first
	nextID   int
	auto     autoMode
	forwards map[int]*forward
	nextFwd  int

	listener net.Listener
	events   chan protocol.ControlEvent
	hist     *history
	ctx      context.Context
	stop     context.CancelFunc
	wg       sync.WaitGroup
}

type entry struct {
	req      protocol.Request // guarded by Daemon.mu
	stdin    []byte
	data     []byte
	decided  chan struct{} // closed once the request leaves pending
	tail     tailBuffer
	lastEmit time.Time
}

// New creates a daemon for the given config.
func New(cfg *config.Config) *Daemon {
	ctx, stop := context.WithCancel(context.Background())
	d := &Daemon{
		cfg:      cfg,
		forwards: make(map[int]*forward),
		events:   make(chan protocol.ControlEvent, 1024),
		hist:     &history{path: config.HistoryPath()},
		ctx:      ctx,
		stop:     stop,
	}
	past, err := d.hist.load(maxEntries)
	if err != nil {
		log.Printf("history: %v", err)
	}
	for i := range past {
		r := past[i]
		if !r.Done() { // rexd died while it was in flight
			r.Status = protocol.StatusCancelled
			r.Error = "rexd restarted"
		}
		d.entries = append(d.entries, &entry{req: r, decided: closedChan()})
		if r.ID > d.nextID {
			d.nextID = r.ID
		}
	}
	return d
}

func closedChan() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

// Start listens on the agent socket.
func (d *Daemon) Start() error {
	sockPath := config.SocketPath()
	if _, err := os.Stat(sockPath); err == nil {
		if conn, err := net.DialTimeout("unix", sockPath, time.Second); err == nil {
			conn.Close()
			return fmt.Errorf("another rexd is already running (socket: %s)", sockPath)
		}
		os.Remove(sockPath)
	}
	if err := os.MkdirAll(config.DefaultDir(), 0700); err != nil {
		return err
	}
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", sockPath, err)
	}
	os.Chmod(sockPath, 0600)
	d.listener = ln
	go d.acceptLoop()
	return nil
}

// Stop cancels everything in flight and removes the socket.
func (d *Daemon) Stop() {
	d.stop()
	if d.listener != nil {
		d.listener.Close()
	}
	d.stopAllForwards()
	done := make(chan struct{})
	go func() { d.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
	os.Remove(config.SocketPath())
}

// Events returns the control events to forward to the app.
func (d *Daemon) Events() <-chan protocol.ControlEvent { return d.events }

func (d *Daemon) emit(ev protocol.ControlEvent) {
	select {
	case d.events <- ev:
	case <-d.ctx.Done():
	}
}

func (d *Daemon) emitRequest(r protocol.Request) {
	d.emit(protocol.ControlEvent{Type: protocol.EvtRequest, Request: &r})
}

func (d *Daemon) emitState() {
	d.mu.Lock()
	st := d.stateLocked()
	d.mu.Unlock()
	d.emit(protocol.ControlEvent{Type: protocol.EvtState, State: st})
}

func (d *Daemon) emitConfig() {
	d.mu.Lock()
	cv := d.configViewLocked()
	d.mu.Unlock()
	d.emit(protocol.ControlEvent{Type: protocol.EvtConfig, Config: cv})
}

func (d *Daemon) notice(msg string) {
	log.Print(msg)
	d.emit(protocol.ControlEvent{Type: protocol.EvtNotice, Message: msg})
}

// hello is the first event sent to a control client.
func (d *Daemon) hello() protocol.ControlEvent {
	d.mu.Lock()
	defer d.mu.Unlock()
	reqs := make([]*protocol.Request, 0, len(d.entries))
	for _, e := range d.entries {
		r := e.req
		reqs = append(reqs, &r)
	}
	return protocol.ControlEvent{
		Type:     protocol.EvtHello,
		Version:  protocol.Version,
		Config:   d.configViewLocked(),
		State:    d.stateLocked(),
		Requests: reqs,
	}
}

func (d *Daemon) configViewLocked() *protocol.ConfigView {
	cv := &protocol.ConfigView{
		Path:           config.ConfigPath(),
		Timeout:        d.cfg.Timeout,
		Whitelist:      append([]config.WhitelistRule{}, d.cfg.Whitelist...),
		Blocklist:      policy.Blocklist(d.cfg),
		AutoMaxMinutes: d.cfg.AutoMaxMinutes(),
	}
	for _, name := range d.cfg.ServerNames() {
		srv, _ := d.cfg.ResolveServer(name)
		cv.Servers = append(cv.Servers, protocol.ServerInfo{
			Name: name, Host: srv.Host, Port: srv.Port, Description: srv.Description,
		})
	}
	return cv
}

func (d *Daemon) stateLocked() *protocol.State {
	st := &protocol.State{Auto: d.auto.view(), Forwards: []protocol.Forward{}}
	for _, f := range d.forwards {
		st.Forwards = append(st.Forwards, f.info)
	}
	return st
}

// --- request lifecycle ---

func (d *Daemon) addEntry(req protocol.Request, stdin, data []byte) *entry {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.nextID++
	req.ID = d.nextID
	req.Status = protocol.StatusPending
	req.CreatedAt = nowMs()
	e := &entry{req: req, stdin: stdin, data: data, decided: make(chan struct{})}
	d.entries = append(d.entries, e)
	if len(d.entries) > maxEntries {
		kept := d.entries[:0]
		drop := len(d.entries) - maxEntries
		for _, old := range d.entries {
			if drop > 0 && old.req.Done() {
				drop--
				continue
			}
			kept = append(kept, old)
		}
		d.entries = kept
	}
	return e
}

func (d *Daemon) find(id int) *entry {
	for _, e := range d.entries {
		if e.req.ID == id {
			return e
		}
	}
	return nil
}

func (d *Daemon) snapshot(e *entry) protocol.Request {
	d.mu.Lock()
	defer d.mu.Unlock()
	return e.req
}

// decide moves a pending request to approved or a final state. It returns
// false if the request was already decided (the first decision wins).
func (d *Daemon) decide(e *entry, status, approvedBy, feedback string) bool {
	d.mu.Lock()
	if e.req.Status != protocol.StatusPending {
		d.mu.Unlock()
		return false
	}
	e.req.Status = status
	e.req.ApprovedBy = approvedBy
	e.req.Feedback = feedback
	e.req.DecidedAt = nowMs()
	e.req.Deadline = 0
	if e.req.Done() {
		e.req.FinishedAt = e.req.DecidedAt
	}
	close(e.decided)
	snap := e.req
	d.mu.Unlock()

	d.emitRequest(snap)
	if snap.Done() {
		d.hist.append(snap)
	}
	return true
}

// setRunning marks an approved request as running.
func (d *Daemon) setRunning(e *entry) {
	d.mu.Lock()
	e.req.Status = protocol.StatusRunning
	snap := e.req
	d.mu.Unlock()
	d.emitRequest(snap)
}

// finish records the final state of an approved request.
func (d *Daemon) finish(e *entry, status string, exitCode int, errMsg string) {
	d.mu.Lock()
	e.req.Status = status
	e.req.ExitCode = exitCode
	e.req.Error = errMsg
	e.req.FinishedAt = nowMs()
	e.req.Output = e.tail.String()
	e.req.OutputSize = e.tail.total
	snap := e.req
	d.mu.Unlock()

	d.emitRequest(snap)
	d.hist.append(snap)
	d.autoObserve(snap)
}

// appendOutput records output and periodically publishes it to the app.
func (d *Daemon) appendOutput(e *entry, p []byte) {
	d.mu.Lock()
	e.tail.Write(p)
	if time.Since(e.lastEmit) < emitInterval {
		d.mu.Unlock()
		return
	}
	e.lastEmit = time.Now()
	e.req.Output = e.tail.String()
	e.req.OutputSize = e.tail.total
	snap := e.req
	d.mu.Unlock()
	d.emitRequest(snap)
}

// Approve approves a pending request.
func (d *Daemon) Approve(id int) error {
	d.mu.Lock()
	e := d.find(id)
	d.mu.Unlock()
	if e == nil {
		return fmt.Errorf("request #%d not found", id)
	}
	if !d.decide(e, protocol.StatusApproved, protocol.ApprovedManual, "") {
		return fmt.Errorf("request #%d is no longer pending", id)
	}
	return nil
}

// Reject rejects a pending request with feedback for the agent.
func (d *Daemon) Reject(id int, feedback string) error {
	d.mu.Lock()
	e := d.find(id)
	d.mu.Unlock()
	if e == nil {
		return fmt.Errorf("request #%d not found", id)
	}
	if !d.decide(e, protocol.StatusRejected, "", feedback) {
		return fmt.Errorf("request #%d is no longer pending", id)
	}
	return nil
}

func nowMs() int64 { return time.Now().UnixMilli() }

// tailBuffer keeps the last tailSize bytes written to it.
type tailBuffer struct {
	buf   []byte
	total int64
}

func (t *tailBuffer) Write(p []byte) {
	t.total += int64(len(p))
	t.buf = append(t.buf, p...)
	if len(t.buf) > tailSize {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-tailSize:]...)
	}
}

func (t *tailBuffer) String() string { return string(t.buf) }
