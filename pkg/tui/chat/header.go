package chat

import (
	"strings"

	"charm.land/lipgloss/v2"

	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
)

var (
	headerLogoStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	headerSepStyle  = lipgloss.NewStyle().Faint(true)
	headerItemStyle = lipgloss.NewStyle().Faint(true)
)

// renderHeader renders a single-line breadcrumb status bar:
//
//	MASS > workspace > agent > model                            ● status
func renderHeader(workspaceName, agentName, agentStatus, currentModel string, width int) string {
	sep := headerSepStyle.Render(" > ")

	left := headerLogoStyle.Render("MASS")
	if workspaceName != "" {
		left += sep + headerItemStyle.Render(workspaceName)
	}
	if agentName != "" {
		left += sep + headerItemStyle.Render(agentName)
	}
	if currentModel != "" {
		left += sep + headerItemStyle.Render(currentModel)
	}

	right := renderHeaderStatus(agentStatus)
	leftWidth := lipgloss.Width(left)
	rightWidth := lipgloss.Width(right)
	gap := strings.Repeat(" ", max(0, width-leftWidth-rightWidth))

	return left + gap + right
}

func renderHeaderStatus(status string) string {
	if status == "" {
		status = "unknown"
	}
	switch status {
	case string(apiruntime.StatusRunning):
		return styleStatusRunning.Render("● running")
	case string(apiruntime.StatusIdle):
		return styleStatusIdle.Render("● idle")
	case string(apiruntime.StatusError):
		return styleStatusError.Render("● error")
	case string(apiruntime.StatusStopped):
		return styleStatusStopped.Render("● stopped")
	default:
		return styleDim.Render("● " + status)
	}
}
