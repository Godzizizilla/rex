package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"rex/internal/protocol"
	"rex/skill"
)

func addCommands(root *cobra.Command) {
	root.AddCommand(serversCmd(), policyCmd(), cpCmd(), forwardCmd(), skillCmd())
}

// query sends an informational request and decodes its result into out.
func query(msg protocol.AgentMsg, out any) error {
	s, err := dial()
	if err != nil {
		return err
	}
	defer s.conn.Close()
	if err := s.send(msg); err != nil {
		return err
	}
	r, err := s.recv()
	if err != nil {
		return err
	}
	if r.Type == protocol.RespError {
		return errors.New(r.Message)
	}
	b, _ := json.Marshal(r.Result)
	return json.Unmarshal(b, out)
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func requireDesc() error {
	if strings.TrimSpace(flagDesc) == "" {
		return errors.New("-d/--desc is required: describe what this does and why")
	}
	return nil
}

// --- rex servers ---

func serversCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "servers",
		Short: "List the servers rex can reach",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var servers []protocol.ServerInfo
			if err := query(protocol.AgentMsg{Type: protocol.AgentServers}, &servers); err != nil {
				return err
			}
			if asJSON {
				return printJSON(servers)
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tHOST\tDESCRIPTION")
			for _, s := range servers {
				host := s.Host
				if s.Port != 22 {
					host = fmt.Sprintf("%s:%d", host, s.Port)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", s.Name, host, s.Description)
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

// --- rex policy ---

func policyCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "policy <server>",
		Short: "Show which operations run on a server without waiting for approval",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var info protocol.PolicyInfo
			if err := query(protocol.AgentMsg{Type: protocol.AgentPolicy, Server: args[0]}, &info); err != nil {
				return err
			}
			if asJSON {
				return printJSON(info)
			}
			fmt.Printf("Server %s: requests wait up to %ds for human approval.\n\n", info.Server, info.Timeout)
			if len(info.Whitelist) == 0 {
				fmt.Println("No whitelist rules: every request needs approval.")
			} else {
				fmt.Println("Runs immediately (whitelist):")
				w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
				for _, r := range info.Whitelist {
					fmt.Fprintf(w, "  %s\t%s\t%s\n", r.Kind(), r.Pattern, r.Note)
				}
				w.Flush()
				fmt.Print(`
A command line runs immediately only if every command in it (split on |, &&,
||, ; and newlines) matches an exec rule, and it contains no $(...), backticks,
<(...), redirects into files, loops/ifs, background jobs (&) or eval.
Everything else waits for a human.
`)
			}
			fmt.Printf("\nHigh-risk patterns, flagged to the reviewer:\n  %s\n", strings.Join(info.Blocklist, ", "))
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

// --- rex cp ---

const maxUploadSize = 50 << 20

// splitRemote parses "server:path" (scp syntax). Paths containing "/" before
// the colon are local.
func splitRemote(s string) (server, path string, ok bool) {
	i := strings.Index(s, ":")
	if i <= 0 || strings.ContainsAny(s[:i], `/\`) {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

func cpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cp <src> <dst>",
		Short: "Copy a file to or from a server (scp syntax, one side is server:path)",
		Example: `  rex cp -d "update nginx config" ./nginx.conf prod:/etc/nginx/nginx.conf
  rex cp -d "fetch error log" prod:/var/log/app/error.log ./`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireDesc(); err != nil {
				return err
			}
			srcServer, srcPath, srcRemote := splitRemote(args[0])
			dstServer, dstPath, dstRemote := splitRemote(args[1])
			switch {
			case srcRemote && dstRemote:
				return errors.New("copying between two servers is not supported")
			case !srcRemote && !dstRemote:
				return errors.New("one of <src> and <dst> must be server:path")
			case dstRemote:
				return upload(args[0], dstServer, dstPath)
			default:
				return download(srcServer, srcPath, args[1])
			}
		},
	}
}

func upload(local, server, remote string) error {
	if remote == "" {
		return errors.New("remote path is empty")
	}
	abs, err := filepath.Abs(local)
	if err != nil {
		return err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", local)
	}
	if fi.Size() > maxUploadSize {
		return fmt.Errorf("%s is larger than %d MB", local, maxUploadSize>>20)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return err
	}
	s, err := dial()
	if err != nil {
		return err
	}
	if err := s.send(protocol.AgentMsg{
		Type: protocol.AgentUpload, Server: server, Desc: flagDesc, Timeout: flagTimeout,
		LocalPath: abs, RemotePath: remote, Data: data,
	}); err != nil {
		return err
	}
	return exitCode(s.await(handlers{}))
}

func download(server, remote, local string) error {
	if remote == "" {
		return errors.New("remote path is empty")
	}
	if fi, err := os.Stat(local); (err == nil && fi.IsDir()) || strings.HasSuffix(local, "/") {
		local = filepath.Join(local, filepath.Base(remote))
	}
	abs, err := filepath.Abs(local)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(abs), ".rex-download-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	s, err := dial()
	if err != nil {
		tmp.Close()
		return err
	}
	if err := s.send(protocol.AgentMsg{
		Type: protocol.AgentDownload, Server: server, Desc: flagDesc, Timeout: flagTimeout,
		LocalPath: abs, RemotePath: remote,
	}); err != nil {
		tmp.Close()
		return err
	}
	code := s.await(handlers{output: func(stream string, data []byte) error {
		if stream == "stderr" {
			_, err := os.Stderr.Write(data)
			return err
		}
		_, err := tmp.Write(data)
		return err
	}})
	if err := tmp.Close(); err != nil && code == 0 {
		return err
	}
	if code != 0 {
		return errExit(code)
	}
	os.Chmod(tmp.Name(), 0644)
	return os.Rename(tmp.Name(), abs)
}

// --- rex forward ---

func forwardCmd() *cobra.Command {
	var spec, ttl string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "forward <server> [local_port:][host:]remote_port",
		Short: "Open a local port forward to a service reachable from a server (ssh -L)",
		Long: `Forward 127.0.0.1:<local_port> to <host>:<remote_port> as seen from the server.
The forward is held by rexd (it outlives this command) until it expires
(--ttl, default from config) or is closed with "rex forward close <id>".
Without local_port a free port is chosen. host defaults to localhost.`,
		Example: `  rex forward -d "inspect slow queries" prod 5432
  rex forward -d "open admin console" --ttl 2h prod 18080:10.0.0.5:8080
  rex forward ls
  rex forward close 3`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 2 {
				if spec != "" {
					return errors.New("pass the forward either with -L or as an argument")
				}
				spec = args[1]
			}
			if spec == "" {
				return errors.New("missing forward spec, e.g. rex forward -d <description> prod 5432")
			}
			if err := requireDesc(); err != nil {
				return err
			}
			lport, host, rport, err := parseForwardSpec(spec)
			if err != nil {
				return err
			}
			minutes, err := parseTTL(ttl)
			if err != nil {
				return err
			}
			s, err := dial()
			if err != nil {
				return err
			}
			if err := s.send(protocol.AgentMsg{
				Type: protocol.AgentForward, Server: args[0], Desc: flagDesc, Timeout: flagTimeout,
				LocalPort: lport, RemoteHost: host, RemotePort: rport, TTL: minutes,
			}); err != nil {
				return err
			}
			return exitCode(s.await(handlers{result: func(r *protocol.AgentResp) error {
				var f protocol.Forward
				b, _ := json.Marshal(r.Result)
				if err := json.Unmarshal(b, &f); err != nil {
					return err
				}
				if asJSON {
					return printJSON(f)
				}
				fmt.Printf("127.0.0.1:%d -> %s:%d on %s (forward #%d, expires %s; close with: rex forward close %d)\n",
					f.LocalPort, f.RemoteHost, f.RemotePort, f.Server, f.ID,
					time.UnixMilli(f.ExpiresAt).Format("15:04"), f.ID)
				return nil
			}}))
		},
	}
	cmd.Flags().StringVarP(&spec, "local", "L", "", "forward spec, as in ssh -L ([local_port:]host:remote_port)")
	cmd.Flags().StringVar(&ttl, "ttl", "", "lifetime, e.g. 30m or 2h (default from config)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")

	var lsJSON bool
	ls := &cobra.Command{
		Use:   "ls",
		Short: "List active port forwards",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var fwds []protocol.Forward
			if err := query(protocol.AgentMsg{Type: protocol.AgentForwardList}, &fwds); err != nil {
				return err
			}
			if lsJSON {
				return printJSON(fwds)
			}
			if len(fwds) == 0 {
				fmt.Println("No active port forwards.")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tLOCAL\tREMOTE\tSERVER\tEXPIRES\tDESCRIPTION")
			for _, f := range fwds {
				fmt.Fprintf(w, "%d\t127.0.0.1:%d\t%s:%d\t%s\t%s\t%s\n", f.ID, f.LocalPort, f.RemoteHost, f.RemotePort,
					f.Server, time.UnixMilli(f.ExpiresAt).Format("15:04"), f.Desc)
			}
			return w.Flush()
		},
	}
	ls.Flags().BoolVar(&lsJSON, "json", false, "print JSON")

	closeCmd := &cobra.Command{
		Use:   "close <id>",
		Short: "Close a port forward",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid forward id %q", args[0])
			}
			var closed int
			return query(protocol.AgentMsg{Type: protocol.AgentForwardClose, ForwardID: id}, &closed)
		},
	}
	cmd.AddCommand(ls, closeCmd)
	return cmd
}

// parseForwardSpec parses "rport", "host:rport" or "lport:host:rport".
func parseForwardSpec(spec string) (lport int, host string, rport int, err error) {
	parts := strings.Split(spec, ":")
	port := func(s string) (int, error) {
		p, err := strconv.Atoi(s)
		if err != nil || p < 1 || p > 65535 {
			return 0, fmt.Errorf("invalid port %q in forward spec %q", s, spec)
		}
		return p, nil
	}
	switch len(parts) {
	case 1:
		rport, err = port(parts[0])
	case 2:
		host = parts[0]
		rport, err = port(parts[1])
	case 3:
		if lport, err = port(parts[0]); err == nil {
			host = parts[1]
			rport, err = port(parts[2])
		}
	default:
		err = fmt.Errorf("invalid forward spec %q, want [local_port:][host:]remote_port", spec)
	}
	return
}

// parseTTL accepts minutes ("45") or a Go duration ("30m", "2h").
func parseTTL(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < time.Minute {
		return 0, fmt.Errorf("invalid --ttl %q, use e.g. 30m or 2h", s)
	}
	return int(d.Minutes()), nil
}

// --- rex skill ---

func skillCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "Print the agent skill that teaches coding agents to use rex",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Print(skill.Content)
			return nil
		},
	}
	var dir string
	install := &cobra.Command{
		Use:   "install",
		Short: "Install the skill for Claude Code (~/.claude/skills/rex/SKILL.md)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if dir == "" {
				home, err := os.UserHomeDir()
				if err != nil {
					return err
				}
				dir = filepath.Join(home, ".claude", "skills", "rex")
			}
			if err := os.MkdirAll(dir, 0755); err != nil {
				return err
			}
			path := filepath.Join(dir, "SKILL.md")
			if err := os.WriteFile(path, []byte(skill.Content), 0644); err != nil {
				return err
			}
			fmt.Println("Installed", path)
			return nil
		},
	}
	install.Flags().StringVar(&dir, "dir", "", "skill directory (default ~/.claude/skills/rex)")
	cmd.AddCommand(install)
	return cmd
}
