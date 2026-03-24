package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"rex/internal/config"
	"rex/internal/protocol"
)

// Event types sent to the TUI.
type Event struct {
	Type    string
	Request *protocol.Request
}

// Daemon manages the command queue, approvals, and execution.
type Daemon struct {
	cfg      *config.Config
	requests []*protocol.Request
	conns    map[int]net.Conn // request ID -> CLI connection
	mu       sync.Mutex
	nextID   atomic.Int32
	events   chan Event
	listener net.Listener
}

// New creates a new Daemon instance.
func New(cfg *config.Config) *Daemon {
	d := &Daemon{
		cfg:    cfg,
		conns:  make(map[int]net.Conn),
		events: make(chan Event, 64),
	}
	d.nextID.Store(0)
	return d
}

// Events returns the event channel for TUI consumption.
func (d *Daemon) Events() <-chan Event {
	return d.events
}

// Config returns the daemon's config.
func (d *Daemon) Config() *config.Config {
	return d.cfg
}

// Requests returns a snapshot of all requests.
func (d *Daemon) Requests() []*protocol.Request {
	d.mu.Lock()
	defer d.mu.Unlock()
	result := make([]*protocol.Request, len(d.requests))
	copy(result, d.requests)
	return result
}

// Start begins listening on the Unix socket.
func (d *Daemon) Start() error {
	sockPath := config.SocketPath()

	// Remove stale socket
	if _, err := os.Stat(sockPath); err == nil {
		// Try connecting to check if another daemon is running
		conn, err := net.DialTimeout("unix", sockPath, time.Second)
		if err == nil {
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
	d.listener = ln

	go d.acceptLoop()
	return nil
}

// Stop shuts down the daemon.
func (d *Daemon) Stop() {
	if d.listener != nil {
		d.listener.Close()
	}
	os.Remove(config.SocketPath())
}

func (d *Daemon) acceptLoop() {
	for {
		conn, err := d.listener.Accept()
		if err != nil {
			return // listener closed
		}
		go d.handleConn(conn)
	}
}

func (d *Daemon) handleConn(conn net.Conn) {
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		conn.Close()
		return
	}

	var msg protocol.ClientMsg
	if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
		d.sendMsg(conn, protocol.ServerMsg{Type: "error", Data: "invalid message"})
		conn.Close()
		return
	}

	if msg.Type != "submit" {
		d.sendMsg(conn, protocol.ServerMsg{Type: "error", Data: "unknown message type"})
		conn.Close()
		return
	}

	// Resolve server
	srv, err := d.cfg.ResolveServer(msg.Server)
	if err != nil {
		d.sendMsg(conn, protocol.ServerMsg{Type: "error", Data: err.Error()})
		conn.Close()
		return
	}

	timeout := msg.Timeout
	if timeout <= 0 {
		timeout = d.cfg.Timeout
	}

	id := int(d.nextID.Add(1))
	req := &protocol.Request{
		ID:        id,
		Server:    msg.Server,
		Host:      srv.Host,
		Command:   msg.Command,
		Desc:      msg.Desc,
		Timeout:   timeout,
		Status:    protocol.StatusPending,
		CreatedAt: time.Now(),
	}

	d.mu.Lock()
	d.requests = append(d.requests, req)
	d.conns[id] = conn
	d.mu.Unlock()

	// Check whitelist
	if matched, note := d.cfg.MatchWhitelist(msg.Server, msg.Command); matched {
		d.sendMsg(conn, protocol.ServerMsg{Type: "auto_approved", ID: id, Data: note})
		req.Status = protocol.StatusApproved
		d.events <- Event{Type: "auto_approved", Request: req}
		go d.executeRequest(req)
		return
	}

	// Queue for approval
	d.sendMsg(conn, protocol.ServerMsg{Type: "queued", ID: id})
	d.events <- Event{Type: "new", Request: req}

	// Start timeout timer
	go func() {
		time.Sleep(time.Duration(timeout) * time.Second)
		d.mu.Lock()
		if req.Status == protocol.StatusPending {
			req.Status = protocol.StatusTimeout
			d.mu.Unlock()
			d.sendMsg(conn, protocol.ServerMsg{Type: "timeout", ID: id})
			conn.Close()
			d.mu.Lock()
			delete(d.conns, id)
			d.mu.Unlock()
			d.events <- Event{Type: "timeout", Request: req}
		} else {
			d.mu.Unlock()
		}
	}()
}

// Approve approves a pending request.
func (d *Daemon) Approve(id int) error {
	d.mu.Lock()
	req := d.findRequest(id)
	if req == nil {
		d.mu.Unlock()
		return fmt.Errorf("request %d not found", id)
	}
	if req.Status != protocol.StatusPending {
		d.mu.Unlock()
		return fmt.Errorf("request %d is not pending (status: %s)", id, req.Status)
	}
	req.Status = protocol.StatusApproved
	conn := d.conns[id]
	d.mu.Unlock()

	if conn != nil {
		d.sendMsg(conn, protocol.ServerMsg{Type: "approved", ID: id})
	}
	d.events <- Event{Type: "approved", Request: req}

	go d.executeRequest(req)
	return nil
}

// Reject rejects a pending request with feedback.
func (d *Daemon) Reject(id int, feedback string) error {
	d.mu.Lock()
	req := d.findRequest(id)
	if req == nil {
		d.mu.Unlock()
		return fmt.Errorf("request %d not found", id)
	}
	if req.Status != protocol.StatusPending {
		d.mu.Unlock()
		return fmt.Errorf("request %d is not pending (status: %s)", id, req.Status)
	}
	req.Status = protocol.StatusRejected
	req.Feedback = feedback
	conn := d.conns[id]
	d.mu.Unlock()

	if conn != nil {
		d.sendMsg(conn, protocol.ServerMsg{Type: "rejected", ID: id, Feedback: feedback})
		conn.Close()
	}

	d.mu.Lock()
	delete(d.conns, id)
	d.mu.Unlock()

	d.events <- Event{Type: "rejected", Request: req}
	return nil
}

// AddWhitelistAndApprove adds the command pattern to whitelist and approves the request.
func (d *Daemon) AddWhitelistAndApprove(id int) error {
	d.mu.Lock()
	req := d.findRequest(id)
	if req == nil {
		d.mu.Unlock()
		return fmt.Errorf("request %d not found", id)
	}
	d.mu.Unlock()

	// Add to whitelist config
	rule := config.WhitelistRule{
		Pattern: req.Command,
		Servers: []string{req.Server},
		Note:    fmt.Sprintf("added from TUI for request #%d", id),
	}
	d.cfg.Whitelist = append(d.cfg.Whitelist, rule)
	if err := config.Save(d.cfg); err != nil {
		// Non-fatal, continue with approval
		_ = err
	}

	return d.Approve(id)
}

func (d *Daemon) executeRequest(req *protocol.Request) {
	d.mu.Lock()
	req.Status = protocol.StatusExecuting
	conn := d.conns[req.ID]
	srv, _ := d.cfg.ResolveServer(req.Server)
	d.mu.Unlock()

	d.events <- Event{Type: "executing", Request: req}

	exitCode := ExecuteSSH(srv, req.Command, func(stream, data string) {
		d.mu.Lock()
		req.Output += data
		d.mu.Unlock()

		if conn != nil {
			d.sendMsg(conn, protocol.ServerMsg{
				Type:   "output",
				ID:     req.ID,
				Data:   data,
				Stream: stream,
			})
		}
		d.events <- Event{Type: "output", Request: req}
	})

	d.mu.Lock()
	req.Status = protocol.StatusCompleted
	req.ExitCode = exitCode
	d.mu.Unlock()

	if conn != nil {
		d.sendMsg(conn, protocol.ServerMsg{
			Type:     "completed",
			ID:       req.ID,
			ExitCode: exitCode,
		})
		conn.Close()
	}

	d.mu.Lock()
	delete(d.conns, req.ID)
	d.mu.Unlock()

	d.events <- Event{Type: "completed", Request: req}
}

func (d *Daemon) findRequest(id int) *protocol.Request {
	for _, r := range d.requests {
		if r.ID == id {
			return r
		}
	}
	return nil
}

func (d *Daemon) sendMsg(conn net.Conn, msg protocol.ServerMsg) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	data = append(data, '\n')
	conn.Write(data)
}
