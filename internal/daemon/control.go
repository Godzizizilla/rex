package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"rex/internal/config"
	"rex/internal/policy"
	"rex/internal/protocol"
)

// ServeControl speaks the control protocol over r/w (rexd's stdin/stdout).
// It returns when r is closed, i.e. when the app that spawned rexd exits.
func (d *Daemon) ServeControl(r io.Reader, w io.Writer) error {
	enc := json.NewEncoder(w)
	writeErr := make(chan error, 1)
	if err := enc.Encode(d.hello()); err != nil {
		return err
	}
	go func() {
		for {
			select {
			case ev := <-d.events:
				if err := enc.Encode(ev); err != nil {
					writeErr <- err
					return
				}
			case <-d.ctx.Done():
				return
			}
		}
	}()

	readErr := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			var cmd protocol.ControlCmd
			if err := json.Unmarshal([]byte(line), &cmd); err != nil {
				d.emit(protocol.ControlEvent{Type: protocol.EvtResult, Error: "invalid command: " + err.Error()})
				continue
			}
			d.handleControl(cmd)
		}
		readErr <- sc.Err()
	}()

	select {
	case err := <-readErr:
		return err
	case err := <-writeErr:
		return err
	}
}

func (d *Daemon) handleControl(cmd protocol.ControlCmd) {
	var err error
	switch cmd.Type {
	case protocol.CtlApprove:
		err = d.Approve(cmd.ID)
	case protocol.CtlReject:
		err = d.Reject(cmd.ID, cmd.Feedback)
	case protocol.CtlApproveWhitelist:
		err = d.approveWhitelist(cmd.ID, cmd.Indices)
	case protocol.CtlAutoStart:
		err = d.autoStart(cmd.Minutes, cmd.Servers)
	case protocol.CtlAutoStop:
		d.autoStop("stopped by user")
	case protocol.CtlForwardClose:
		err = d.closeForward(cmd.ID, "closed by user")
	case protocol.CtlWhitelistAdd:
		err = d.whitelistAdd(cmd.Rule)
	case protocol.CtlWhitelistRemove:
		err = d.whitelistRemove(cmd.Index)
	case protocol.CtlReloadConfig:
		err = d.reloadConfig()
	case protocol.CtlHistory:
		d.emit(d.historyEvent(cmd.Limit))
	default:
		err = fmt.Errorf("unknown command %q", cmd.Type)
	}
	res := protocol.ControlEvent{Type: protocol.EvtResult, Seq: cmd.Seq, OK: err == nil}
	if err != nil {
		res.Error = err.Error()
	}
	d.emit(res)
}

// approveWhitelist adds rules covering the selected parts of a request and
// approves it. For exec requests indices select commands ("name *" rules);
// other kinds whitelist their target.
func (d *Daemon) approveWhitelist(id int, indices []int) error {
	d.mu.Lock()
	e := d.find(id)
	if e == nil {
		d.mu.Unlock()
		return fmt.Errorf("request #%d not found", id)
	}
	req := e.req
	if req.Status != protocol.StatusPending {
		d.mu.Unlock()
		return fmt.Errorf("request #%d is no longer pending", id)
	}
	var rules []config.WhitelistRule
	switch req.Kind {
	case protocol.KindExec:
		for _, i := range indices {
			if req.Eval == nil || i < 0 || i >= len(req.Eval.Commands) {
				continue
			}
			c := req.Eval.Commands[i]
			rules = append(rules, config.WhitelistRule{
				Pattern: policy.GlobPattern(c),
				Servers: []string{req.Server},
				Note:    fmt.Sprintf("added from request #%d: %s", id, c.Name),
			})
		}
	case protocol.KindDownload, protocol.KindUpload:
		rules = append(rules, config.WhitelistRule{
			Type:    req.Kind,
			Pattern: path.Dir(req.RemotePath) + "/*",
			Servers: []string{req.Server},
			Note:    fmt.Sprintf("added from request #%d", id),
		})
	case protocol.KindForward:
		rules = append(rules, config.WhitelistRule{
			Type:    req.Kind,
			Pattern: fmt.Sprintf("%s:%d", req.RemoteHost, req.RemotePort),
			Servers: []string{req.Server},
			Note:    fmt.Sprintf("added from request #%d", id),
		})
	}
	changed := false
	for _, r := range rules {
		if !d.hasRuleLocked(r) {
			d.cfg.Whitelist = append(d.cfg.Whitelist, r)
			changed = true
		}
	}
	var saveErr error
	if changed {
		saveErr = config.Save(d.cfg)
	}
	d.mu.Unlock()

	if changed {
		d.emitConfig()
	}
	if err := d.Approve(id); err != nil {
		return err
	}
	if saveErr != nil {
		return fmt.Errorf("approved, but saving the whitelist failed: %w", saveErr)
	}
	return nil
}

func (d *Daemon) hasRuleLocked(r config.WhitelistRule) bool {
	for _, x := range d.cfg.Whitelist {
		if x.Kind() == r.Kind() && x.Pattern == r.Pattern {
			for _, s := range r.Servers {
				if x.AppliesTo(s) {
					return true
				}
			}
		}
	}
	return false
}

func (d *Daemon) whitelistAdd(r *config.WhitelistRule) error {
	if r == nil || strings.TrimSpace(r.Pattern) == "" {
		return errors.New("rule pattern is required")
	}
	if len(r.Servers) == 0 {
		r.Servers = []string{"*"}
	}
	d.mu.Lock()
	d.cfg.Whitelist = append(d.cfg.Whitelist, *r)
	err := config.Save(d.cfg)
	d.mu.Unlock()
	d.emitConfig()
	return err
}

func (d *Daemon) whitelistRemove(i int) error {
	d.mu.Lock()
	if i < 0 || i >= len(d.cfg.Whitelist) {
		d.mu.Unlock()
		return fmt.Errorf("no whitelist rule at index %d", i)
	}
	d.cfg.Whitelist = append(d.cfg.Whitelist[:i], d.cfg.Whitelist[i+1:]...)
	err := config.Save(d.cfg)
	d.mu.Unlock()
	d.emitConfig()
	return err
}

func (d *Daemon) reloadConfig() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.cfg = cfg
	d.mu.Unlock()
	d.emitConfig()
	return nil
}

func (d *Daemon) historyEvent(limit int) protocol.ControlEvent {
	if limit <= 0 {
		limit = 200
	}
	past, _ := d.hist.load(limit)
	reqs := make([]*protocol.Request, len(past))
	for i := range past {
		reqs[i] = &past[i]
	}
	return protocol.ControlEvent{Type: protocol.EvtHistory, Requests: reqs}
}
