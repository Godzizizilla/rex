// rex runs commands on remote servers after human approval, with an
// interface modelled on ssh:
//
//	rex -d "why" prod -- tail -n 100 /var/log/app.log
//
// On success rex prints exactly what the remote command printed and exits
// with its exit code. When rex itself fails (rejected, approval timeout,
// rexd unreachable) it prints "rex: ..." on stderr and exits with 255.
package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"rex/internal/protocol"
)

var (
	flagDesc    string
	flagTimeout int
	flagVerbose bool

	flagNoStdin bool
	flagServer  string // legacy -s
	flagCmd     string // legacy -c
)

func main() {
	root := &cobra.Command{
		Use:   "rex [flags] <server> -- <command...>",
		Short: "Run commands on remote servers after human approval",
		Long: `rex submits a remote command to rexd (Rex.app) for human approval and runs it over ssh.

Whitelisted commands run immediately. Everything else waits until a human
approves or rejects it in Rex.app, or until the approval timeout expires.

Output and exit code are those of the remote command, like ssh. If rex itself
fails (rejected, approval timeout, rexd not running) it prints "rex: <reason>"
on stderr and exits with 255.`,
		Example: `  rex -d "check recent errors" prod -- tail -n 100 /var/log/app.log
  rex -d "count 5xx" prod -- 'grep " 5[0-9][0-9] " /var/log/nginx/access.log | wc -l'
  cat migrate.sql | rex -d "apply migration 042" prod -- psql app`,
		Args:          cobra.ArbitraryArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          runExec,
	}
	pf := root.PersistentFlags()
	pf.StringVarP(&flagDesc, "desc", "d", "", "what this does and why, shown to the reviewer (required)")
	pf.IntVarP(&flagTimeout, "timeout", "t", 0, "approval timeout in seconds (default from config)")
	pf.BoolVarP(&flagVerbose, "verbose", "v", false, "print progress (queued/approved) on stderr")

	f := root.Flags()
	f.BoolVarP(&flagNoStdin, "no-stdin", "n", false, "do not forward stdin to the remote command")
	f.StringVarP(&flagServer, "server", "s", "", "server alias (legacy form of the positional argument)")
	f.StringVarP(&flagCmd, "cmd", "c", "", "remote command (legacy form of the arguments after --)")

	addCommands(root)

	err := root.Execute()
	var code errExit
	switch {
	case err == nil:
	case errors.As(err, &code):
		os.Exit(int(code))
	default:
		fmt.Fprintf(os.Stderr, "rex: %v\n", err)
		os.Exit(protocol.ExitRexError)
	}
}

func runExec(cmd *cobra.Command, args []string) error {
	server := flagServer
	if server == "" {
		if len(args) == 0 || cmd.ArgsLenAtDash() == 0 {
			return errors.New("usage: rex -d <description> <server> -- <command...>  (see rex --help)")
		}
		server, args = args[0], args[1:]
	}
	command := flagCmd
	if command == "" {
		// Like ssh, multiple arguments are joined with spaces and interpreted
		// by the remote shell.
		command = strings.Join(args, " ")
	} else if len(args) > 0 {
		return errors.New("pass the command either with -c or after --, not both")
	}
	if strings.TrimSpace(command) == "" {
		return errors.New("no command given: rex -d <description> <server> -- <command...>")
	}
	if strings.TrimSpace(flagDesc) == "" {
		return errors.New("-d/--desc is required: describe what the command does and why")
	}

	var stdin []byte
	if !flagNoStdin {
		var err error
		if stdin, err = readStdin(); err != nil {
			return err
		}
	}

	s, err := dial()
	if err != nil {
		return err
	}
	if err := s.send(protocol.AgentMsg{
		Type:    protocol.AgentExec,
		Server:  server,
		Command: command,
		Desc:    flagDesc,
		Timeout: flagTimeout,
		Stdin:   stdin,
	}); err != nil {
		return err
	}
	return exitCode(s.await(handlers{}))
}

func exitCode(code int) error {
	if code == 0 {
		return nil
	}
	return errExit(code)
}

// addCommands registers the subcommands (cp, forward, servers, policy, skill).
func addCommands(root *cobra.Command) {}
