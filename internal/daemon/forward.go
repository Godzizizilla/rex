package daemon

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"rex/internal/protocol"
)

// forward is a running `ssh -N -L` process owned by rexd. Forwards outlive
// the rex invocation that opened them and are closed on expiry, on request,
// or when rexd exits. They only ever listen on 127.0.0.1.
type forward struct {
	info    protocol.Forward
	cancel  context.CancelFunc
	closing string // reason, set when closed on purpose
	timer   *time.Timer
}

// startForward launches the ssh process for an approved forward request and
// waits until the local port accepts connections.
func (d *Daemon) startForward(e *entry) (protocol.Forward, error) {
	d.mu.Lock()
	srv, err := d.cfg.ResolveServer(e.req.Server)
	req := e.req
	d.mu.Unlock()
	if err != nil {
		return protocol.Forward{}, err
	}

	port := req.LocalPort
	if port == 0 {
		if port, err = freePort(); err != nil {
			return protocol.Forward{}, err
		}
	}

	ctx, cancel := context.WithCancel(d.ctx)
	args := append(sshArgs(srv, false),
		"-N",
		"-o", "ExitOnForwardFailure=yes",
		"-L", fmt.Sprintf("127.0.0.1:%d:%s:%d", port, req.RemoteHost, req.RemotePort),
		"--", srv.Host)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 3 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return protocol.Forward{}, fmt.Errorf("failed to start ssh: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	// Wait for the listener.
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(15 * time.Second)
	for {
		select {
		case err := <-exited:
			cancel()
			msg := strings.TrimSpace(stderr.String())
			if msg == "" && err != nil {
				msg = err.Error()
			}
			return protocol.Forward{}, fmt.Errorf("port forward failed: %s", msg)
		default:
		}
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			cancel()
			return protocol.Forward{}, fmt.Errorf("port forward did not come up within 15s: %s", strings.TrimSpace(stderr.String()))
		}
		time.Sleep(100 * time.Millisecond)
	}

	now := time.Now()
	ttl := time.Duration(req.TTL) * time.Minute
	f := &forward{cancel: cancel}
	d.mu.Lock()
	d.nextFwd++
	f.info = protocol.Forward{
		ID:         d.nextFwd,
		RequestID:  req.ID,
		Server:     req.Server,
		Desc:       req.Desc,
		LocalPort:  port,
		RemoteHost: req.RemoteHost,
		RemotePort: req.RemotePort,
		CreatedAt:  now.UnixMilli(),
		ExpiresAt:  now.Add(ttl).UnixMilli(),
	}
	id := f.info.ID
	f.timer = time.AfterFunc(ttl, func() { d.closeForward(id, "expired") })
	d.forwards[id] = f
	d.mu.Unlock()

	go func() {
		<-exited
		d.mu.Lock()
		reason := f.closing
		if d.forwards[id] == f {
			delete(d.forwards, id)
		}
		f.timer.Stop()
		d.mu.Unlock()
		cancel()
		if reason == "" && d.ctx.Err() == nil {
			d.notice(fmt.Sprintf("Port forward #%d (%s %s:%d) exited unexpectedly: %s",
				id, f.info.Server, f.info.RemoteHost, f.info.RemotePort, strings.TrimSpace(stderr.String())))
		}
		d.emitState()
	}()

	d.emitState()
	return f.info, nil
}

// closeForward stops a forward.
func (d *Daemon) closeForward(id int, reason string) error {
	d.mu.Lock()
	f := d.forwards[id]
	if f == nil {
		d.mu.Unlock()
		return fmt.Errorf("no active forward #%d", id)
	}
	delete(d.forwards, id)
	f.closing = reason
	d.mu.Unlock()

	f.cancel()
	d.emitState()
	if reason == "expired" {
		d.notice(fmt.Sprintf("Port forward #%d to %s:%d on %s expired", id, f.info.RemoteHost, f.info.RemotePort, f.info.Server))
	}
	return nil
}

func (d *Daemon) stopAllForwards() {
	d.mu.Lock()
	fwds := d.forwards
	d.forwards = make(map[int]*forward)
	for _, f := range fwds {
		f.closing = "rexd exiting"
	}
	d.mu.Unlock()
	for _, f := range fwds {
		f.cancel()
	}
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
