package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"rex/internal/policy"
	"rex/internal/protocol"
)

// agentConn is one rex CLI connection.
type agentConn struct {
	nc     net.Conn
	dec    *json.Decoder
	wmu    sync.Mutex
	enc    *json.Encoder
	closed chan struct{} // closed when the client disconnects or cancels
}

func (c *agentConn) send(r protocol.AgentResp) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.nc.SetWriteDeadline(time.Now().Add(30 * time.Second))
	c.enc.Encode(r)
}

func (c *agentConn) fail(format string, args ...any) {
	c.send(protocol.AgentResp{Type: protocol.RespError, Message: fmt.Sprintf(format, args...)})
}

// watch reads further client messages; a cancel message or EOF closes c.closed.
func (c *agentConn) watch() {
	defer close(c.closed)
	for {
		var m protocol.AgentMsg
		if err := c.dec.Decode(&m); err != nil || m.Type == protocol.AgentCancel {
			return
		}
	}
}

func (d *Daemon) acceptLoop() {
	for {
		nc, err := d.listener.Accept()
		if err != nil {
			return
		}
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			defer nc.Close()
			d.handleConn(nc)
		}()
	}
}

const maxMessage = 80 << 20 // stdin / upload payloads are base64 encoded

func (d *Daemon) handleConn(nc net.Conn) {
	c := &agentConn{
		nc:     nc,
		dec:    json.NewDecoder(bufio.NewReaderSize(&limitedReader{r: nc, n: maxMessage}, 64<<10)),
		enc:    json.NewEncoder(nc),
		closed: make(chan struct{}),
	}
	var msg protocol.AgentMsg
	if err := c.dec.Decode(&msg); err != nil {
		c.fail("invalid message: %v", err)
		return
	}

	if d.handleQuery(c, &msg) {
		return
	}

	e, err := d.prepare(&msg)
	if err != nil {
		c.fail("%v", err)
		return
	}
	go c.watch()
	d.serve(c, e)
}

// prepare validates a submission and turns it into a pending entry.
func (d *Daemon) prepare(msg *protocol.AgentMsg) (*entry, error) {
	d.mu.Lock()
	cfg := d.cfg.Clone()
	d.mu.Unlock()

	srv, err := cfg.ResolveServer(msg.Server)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(msg.Desc) == "" {
		return nil, errors.New("a description (-d) is required")
	}
	timeout := msg.Timeout
	if timeout <= 0 {
		timeout = cfg.Timeout
	}
	req := protocol.Request{
		Server:  msg.Server,
		Host:    srv.Host,
		Desc:    msg.Desc,
		Timeout: timeout,
	}

	switch msg.Type {
	case protocol.AgentExec, protocol.AgentSubmit:
		if strings.TrimSpace(msg.Command) == "" {
			return nil, errors.New("empty command")
		}
		req.Kind = protocol.KindExec
		req.Command = msg.Command
		req.StdinSize = len(msg.Stdin)
		req.StdinPreview = preview(msg.Stdin)
		req.Eval = policy.EvaluateExec(cfg, msg.Server, msg.Command, msg.Stdin)
		return d.addEntry(req, msg.Stdin, nil), nil
	default:
		return d.prepareExtra(cfg, srv, req, msg)
	}
}

// preview returns the beginning of stdin for display, or a note for binary data.
func preview(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	p := b
	if len(p) > stdinPreviewSz {
		p = p[:stdinPreviewSz]
	}
	for _, c := range p {
		if c == 0 {
			return fmt.Sprintf("(%d bytes of binary data)", len(b))
		}
	}
	return strings.ToValidUTF8(string(p), "?")
}

// serve drives one request from submission to completion.
func (d *Daemon) serve(c *agentConn, e *entry) {
	if by, held := d.preDecision(e); by != "" {
		d.decide(e, protocol.StatusApproved, by, "")
	} else {
		d.mu.Lock()
		e.req.HeldBy = held
		e.req.Deadline = nowMs() + int64(e.req.Timeout)*1000
		snap := e.req
		d.mu.Unlock()
		d.emitRequest(snap)
		c.send(protocol.AgentResp{Type: protocol.RespQueued, ID: snap.ID})

		timer := time.NewTimer(time.Duration(snap.Timeout) * time.Second)
		select {
		case <-e.decided:
		case <-timer.C:
			d.decide(e, protocol.StatusTimeout, "", "")
		case <-c.closed:
			d.decide(e, protocol.StatusCancelled, "", "agent disconnected")
		case <-d.ctx.Done():
			d.decide(e, protocol.StatusCancelled, "", "rexd shutting down")
		}
		timer.Stop()
	}

	snap := d.snapshot(e)
	switch snap.Status {
	case protocol.StatusApproved:
		by := snap.ApprovedBy
		if by == protocol.ApprovedAuto {
			by = protocol.ApprovedManual // agents should not learn that auto mode is on
		}
		c.send(protocol.AgentResp{Type: protocol.RespApproved, ID: snap.ID, Message: by})
		d.run(c, e)
	case protocol.StatusRejected:
		c.send(protocol.AgentResp{Type: protocol.RespRejected, ID: snap.ID, Message: snap.Feedback})
	case protocol.StatusTimeout:
		c.send(protocol.AgentResp{Type: protocol.RespTimeout, ID: snap.ID})
	default:
		c.send(protocol.AgentResp{Type: protocol.RespCancelled, ID: snap.ID, Message: snap.Feedback})
	}
}

// preDecision returns how the request can be approved without a human ("" if
// it cannot), and why auto mode held it.
func (d *Daemon) preDecision(e *entry) (string, []string) {
	d.mu.Lock()
	ev := e.req.Eval
	if ev != nil && ev.Whitelisted {
		d.mu.Unlock()
		return protocol.ApprovedWhitelist, nil
	}
	by, held := d.autoDecideLocked(&e.req)
	d.mu.Unlock()
	if by != "" || held != nil {
		d.emitState() // auto mode counters changed
	}
	return by, held
}

// run executes an approved request and streams the result to the agent.
func (d *Daemon) run(c *agentConn, e *entry) {
	ctx, cancel := context.WithCancel(d.ctx)
	defer cancel()
	go func() {
		select {
		case <-c.closed:
			cancel()
		case <-ctx.Done():
		}
	}()

	d.setRunning(e)
	if e.req.Kind == protocol.KindForward {
		f, err := d.startForward(e)
		if err != nil {
			d.finish(e, protocol.StatusFailed, protocol.ExitRexError, err.Error())
			c.fail("%v", err)
			return
		}
		d.finish(e, protocol.StatusCompleted, 0, "")
		c.send(protocol.AgentResp{Type: protocol.RespResult, ID: e.req.ID, Result: f})
		return
	}

	out := func(stream string, p []byte) {
		d.appendOutput(e, p)
		c.send(protocol.AgentResp{Type: protocol.RespOutput, Stream: stream, Data: p})
	}

	var code int
	var err error
	switch e.req.Kind {
	case protocol.KindExec:
		code, err = d.runExec(ctx, e, out)
	default:
		code, err = d.runTransfer(ctx, e, out)
	}

	switch {
	case ctx.Err() != nil:
		d.finish(e, protocol.StatusCancelled, -1, "cancelled while running")
	case err != nil:
		d.finish(e, protocol.StatusFailed, protocol.ExitRexError, err.Error())
		c.fail("%v", err)
	default:
		d.finish(e, protocol.StatusCompleted, code, "")
		c.send(protocol.AgentResp{Type: protocol.RespCompleted, ID: e.req.ID, ExitCode: code})
	}
}

func (d *Daemon) runExec(ctx context.Context, e *entry, out func(string, []byte)) (int, error) {
	d.mu.Lock()
	srv, err := d.cfg.ResolveServer(e.req.Server)
	d.mu.Unlock()
	if err != nil {
		return 0, err
	}
	return runSSH(ctx, srv, e.req.Command, e.stdin, out)
}

// limitedReader fails once more than n bytes were read, so a single huge
// message cannot exhaust memory.
type limitedReader struct {
	r interface{ Read([]byte) (int, error) }
	n int64
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.n <= 0 {
		return 0, errors.New("message too large")
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	return n, err
}
