// Package protocol defines the two wire protocols spoken by rexd:
//
//   - the agent protocol, NDJSON over the Unix socket ~/.rex/rex.sock, used by
//     the rex CLI to submit requests and stream results;
//   - the control protocol, NDJSON over rexd's stdin/stdout, used by Rex.app to
//     observe requests and approve or reject them. Only the process that
//     spawned rexd holds these pipes, so an agent cannot approve its own
//     requests by talking to the socket.
//
// Timestamps are Unix milliseconds. []byte fields are base64 encoded.
// See docs/protocol.md for the full description.
package protocol

import (
	"rex/internal/config"
	"rex/internal/policy"
)

// Version is bumped on incompatible protocol changes.
const Version = 2

// ExitRexError is the exit code of rex when rex itself fails (rejected,
// approval timeout, daemon unreachable, ...). Like ssh, every other exit code
// comes from the remote command.
const ExitRexError = 255

// Request kinds.
const (
	KindExec     = config.RuleExec
	KindUpload   = config.RuleUpload
	KindDownload = config.RuleDownload
	KindForward  = config.RuleForward
)

// Request statuses.
const (
	StatusPending   = "pending"
	StatusApproved  = "approved"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusRejected  = "rejected"
	StatusTimeout   = "timeout"
	StatusCancelled = "cancelled"
	StatusFailed    = "failed"
)

// How a request got approved.
const (
	ApprovedManual    = "manual"
	ApprovedWhitelist = "whitelist"
	ApprovedAuto      = "auto"
)

// Request is a remote operation submitted by an agent.
type Request struct {
	ID      int    `json:"id"`
	Kind    string `json:"kind"`
	Server  string `json:"server"`
	Host    string `json:"host"`
	Desc    string `json:"desc"`
	Timeout int    `json:"timeout"` // approval timeout in seconds

	Command      string `json:"command,omitempty"`       // exec
	StdinSize    int    `json:"stdin_size,omitempty"`    // exec: bytes fed to the command
	StdinPreview string `json:"stdin_preview,omitempty"` // exec: first KBs of stdin

	LocalPath  string `json:"local_path,omitempty"`  // upload/download
	RemotePath string `json:"remote_path,omitempty"` // upload/download
	Size       int64  `json:"size,omitempty"`        // upload: file size
	Diff       string `json:"diff,omitempty"`        // upload: diff against the remote file

	LocalPort  int    `json:"local_port,omitempty"`  // forward
	RemoteHost string `json:"remote_host,omitempty"` // forward
	RemotePort int    `json:"remote_port,omitempty"` // forward
	TTL        int    `json:"ttl,omitempty"`         // forward: minutes

	Eval *policy.Evaluation `json:"eval,omitempty"`

	Status     string   `json:"status"`
	ApprovedBy string   `json:"approved_by,omitempty"` // manual, whitelist, auto
	HeldBy     []string `json:"held_by,omitempty"`     // why auto mode did not approve it
	Feedback   string   `json:"feedback,omitempty"`    // rejection reason
	Error      string   `json:"error,omitempty"`
	Output     string   `json:"output,omitempty"` // tail of stdout+stderr
	OutputSize int64    `json:"output_size,omitempty"`
	ExitCode   int      `json:"exit_code"`

	CreatedAt  int64 `json:"created_at"`
	Deadline   int64 `json:"deadline,omitempty"` // approval deadline while pending
	DecidedAt  int64 `json:"decided_at,omitempty"`
	FinishedAt int64 `json:"finished_at,omitempty"`
}

// Done reports whether the request reached a final state.
func (r *Request) Done() bool {
	switch r.Status {
	case StatusCompleted, StatusRejected, StatusTimeout, StatusCancelled, StatusFailed:
		return true
	}
	return false
}

// --- Agent protocol (rex CLI <-> rexd over the Unix socket) ---

// Agent message types sent by the CLI.
const (
	AgentExec         = "exec"
	AgentSubmit       = "submit" // legacy alias of exec
	AgentUpload       = "upload"
	AgentDownload     = "download"
	AgentForward      = "forward"
	AgentForwardList  = "forward_list"
	AgentForwardClose = "forward_close"
	AgentServers      = "servers"
	AgentPolicy       = "policy"
	AgentCancel       = "cancel"
)

// AgentMsg is sent by the rex CLI. The first message on a connection opens
// a request; a later {"type":"cancel"} (or closing the connection) withdraws
// it, killing the remote command if it is already running.
type AgentMsg struct {
	Type    string `json:"type"`
	Server  string `json:"server,omitempty"`
	Desc    string `json:"desc,omitempty"`
	Timeout int    `json:"timeout,omitempty"`

	Command string `json:"command,omitempty"`
	Stdin   []byte `json:"stdin,omitempty"`

	LocalPath  string `json:"local_path,omitempty"`
	RemotePath string `json:"remote_path,omitempty"`
	Data       []byte `json:"data,omitempty"` // upload content

	LocalPort  int    `json:"local_port,omitempty"`
	RemoteHost string `json:"remote_host,omitempty"`
	RemotePort int    `json:"remote_port,omitempty"`
	TTL        int    `json:"ttl,omitempty"`
	ForwardID  int    `json:"forward_id,omitempty"`
}

// Agent message types sent by rexd.
const (
	RespQueued    = "queued"    // waiting for a human
	RespApproved  = "approved"  // message holds how it was approved
	RespOutput    = "output"    // stream + data
	RespCompleted = "completed" // exit_code
	RespRejected  = "rejected"  // message holds the feedback
	RespTimeout   = "timeout"
	RespCancelled = "cancelled"
	RespError     = "error"
	RespResult    = "result" // answer to an informational query or a forward
)

// AgentResp is sent by rexd to the rex CLI.
type AgentResp struct {
	Type     string `json:"type"`
	ID       int    `json:"id,omitempty"`
	Stream   string `json:"stream,omitempty"` // stdout or stderr
	Data     []byte `json:"data,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
	Message  string `json:"message,omitempty"`
	Result   any    `json:"result,omitempty"`
}

// ServerInfo describes a server for `rex servers`.
type ServerInfo struct {
	Name        string `json:"name"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Description string `json:"description,omitempty"`
}

// PolicyInfo is the answer to `rex policy <server>`.
type PolicyInfo struct {
	Server    string                 `json:"server"`
	Timeout   int                    `json:"timeout"`
	Whitelist []config.WhitelistRule `json:"whitelist"`
	Blocklist []string               `json:"blocklist"`
}

// Forward is an active port forward.
type Forward struct {
	ID         int    `json:"id"`
	RequestID  int    `json:"request_id"`
	Server     string `json:"server"`
	Desc       string `json:"desc"`
	LocalPort  int    `json:"local_port"`
	RemoteHost string `json:"remote_host"`
	RemotePort int    `json:"remote_port"`
	CreatedAt  int64  `json:"created_at"`
	ExpiresAt  int64  `json:"expires_at"`
}

// --- Control protocol (Rex.app <-> rexd over stdin/stdout) ---

// Control commands sent by the app.
const (
	CtlApprove          = "approve"           // id
	CtlReject           = "reject"            // id, feedback
	CtlApproveWhitelist = "approve_whitelist" // id, indices: commands to add as "name *" rules
	CtlAutoStart        = "auto_start"        // minutes, servers (empty = all)
	CtlAutoStop         = "auto_stop"
	CtlForwardClose     = "forward_close" // id
	CtlWhitelistAdd     = "whitelist_add" // rule
	CtlWhitelistRemove  = "whitelist_remove"
	CtlReloadConfig     = "reload_config"
	CtlHistory          = "history" // limit
)

// ControlCmd is sent by the app on rexd's stdin.
type ControlCmd struct {
	Seq      int                   `json:"seq,omitempty"` // echoed in the result event
	Type     string                `json:"type"`
	ID       int                   `json:"id,omitempty"`
	Feedback string                `json:"feedback,omitempty"`
	Indices  []int                 `json:"indices,omitempty"`
	Minutes  int                   `json:"minutes,omitempty"`
	Servers  []string              `json:"servers,omitempty"`
	Rule     *config.WhitelistRule `json:"rule,omitempty"`
	Index    int                   `json:"index,omitempty"`
	Limit    int                   `json:"limit,omitempty"`
}

// Control event types sent by rexd.
const (
	EvtHello   = "hello"   // version, config, state, requests
	EvtRequest = "request" // a request was created or changed
	EvtState   = "state"   // auto mode / forwards changed
	EvtConfig  = "config"  // config changed
	EvtHistory = "history" // answer to CtlHistory
	EvtResult  = "result"  // seq, ok, error
	EvtNotice  = "notice"  // message for the user (e.g. auto mode stopped)
)

// ControlEvent is written by rexd on stdout.
type ControlEvent struct {
	Type     string      `json:"type"`
	Version  int         `json:"version,omitempty"`
	Seq      int         `json:"seq,omitempty"`
	OK       bool        `json:"ok,omitempty"`
	Error    string      `json:"error,omitempty"`
	Message  string      `json:"message,omitempty"`
	Request  *Request    `json:"request,omitempty"`
	Requests []*Request  `json:"requests,omitempty"`
	State    *State      `json:"state,omitempty"`
	Config   *ConfigView `json:"config,omitempty"`
}

// State is the daemon state shown in the app.
type State struct {
	Auto     AutoState `json:"auto"`
	Forwards []Forward `json:"forwards"`
}

// AutoState describes temporary auto-approve mode.
type AutoState struct {
	Active     bool     `json:"active"`
	StartedAt  int64    `json:"started_at,omitempty"`
	Until      int64    `json:"until,omitempty"`
	Servers    []string `json:"servers,omitempty"` // empty = all servers
	Approved   int      `json:"approved"`          // approved by auto mode in this session
	Held       int      `json:"held"`              // held for a human in this session
	StopReason string   `json:"stop_reason,omitempty"`
}

// ConfigView is the part of the config shown in the app.
type ConfigView struct {
	Path           string                 `json:"path"`
	Servers        []ServerInfo           `json:"servers"`
	Timeout        int                    `json:"timeout"`
	Whitelist      []config.WhitelistRule `json:"whitelist"`
	Blocklist      []string               `json:"blocklist"`
	AutoMaxMinutes int                    `json:"auto_max_minutes"`
}
