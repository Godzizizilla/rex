package daemon

import (
	"context"
	"errors"

	"rex/internal/config"
	"rex/internal/protocol"
)

// Placeholders for features implemented in later commits.

type forward struct{ info protocol.Forward }

func (d *Daemon) stopAllForwards()                                {}
func (d *Daemon) closeForward(int, string) error                  { return errors.New("not supported") }
func (d *Daemon) handleQuery(*agentConn, *protocol.AgentMsg) bool { return false }

func (d *Daemon) prepareExtra(*config.Config, *config.ServerConfig, protocol.Request, *protocol.AgentMsg) (*entry, error) {
	return nil, errors.New("unsupported request type")
}

func (d *Daemon) runExtra(context.Context, *agentConn, *entry, func(string, []byte)) (int, error) {
	return 0, errors.New("unsupported request type")
}
