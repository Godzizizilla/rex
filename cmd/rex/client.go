package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"rex/internal/config"
	"rex/internal/protocol"
)

// errExit makes main exit with a specific code after printing nothing more.
type errExit int

func (e errExit) Error() string { return fmt.Sprintf("exit %d", int(e)) }

// session is one connection to rexd.
type session struct {
	conn net.Conn
	dec  *json.Decoder
	enc  *json.Encoder
}

func dial() (*session, error) {
	conn, err := net.DialTimeout("unix", config.SocketPath(), 3*time.Second)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to rexd (is Rex.app running?): %v", err)
	}
	s := &session{
		conn: conn,
		dec:  json.NewDecoder(bufio.NewReaderSize(conn, 64<<10)),
		enc:  json.NewEncoder(conn),
	}
	// Closing the connection withdraws the request (or kills the remote
	// command), so an interrupted rex never leaves anything behind.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		s2 := <-sig
		s.enc.Encode(protocol.AgentMsg{Type: protocol.AgentCancel})
		conn.Close()
		verbosef("interrupted, request withdrawn")
		if s2 == syscall.SIGINT {
			os.Exit(130)
		}
		os.Exit(143)
	}()
	return s, nil
}

func (s *session) send(m protocol.AgentMsg) error {
	if err := s.enc.Encode(m); err != nil {
		return fmt.Errorf("failed to send request: %v", err)
	}
	return nil
}

func (s *session) recv() (*protocol.AgentResp, error) {
	var r protocol.AgentResp
	if err := s.dec.Decode(&r); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
			return nil, errors.New("connection to rexd lost")
		}
		return nil, fmt.Errorf("connection to rexd lost: %v", err)
	}
	return &r, nil
}

// handlers customize how a request's results are consumed.
type handlers struct {
	output func(stream string, data []byte) error // default: copy to stdout/stderr
	result func(r *protocol.AgentResp) error      // RespResult (forwards)
}

// await follows a submitted request until it finishes and returns rex's exit
// code. Only failures of rex itself are reported on stderr; on success the
// output is exactly what the remote command produced.
func (s *session) await(h handlers) int {
	for {
		r, err := s.recv()
		if err != nil {
			return fail("%v", err)
		}
		switch r.Type {
		case protocol.RespQueued:
			verbosef("request #%d is waiting for approval", r.ID)
		case protocol.RespApproved:
			verbosef("request #%d approved (%s)", r.ID, r.Message)
		case protocol.RespOutput:
			if h.output != nil {
				if err := h.output(r.Stream, r.Data); err != nil {
					return fail("%v", err)
				}
			} else if r.Stream == "stderr" {
				os.Stderr.Write(r.Data)
			} else {
				os.Stdout.Write(r.Data)
			}
		case protocol.RespResult:
			if h.result != nil {
				if err := h.result(r); err != nil {
					return fail("%v", err)
				}
			}
			return 0
		case protocol.RespCompleted:
			verbosef("request #%d finished with exit code %d", r.ID, r.ExitCode)
			return r.ExitCode
		case protocol.RespRejected:
			if r.Message == "" {
				return fail("rejected by reviewer (request #%d was not executed)", r.ID)
			}
			return fail("rejected by reviewer: %s (request #%d was not executed)", r.Message, r.ID)
		case protocol.RespTimeout:
			return fail("approval timed out (request #%d was not executed)", r.ID)
		case protocol.RespCancelled:
			return fail("request #%d cancelled: %s", r.ID, r.Message)
		case protocol.RespError:
			return fail("%s", r.Message)
		}
	}
}

// fail prints a rex error and returns the rex error exit code.
func fail(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, "rex: "+format+"\n", args...)
	return protocol.ExitRexError
}

func verbosef(format string, args ...any) {
	if flagVerbose || os.Getenv("REX_VERBOSE") != "" {
		fmt.Fprintf(os.Stderr, "[rex] "+format+"\n", args...)
	}
}

const maxStdin = 16 << 20

// readStdin returns the data piped into rex. Only pipes and regular files are
// read: a terminal, /dev/null or an inherited socket means "no input", so rex
// never blocks on a stdin nobody is going to close.
func readStdin() ([]byte, error) {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return nil, nil
	}
	m := fi.Mode()
	if m&os.ModeNamedPipe == 0 && !m.IsRegular() {
		return nil, nil
	}
	hint := time.AfterFunc(3*time.Second, func() {
		fmt.Fprintln(os.Stderr, "rex: waiting for stdin to be closed (pass -n if the command reads no input)")
	})
	defer hint.Stop()
	data, err := io.ReadAll(io.LimitReader(os.Stdin, maxStdin+1))
	if err != nil {
		return nil, fmt.Errorf("reading stdin: %v", err)
	}
	if len(data) > maxStdin {
		return nil, fmt.Errorf("stdin is larger than %d MB; use `rex cp` to transfer files", maxStdin>>20)
	}
	return data, nil
}
