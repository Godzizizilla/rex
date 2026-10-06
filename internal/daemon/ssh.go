package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"rex/internal/config"
)

// sshArgs returns the common ssh options. Connections are multiplexed through
// a ControlMaster kept alive for a few minutes, so consecutive commands don't
// pay for a new handshake.
func sshArgs(srv *config.ServerConfig, multiplex bool) []string {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ServerAliveInterval=30",
	}
	if multiplex {
		if cp := controlPath(); cp != "" {
			args = append(args, "-o", "ControlMaster=auto", "-o", "ControlPath="+cp, "-o", "ControlPersist=10m")
		}
	} else {
		args = append(args, "-o", "ControlMaster=no", "-o", "ControlPath=none")
	}
	if srv.Port != 0 && srv.Port != 22 {
		args = append(args, "-p", fmt.Sprint(srv.Port))
	}
	return args
}

// controlPath returns the ControlPath template, or "" when the resulting
// socket path would exceed the Unix socket path limit.
func controlPath() string {
	p := filepath.Join(config.DefaultDir(), "cm-%C")
	if len(p)-2+40 > 100 { // %C expands to a 40 character hash
		return ""
	}
	return p
}

// runSSH runs a command on the server, streaming raw stdout/stderr chunks to
// out. It returns the remote exit code (255 for ssh failures, like ssh).
func runSSH(ctx context.Context, srv *config.ServerConfig, command string, stdin []byte, out func(stream string, p []byte)) (int, error) {
	args := append(sshArgs(srv, true), "--", srv.Host, command)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	if len(stdin) > 0 {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 3 * time.Second

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("failed to start ssh: %w", err)
	}

	var wg sync.WaitGroup
	pump := func(stream string, r io.Reader) {
		defer wg.Done()
		buf := make([]byte, 32<<10)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				out(stream, append([]byte(nil), buf[:n]...))
			}
			if err != nil {
				return
			}
		}
	}
	wg.Add(2)
	go pump("stdout", stdout)
	go pump("stderr", stderr)
	wg.Wait()

	err = cmd.Wait()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &exitErr) && exitErr.ExitCode() >= 0:
		return exitErr.ExitCode(), nil
	case ctx.Err() != nil:
		return -1, ctx.Err()
	default:
		return 0, fmt.Errorf("ssh: %w", err)
	}
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
