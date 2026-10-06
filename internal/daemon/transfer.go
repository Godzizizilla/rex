package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"rex/internal/config"
	"rex/internal/policy"
	"rex/internal/protocol"
)

const (
	maxUpload   = 50 << 20
	maxDiffBase = 1 << 20 // remote files larger than this are not diffed
)

// prepareExtra handles upload, download and forward submissions.
func (d *Daemon) prepareExtra(cfg *config.Config, srv *config.ServerConfig, req protocol.Request, msg *protocol.AgentMsg) (*entry, error) {
	switch msg.Type {
	case protocol.AgentUpload:
		if msg.RemotePath == "" {
			return nil, errors.New("remote path is required")
		}
		if len(msg.Data) > maxUpload {
			return nil, fmt.Errorf("file is larger than %d MB", maxUpload>>20)
		}
		req.Kind = protocol.KindUpload
		req.LocalPath = msg.LocalPath
		req.RemotePath = msg.RemotePath
		req.Size = int64(len(msg.Data))
		req.Eval = policy.EvaluateTarget(cfg, protocol.KindUpload, msg.Server, msg.RemotePath)
		req.Diff = d.uploadDiff(srv, msg.RemotePath, path.Base(msg.LocalPath), msg.Data)
		return d.addEntry(req, nil, msg.Data), nil

	case protocol.AgentDownload:
		if msg.RemotePath == "" {
			return nil, errors.New("remote path is required")
		}
		req.Kind = protocol.KindDownload
		req.LocalPath = msg.LocalPath
		req.RemotePath = msg.RemotePath
		req.Eval = policy.EvaluateTarget(cfg, protocol.KindDownload, msg.Server, msg.RemotePath)
		return d.addEntry(req, nil, nil), nil

	case protocol.AgentForward:
		host := msg.RemoteHost
		if host == "" {
			host = "localhost"
		}
		if strings.ContainsAny(host, " \t\n/") {
			return nil, fmt.Errorf("invalid remote host %q", host)
		}
		if msg.RemotePort < 1 || msg.RemotePort > 65535 || msg.LocalPort < 0 || msg.LocalPort > 65535 {
			return nil, errors.New("invalid port")
		}
		ttl := msg.TTL
		if ttl <= 0 {
			ttl = cfg.ForwardDefaultTTL()
		}
		if ttl > cfg.ForwardMaxTTL() {
			return nil, fmt.Errorf("ttl must be at most %d minutes", cfg.ForwardMaxTTL())
		}
		req.Kind = protocol.KindForward
		req.LocalPort = msg.LocalPort
		req.RemoteHost = host
		req.RemotePort = msg.RemotePort
		req.TTL = ttl
		req.Eval = policy.EvaluateTarget(cfg, protocol.KindForward, msg.Server, fmt.Sprintf("%s:%d", host, msg.RemotePort))
		return d.addEntry(req, nil, nil), nil
	}
	return nil, fmt.Errorf("unknown request type %q", msg.Type)
}

// resolveTarget is a shell snippet that sets $t to the upload destination,
// appending the local file name when the remote path is a directory.
func resolveTarget(remotePath, base string) string {
	return fmt.Sprintf(`t=%s; if [ -d "$t" ]; then t="${t%%/}"/%s; fi; `, shellQuote(remotePath), shellQuote(base))
}

// uploadDiff fetches the current remote file (read-only) and diffs it against
// the upload, so the reviewer sees what will change.
func (d *Daemon) uploadDiff(srv *config.ServerConfig, remotePath, base string, data []byte) string {
	if bytes.IndexByte(data, 0) >= 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(d.ctx, 10*time.Second)
	defer cancel()
	script := resolveTarget(remotePath, base) +
		fmt.Sprintf(`if [ -f "$t" ]; then printf 'EXISTS\n'; head -c %d -- "$t"; else printf 'MISSING\n'; fi`, maxDiffBase+1)
	var out bytes.Buffer
	code, err := runSSH(ctx, srv, script, nil, func(stream string, p []byte) {
		if stream == "stdout" {
			out.Write(p)
		}
	})
	if err != nil || code != 0 {
		return ""
	}
	body := out.Bytes()
	switch {
	case bytes.HasPrefix(body, []byte("MISSING\n")):
		return unifiedDiff("/dev/null", remotePath, "", string(data))
	case bytes.HasPrefix(body, []byte("EXISTS\n")):
		old := body[len("EXISTS\n"):]
		if len(old) > maxDiffBase || bytes.IndexByte(old, 0) >= 0 {
			return ""
		}
		if bytes.Equal(old, data) {
			return "(identical to the remote file)"
		}
		return unifiedDiff(remotePath, remotePath, string(old), string(data))
	}
	return ""
}

// runTransfer executes an approved upload or download.
func (d *Daemon) runTransfer(ctx context.Context, e *entry, out func(string, []byte)) (int, error) {
	d.mu.Lock()
	srv, err := d.cfg.ResolveServer(e.req.Server)
	d.mu.Unlock()
	if err != nil {
		return 0, err
	}
	switch e.req.Kind {
	case protocol.KindUpload:
		script := resolveTarget(e.req.RemotePath, path.Base(e.req.LocalPath)) + `cat > "$t"`
		return runSSH(ctx, srv, script, e.data, out)
	case protocol.KindDownload:
		return runSSH(ctx, srv, "cat -- "+shellQuote(e.req.RemotePath), nil, out)
	}
	return 0, fmt.Errorf("unknown request kind %q", e.req.Kind)
}

// handleQuery answers informational requests that need no approval.
func (d *Daemon) handleQuery(c *agentConn, msg *protocol.AgentMsg) bool {
	switch msg.Type {
	case protocol.AgentServers:
		d.mu.Lock()
		servers := d.configViewLocked().Servers
		d.mu.Unlock()
		c.send(protocol.AgentResp{Type: protocol.RespResult, Result: servers})
	case protocol.AgentPolicy:
		d.mu.Lock()
		info := protocol.PolicyInfo{Server: msg.Server, Timeout: d.cfg.Timeout, Blocklist: policy.Blocklist(d.cfg)}
		for _, r := range d.cfg.Whitelist {
			if msg.Server == "" || r.AppliesTo(msg.Server) {
				info.Whitelist = append(info.Whitelist, r)
			}
		}
		_, err := d.cfg.ResolveServer(msg.Server)
		d.mu.Unlock()
		if msg.Server != "" && err != nil {
			c.fail("%v", err)
		} else {
			c.send(protocol.AgentResp{Type: protocol.RespResult, Result: info})
		}
	case protocol.AgentForwardList:
		d.mu.Lock()
		fwds := d.stateLocked().Forwards
		d.mu.Unlock()
		c.send(protocol.AgentResp{Type: protocol.RespResult, Result: fwds})
	case protocol.AgentForwardClose:
		if err := d.closeForward(msg.ForwardID, "closed by agent"); err != nil {
			c.fail("%v", err)
		} else {
			c.send(protocol.AgentResp{Type: protocol.RespResult, Result: msg.ForwardID})
		}
	default:
		return false
	}
	return true
}
