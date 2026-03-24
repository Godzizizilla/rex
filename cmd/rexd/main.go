package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"rex/internal/config"
	"rex/internal/daemon"
	"rex/internal/tui"
)

func main() {
	// Load config
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		fmt.Fprintf(os.Stderr, "\nCreate a config file at %s, example:\n\n", config.ConfigPath())
		fmt.Fprintln(os.Stderr, `servers:
  prod:
    host: deploy@10.0.1.100
    description: "Production server"
  staging:
    host: admin@staging.example.com
    port: 2222
    description: "Staging server"

timeout: 60

whitelist:
  - pattern: "tail *"
    servers: ["*"]
    note: "Allow tail commands"`)
		os.Exit(1)
	}

	// Create and start daemon
	d := daemon.New(cfg)
	if err := d.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer d.Stop()

	fmt.Fprintf(os.Stderr, "rexd listening on %s\n", config.SocketPath())

	// Start TUI
	model := tui.NewModel(d)
	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
		os.Exit(1)
	}
}
