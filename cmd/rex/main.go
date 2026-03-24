package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/spf13/cobra"

	"rex/internal/config"
	"rex/internal/protocol"
)

var (
	flagServer  string
	flagDesc    string
	flagCmd     string
	flagTimeout int
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "rex",
		Short: "Remote command execution gateway for coding agents",
		Long: `Rex submits remote commands for human approval before execution.
Commands are reviewed in the rexd TUI before being executed on remote servers.`,
		RunE: run,
	}

	rootCmd.Flags().StringVarP(&flagServer, "server", "s", "", "Server alias (required, defined in ~/.rex/config.yaml)")
	rootCmd.Flags().StringVarP(&flagDesc, "desc", "d", "", "Command description - what this command does and why (required)")
	rootCmd.Flags().StringVarP(&flagCmd, "cmd", "c", "", "Remote command to execute (required)")
	rootCmd.Flags().IntVarP(&flagTimeout, "timeout", "t", 0, "Approval timeout in seconds (default: from config)")

	rootCmd.MarkFlagRequired("server")
	rootCmd.MarkFlagRequired("desc")
	rootCmd.MarkFlagRequired("cmd")

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func run(cmd *cobra.Command, args []string) error {
	// Load config to validate server alias
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if _, err := cfg.ResolveServer(flagServer); err != nil {
		return err
	}

	timeout := flagTimeout
	if timeout <= 0 {
		timeout = cfg.Timeout
	}

	// Connect to daemon
	conn, err := net.DialTimeout("unix", config.SocketPath(), 3*time.Second)
	if err != nil {
		return fmt.Errorf("cannot connect to rexd (is it running?): %w\nStart it with: rexd", err)
	}
	defer conn.Close()

	// Submit request
	msg := protocol.ClientMsg{
		Type:    "submit",
		Server:  flagServer,
		Command: flagCmd,
		Desc:    flagDesc,
		Timeout: timeout,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}

	// Read responses
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 256*1024), 10*1024*1024)

	for scanner.Scan() {
		var resp protocol.ServerMsg
		if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
			fmt.Fprintf(os.Stderr, "rex: invalid response from daemon\n")
			continue
		}

		switch resp.Type {
		case "queued":
			fmt.Fprintf(os.Stderr, "[rex] Request #%d queued, waiting for approval...\n", resp.ID)

		case "auto_approved":
			note := resp.Data
			if note != "" {
				note = ": " + note
			}
			fmt.Fprintf(os.Stderr, "[rex] Auto-approved (whitelist%s)\n", note)

		case "approved":
			fmt.Fprintf(os.Stderr, "[rex] Approved, executing...\n")

		case "output":
			if resp.Stream == "stderr" {
				fmt.Fprint(os.Stderr, resp.Data)
			} else {
				fmt.Print(resp.Data)
			}

		case "completed":
			if resp.ExitCode != 0 {
				fmt.Fprintf(os.Stderr, "[rex] Command exited with code %d\n", resp.ExitCode)
			}
			os.Exit(resp.ExitCode)

		case "rejected":
			feedback := resp.Feedback
			if feedback == "" {
				feedback = "no reason provided"
			}
			fmt.Fprintf(os.Stderr, "[rex] REJECTED: %s\n", feedback)
			os.Exit(protocol.ExitRejected)

		case "timeout":
			fmt.Fprintf(os.Stderr, "[rex] Timed out waiting for approval\n")
			os.Exit(protocol.ExitTimeout)

		case "error":
			fmt.Fprintf(os.Stderr, "[rex] Error: %s\n", resp.Data)
			os.Exit(protocol.ExitInternal)
		}
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "[rex] Connection error: %v\n", err)
		os.Exit(protocol.ExitConnErr)
	}

	// Connection closed unexpectedly
	fmt.Fprintf(os.Stderr, "[rex] Connection closed\n")
	os.Exit(protocol.ExitConnErr)
	return nil
}
