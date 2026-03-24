package daemon

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"rex/internal/config"
	"sync"
)

// OutputCallback is called with each chunk of output from the remote command.
type OutputCallback func(stream string, data string)

// ExecuteSSH runs a command on a remote server via ssh.
// It streams output through the callback and returns the exit code.
func ExecuteSSH(srv *config.ServerConfig, command string, cb OutputCallback) int {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "StrictHostKeyChecking=accept-new",
	}
	if srv.Port != 22 {
		args = append(args, "-p", fmt.Sprintf("%d", srv.Port))
	}
	args = append(args, srv.Host, command)

	cmd := exec.Command("ssh", args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cb("stderr", fmt.Sprintf("rex: failed to create stdout pipe: %v\n", err))
		return 1
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cb("stderr", fmt.Sprintf("rex: failed to create stderr pipe: %v\n", err))
		return 1
	}

	if err := cmd.Start(); err != nil {
		cb("stderr", fmt.Sprintf("rex: failed to start ssh: %v\n", err))
		return 1
	}

	var wg sync.WaitGroup
	wg.Add(2)

	streamReader := func(stream string, r io.Reader) {
		defer wg.Done()
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
		for scanner.Scan() {
			cb(stream, scanner.Text()+"\n")
		}
	}

	go streamReader("stdout", stdout)
	go streamReader("stderr", stderr)

	wg.Wait()

	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		cb("stderr", fmt.Sprintf("rex: ssh error: %v\n", err))
		return 1
	}
	return 0
}
