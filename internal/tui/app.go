package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"rex/internal/daemon"
	"rex/internal/notify"
	"rex/internal/protocol"
)

// View mode for the TUI.
type viewMode int

const (
	viewPending viewMode = iota
	viewHistory
	viewDetail
)

// Input mode for the TUI.
type inputMode int

const (
	inputNormal inputMode = iota
	inputReject
	inputWhitelist // multi-select sub-commands for whitelisting
)

// eventMsg wraps a daemon event as a tea.Msg.
type eventMsg daemon.Event

// tickMsg is sent periodically to update relative timestamps.
type tickMsg time.Time

// Model is the bubbletea model for the TUI.
type Model struct {
	daemon       *daemon.Daemon
	viewMode     viewMode
	inputMode    inputMode
	cursor       int
	rejectInput  string
	rejectTarget int
	detailID     int
	width        int
	height       int
	lastActivity time.Time
	message      string // transient status message
	messageTime  time.Time

	// Whitelist multi-select state
	wlTarget   int    // request ID being whitelisted
	wlCursor   int    // cursor within sub-command list
	wlSelected []bool // which sub-commands are selected
}

// NewModel creates a new TUI model.
func NewModel(d *daemon.Daemon) Model {
	return Model{
		daemon:       d,
		lastActivity: time.Now(),
	}
}

// Init starts listening for daemon events.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		waitForEvent(m.daemon.Events()),
		tickCmd(),
	)
}

func waitForEvent(events <-chan daemon.Event) tea.Cmd {
	return func() tea.Msg {
		e := <-events
		return eventMsg(e)
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// Update handles messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case tickMsg:
		return m, tickCmd()

	case eventMsg:
		m.handleEvent(daemon.Event(msg))
		return m, waitForEvent(m.daemon.Events())

	case tea.KeyMsg:
		m.lastActivity = time.Now()
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *Model) handleEvent(e daemon.Event) {
	switch e.Type {
	case "new":
		// Send macOS notification if TUI hasn't been active recently
		if time.Since(m.lastActivity) > 10*time.Second {
			go notify.Send("Rex - New Command",
				fmt.Sprintf("[%s] %s\n%s", e.Request.Server, e.Request.Desc, e.Request.Command))
		}
		m.setMessage(fmt.Sprintf("New request #%d from %s", e.Request.ID, e.Request.Server))
	case "timeout":
		m.setMessage(fmt.Sprintf("Request #%d timed out", e.Request.ID))
	case "completed":
		m.setMessage(fmt.Sprintf("Request #%d completed (exit %d)", e.Request.ID, e.Request.ExitCode))
	}
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Handle whitelist multi-select mode
	if m.inputMode == inputWhitelist {
		return m.handleWhitelistKey(key)
	}

	// Handle reject input mode
	if m.inputMode == inputReject {
		switch key {
		case "esc":
			m.inputMode = inputNormal
			m.rejectInput = ""
		case "enter":
			if m.rejectInput != "" {
				if err := m.daemon.Reject(m.rejectTarget, m.rejectInput); err != nil {
					m.setMessage(fmt.Sprintf("Error: %v", err))
				} else {
					m.setMessage(fmt.Sprintf("Rejected #%d", m.rejectTarget))
				}
			}
			m.inputMode = inputNormal
			m.rejectInput = ""
		case "backspace":
			if len(m.rejectInput) > 0 {
				m.rejectInput = m.rejectInput[:len(m.rejectInput)-1]
			}
		default:
			if len(key) == 1 || key == " " {
				m.rejectInput += key
			}
		}
		return m, nil
	}

	// Handle detail view
	if m.viewMode == viewDetail {
		switch key {
		case "esc", "q":
			m.viewMode = viewPending
		}
		return m, nil
	}

	// Normal mode
	switch key {
	case "q", "ctrl+c":
		return m, tea.Quit

	case "tab":
		if m.viewMode == viewPending {
			m.viewMode = viewHistory
		} else {
			m.viewMode = viewPending
		}
		m.cursor = 0

	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		items := m.currentItems()
		if m.cursor < len(items)-1 {
			m.cursor++
		}

	case "enter":
		if req := m.selectedRequest(); req != nil {
			m.detailID = req.ID
			m.viewMode = viewDetail
		}

	case "a":
		if m.viewMode == viewPending {
			if req := m.selectedRequest(); req != nil && req.Status == protocol.StatusPending {
				if err := m.daemon.Approve(req.ID); err != nil {
					m.setMessage(fmt.Sprintf("Error: %v", err))
				} else {
					m.setMessage(fmt.Sprintf("Approved #%d", req.ID))
				}
			}
		}

	case "r":
		if m.viewMode == viewPending {
			if req := m.selectedRequest(); req != nil && req.Status == protocol.StatusPending {
				m.inputMode = inputReject
				m.rejectTarget = req.ID
				m.rejectInput = ""
			}
		}

	case "w":
		if m.viewMode == viewPending {
			if req := m.selectedRequest(); req != nil && req.Status == protocol.StatusPending {
				m.enterWhitelistMode(req)
			}
		}
	}
	return m, nil
}

func (m *Model) enterWhitelistMode(req *protocol.Request) {
	m.inputMode = inputWhitelist
	m.wlTarget = req.ID
	m.wlCursor = 0

	// Pre-select sub-commands that are NOT already whitelisted
	m.wlSelected = make([]bool, len(req.SubCommands))
	if req.WLResult != nil {
		for i, match := range req.WLResult.SubCommands {
			m.wlSelected[i] = !match.Matched // select those not yet whitelisted
		}
	} else {
		for i := range m.wlSelected {
			m.wlSelected[i] = true
		}
	}
}

func (m Model) handleWhitelistKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.inputMode = inputNormal

	case "up", "k":
		if m.wlCursor > 0 {
			m.wlCursor--
		}
	case "down", "j":
		if m.wlCursor < len(m.wlSelected)-1 {
			m.wlCursor++
		}

	case " ":
		if m.wlCursor < len(m.wlSelected) {
			m.wlSelected[m.wlCursor] = !m.wlSelected[m.wlCursor]
		}

	case "a":
		allSelected := true
		for _, s := range m.wlSelected {
			if !s {
				allSelected = false
				break
			}
		}
		for i := range m.wlSelected {
			m.wlSelected[i] = !allSelected
		}

	case "enter":
		// Collect selected indices
		var indices []int
		for i, sel := range m.wlSelected {
			if sel {
				indices = append(indices, i)
			}
		}
		if err := m.daemon.AddWhitelistAndApprove(m.wlTarget, indices); err != nil {
			m.setMessage(fmt.Sprintf("Error: %v", err))
		} else {
			count := len(indices)
			m.setMessage(fmt.Sprintf("Whitelisted %d cmd(s) & approved #%d", count, m.wlTarget))
		}
		m.inputMode = inputNormal
	}
	return m, nil
}

func (m *Model) setMessage(s string) {
	m.message = s
	m.messageTime = time.Now()
}

func (m Model) currentItems() []*protocol.Request {
	all := m.daemon.Requests()
	var result []*protocol.Request
	for i := len(all) - 1; i >= 0; i-- {
		r := all[i]
		if m.viewMode == viewPending {
			if r.Status == protocol.StatusPending || r.Status == protocol.StatusExecuting {
				result = append(result, r)
			}
		} else {
			if r.Status != protocol.StatusPending {
				result = append(result, r)
			}
		}
	}
	return result
}

func (m Model) selectedRequest() *protocol.Request {
	items := m.currentItems()
	if m.cursor >= 0 && m.cursor < len(items) {
		return items[m.cursor]
	}
	return nil
}

// View renders the TUI.
func (m Model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	// Detail view
	if m.viewMode == viewDetail {
		return m.viewDetail()
	}

	var b strings.Builder

	// Header
	b.WriteString(m.viewHeader())
	b.WriteString("\n\n")

	// Items
	items := m.currentItems()
	if len(items) == 0 {
		if m.viewMode == viewPending {
			b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render("  No pending commands"))
		} else {
			b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render("  No history"))
		}
		b.WriteString("\n")
	} else {
		contentHeight := m.height - 8 // header + footer
		visibleItems := items
		if contentHeight > 0 {
			maxItems := contentHeight / 3
			if maxItems < 1 {
				maxItems = 1
			}
			if len(visibleItems) > maxItems {
				start := 0
				if m.cursor >= maxItems {
					start = m.cursor - maxItems + 1
				}
				end := start + maxItems
				if end > len(visibleItems) {
					end = len(visibleItems)
					start = end - maxItems
					if start < 0 {
						start = 0
					}
				}
				visibleItems = visibleItems[start:end]
			}
		}

		for i, req := range visibleItems {
			actualIdx := i
			if len(items) > len(visibleItems) {
				start := 0
				maxItems := contentHeight / 3
				if m.cursor >= maxItems {
					start = m.cursor - maxItems + 1
				}
				actualIdx = start + i
			}

			selected := actualIdx == m.cursor
			b.WriteString(m.renderItem(req, selected))
			b.WriteString("\n")
		}
	}

	// Status message
	if m.message != "" && time.Since(m.messageTime) < 5*time.Second {
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().Foreground(colorSuccess).Render("  " + m.message))
		b.WriteString("\n")
	}

	// Reject input
	if m.inputMode == inputReject {
		b.WriteString("\n")
		b.WriteString(inputPromptStyle.Render(fmt.Sprintf("  Reject #%d, reason: ", m.rejectTarget)))
		b.WriteString(m.rejectInput)
		b.WriteString("█\n")
	}

	// Whitelist multi-select overlay
	if m.inputMode == inputWhitelist {
		b.WriteString("\n")
		b.WriteString(m.viewWhitelistSelect())
	}

	// Footer
	b.WriteString("\n")
	b.WriteString(m.viewFooter())

	return b.String()
}

func (m Model) viewWhitelistSelect() string {
	var req *protocol.Request
	for _, r := range m.daemon.Requests() {
		if r.ID == m.wlTarget {
			req = r
			break
		}
	}
	if req == nil {
		return ""
	}

	var b strings.Builder
	b.WriteString(inputPromptStyle.Render(fmt.Sprintf("  Whitelist + Approve #%d", m.wlTarget)))
	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render("  Select sub-commands to add to whitelist:"))
	b.WriteString("\n\n")

	for i, sub := range req.SubCommands {
		cursor := "  "
		if i == m.wlCursor {
			cursor = selectedStyle.Render("> ")
		}

		checkbox := "[ ]"
		if i < len(m.wlSelected) && m.wlSelected[i] {
			checkbox = lipgloss.NewStyle().Foreground(colorSuccess).Render("[x]")
		}

		// Show the glob pattern that will be added
		pattern := sub.GlobPattern()

		// Mark already-whitelisted commands
		alreadyWL := ""
		if req.WLResult != nil && i < len(req.WLResult.SubCommands) && req.WLResult.SubCommands[i].Matched {
			alreadyWL = lipgloss.NewStyle().Foreground(colorMuted).Render(" (already whitelisted)")
		}

		// Show operator before command if not the first
		prefix := ""
		if sub.Operator != "" {
			prefix = lipgloss.NewStyle().Foreground(colorMuted).Render(sub.Operator+" ") + " "
		}

		b.WriteString(fmt.Sprintf("  %s %s %s%s  %s%s\n",
			cursor,
			checkbox,
			prefix,
			cmdStyle.Render(sub.Raw),
			lipgloss.NewStyle().Foreground(colorMuted).Render("→ "+pattern),
			alreadyWL,
		))
	}

	return b.String()
}

func (m Model) viewHeader() string {
	title := titleStyle.Render("Rex")

	pendingCount := 0
	historyCount := 0
	for _, r := range m.daemon.Requests() {
		if r.Status == protocol.StatusPending || r.Status == protocol.StatusExecuting {
			pendingCount++
		} else {
			historyCount++
		}
	}

	var pendingTab, historyTab string
	if m.viewMode == viewPending {
		pendingTab = tabActiveStyle.Render(fmt.Sprintf("Pending (%d)", pendingCount))
		historyTab = tabInactiveStyle.Render(fmt.Sprintf("History (%d)", historyCount))
	} else {
		pendingTab = tabInactiveStyle.Render(fmt.Sprintf("Pending (%d)", pendingCount))
		historyTab = tabActiveStyle.Render(fmt.Sprintf("History (%d)", historyCount))
	}

	return lipgloss.JoinHorizontal(lipgloss.Center, title, "  ", pendingTab, " ", historyTab)
}

func (m Model) viewFooter() string {
	var keys string
	switch m.inputMode {
	case inputWhitelist:
		keys = "[space]toggle  [a]toggle all  [enter]confirm  [esc]cancel"
	case inputReject:
		keys = "[enter]confirm  [esc]cancel"
	default:
		if m.viewMode == viewPending {
			keys = "[a]pprove  [r]eject  [w]hitelist+approve  [enter]detail  [tab]history  [q]uit"
		} else {
			keys = "[enter]detail  [tab]pending  [q]uit"
		}
	}
	return helpStyle.Render("  " + keys)
}

func (m Model) renderItem(req *protocol.Request, selected bool) string {
	cursor := "  "
	if selected {
		cursor = selectedStyle.Render("> ")
	}

	// Status badge
	var status string
	switch req.Status {
	case protocol.StatusPending:
		elapsed := time.Since(req.CreatedAt).Round(time.Second)
		status = statusPendingStyle.Render(fmt.Sprintf("PENDING %s", elapsed))
	case protocol.StatusExecuting:
		status = statusApprovedStyle.Render("EXECUTING...")
	case protocol.StatusApproved:
		status = statusApprovedStyle.Render("APPROVED")
	case protocol.StatusCompleted:
		if req.ExitCode == 0 {
			status = statusApprovedStyle.Render("DONE")
		} else {
			status = statusRejectedStyle.Render(fmt.Sprintf("DONE (exit %d)", req.ExitCode))
		}
	case protocol.StatusRejected:
		status = statusRejectedStyle.Render("REJECTED")
	case protocol.StatusTimeout:
		status = statusTimeoutStyle.Render("TIMEOUT")
	}

	header := fmt.Sprintf("%s#%-4d %s  %s",
		cursor,
		req.ID,
		serverStyle.Render(req.Server),
		status,
	)

	desc := fmt.Sprintf("    %s %s",
		detailLabelStyle.Render("DESC"),
		descStyle.Render(req.Desc),
	)

	cmd := req.Command
	if len(cmd) > m.width-20 && m.width > 30 {
		cmd = cmd[:m.width-23] + "..."
	}
	cmdLine := fmt.Sprintf("    %s %s",
		detailLabelStyle.Render("CMD"),
		cmdStyle.Render(cmd),
	)

	return header + "\n" + desc + "\n" + cmdLine
}

func (m Model) viewDetail() string {
	var req *protocol.Request
	for _, r := range m.daemon.Requests() {
		if r.ID == m.detailID {
			req = r
			break
		}
	}
	if req == nil {
		return "Request not found. Press [esc] to go back."
	}

	var b strings.Builder

	b.WriteString(titleStyle.Render(fmt.Sprintf("Rex - Request #%d", req.ID)))
	b.WriteString("\n\n")

	fields := []struct{ label, value string }{
		{"Server", fmt.Sprintf("%s (%s)", req.Server, req.Host)},
		{"Status", req.Status},
		{"Desc", req.Desc},
		{"Command", req.Command},
		{"Time", req.CreatedAt.Format("15:04:05")},
	}
	if req.ExitCode != 0 && req.Status == protocol.StatusCompleted {
		fields = append(fields, struct{ label, value string }{"Exit", fmt.Sprintf("%d", req.ExitCode)})
	}
	if req.Feedback != "" {
		fields = append(fields, struct{ label, value string }{"Feedback", req.Feedback})
	}

	for _, f := range fields {
		b.WriteString(fmt.Sprintf("  %s  %s\n",
			detailLabelStyle.Render(f.label),
			detailValueStyle.Render(f.value),
		))
	}

	// Show sub-commands breakdown
	if len(req.SubCommands) > 1 {
		b.WriteString("\n")
		b.WriteString(detailLabelStyle.Render("  Sub-cmds:"))
		b.WriteString("\n")
		for i, sub := range req.SubCommands {
			prefix := "  "
			if sub.Operator != "" {
				prefix = lipgloss.NewStyle().Foreground(colorMuted).Render("  " + sub.Operator + " ")
			}
			wlStatus := ""
			if req.WLResult != nil && i < len(req.WLResult.SubCommands) {
				if req.WLResult.SubCommands[i].Matched {
					wlStatus = statusApprovedStyle.Render(" [WL]")
				}
			}
			b.WriteString(fmt.Sprintf("  %s%s%s\n", prefix, cmdStyle.Render(sub.Raw), wlStatus))
		}
	}

	if req.Output != "" {
		b.WriteString("\n")
		b.WriteString(detailLabelStyle.Render("  Output:"))
		b.WriteString("\n")

		lines := strings.Split(req.Output, "\n")
		maxLines := m.height - 15
		if maxLines < 5 {
			maxLines = 5
		}
		if len(lines) > maxLines {
			lines = lines[len(lines)-maxLines:]
			b.WriteString(outputStyle.Render(fmt.Sprintf("  ... (%d lines truncated)\n", len(lines)-maxLines)))
		}
		for _, line := range lines {
			if len(line) > m.width-4 && m.width > 10 {
				line = line[:m.width-7] + "..."
			}
			b.WriteString(outputStyle.Render("  "+line) + "\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(helpStyle.Render("  [esc] back"))

	return b.String()
}
