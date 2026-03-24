package tui

import "github.com/charmbracelet/lipgloss"

var (
	// Colors
	colorPrimary   = lipgloss.Color("#7C3AED") // purple
	colorSuccess   = lipgloss.Color("#10B981") // green
	colorDanger    = lipgloss.Color("#EF4444") // red
	colorWarning   = lipgloss.Color("#F59E0B") // yellow
	colorMuted     = lipgloss.Color("#6B7280") // gray
	colorHighlight = lipgloss.Color("#3B82F6") // blue

	// Styles
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorPrimary).
			Padding(0, 1)

	tabActiveStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(colorPrimary).
			Padding(0, 2)

	tabInactiveStyle = lipgloss.NewStyle().
				Foreground(colorMuted).
				Padding(0, 2)

	selectedStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorHighlight)

	statusPendingStyle = lipgloss.NewStyle().
				Foreground(colorWarning).
				Bold(true)

	statusApprovedStyle = lipgloss.NewStyle().
				Foreground(colorSuccess)

	statusRejectedStyle = lipgloss.NewStyle().
				Foreground(colorDanger)

	statusTimeoutStyle = lipgloss.NewStyle().
				Foreground(colorMuted)

	descStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#D1D5DB"))

	cmdStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#93C5FD"))

	serverStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#A78BFA"))

	helpStyle = lipgloss.NewStyle().
			Foreground(colorMuted).
			Padding(1, 0)

	inputPromptStyle = lipgloss.NewStyle().
				Foreground(colorWarning).
				Bold(true)

	detailLabelStyle = lipgloss.NewStyle().
				Foreground(colorMuted).
				Width(10)

	detailValueStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#E5E7EB"))

	outputStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#9CA3AF"))

	borderStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorPrimary)
)
