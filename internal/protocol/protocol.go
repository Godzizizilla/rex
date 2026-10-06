package protocol

import (
	"time"

	"rex/internal/policy"
)

// Request status constants.
const (
	StatusPending   = "pending"
	StatusApproved  = "approved"
	StatusExecuting = "executing"
	StatusCompleted = "completed"
	StatusRejected  = "rejected"
	StatusTimeout   = "timeout"
)

// Exit codes for rex CLI.
const (
	ExitRejected = 125
	ExitTimeout  = 124
	ExitConnErr  = 126
	ExitInternal = 127
)

// Request represents a command execution request from the agent.
type Request struct {
	ID        int                `json:"id"`
	Server    string             `json:"server"`
	Host      string             `json:"host"`
	Command   string             `json:"command"`
	Desc      string             `json:"desc"`
	Timeout   int                `json:"timeout"`
	Status    string             `json:"status"`
	Output    string             `json:"output"`
	ExitCode  int                `json:"exit_code"`
	Feedback  string             `json:"feedback"`
	CreatedAt time.Time          `json:"created_at"`
	Eval      *policy.Evaluation `json:"-"` // policy verdict
}

// --- Messages over Unix socket (NDJSON) ---

// ClientMsg is sent from rex CLI to the daemon.
type ClientMsg struct {
	Type    string `json:"type"`    // "submit"
	Server  string `json:"server"`  // server alias
	Command string `json:"command"` // remote command
	Desc    string `json:"desc"`    // command description
	Timeout int    `json:"timeout"` // approval timeout in seconds
}

// ServerMsg is sent from the daemon to rex CLI.
type ServerMsg struct {
	Type     string `json:"type"`                // queued, auto_approved, approved, output, completed, rejected, timeout, error
	ID       int    `json:"id,omitempty"`        // request ID
	Data     string `json:"data,omitempty"`      // output data or error message
	Stream   string `json:"stream,omitempty"`    // stdout or stderr
	ExitCode int    `json:"exit_code,omitempty"` // remote exit code
	Feedback string `json:"feedback,omitempty"`  // rejection feedback
}
