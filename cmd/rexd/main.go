// rexd is the approval daemon. It is normally launched by Rex.app, which
// talks to it over stdin/stdout (the control protocol); agents talk to it
// through the rex CLI over ~/.rex/rex.sock.
package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"rex/internal/config"
	"rex/internal/daemon"
	"rex/internal/protocol"
)

const exampleConfig = `servers:
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
    note: "Allow tail commands"`

func main() {
	log.SetFlags(log.LstdFlags)
	log.SetPrefix("rexd: ")
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Printf("rexd protocol v%d\n", protocol.Version)
		return
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n\nCreate a config file at %s, example:\n\n%s\n", err, config.ConfigPath(), exampleConfig)
		os.Exit(1)
	}

	d := daemon.New(cfg)
	if err := d.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	log.Printf("listening on %s", config.SocketPath())
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		log.Print("rexd is normally started by Rex.app; the control protocol is spoken on stdin/stdout (see docs/protocol.md)")
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	done := make(chan error, 1)
	go func() { done <- d.ServeControl(os.Stdin, os.Stdout) }()

	select {
	case s := <-sig:
		log.Printf("received %v, shutting down", s)
	case err := <-done:
		if err != nil {
			log.Printf("control channel: %v", err)
		}
		log.Print("control channel closed, shutting down")
	}
	d.Stop()
}
