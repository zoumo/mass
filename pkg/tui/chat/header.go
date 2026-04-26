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
//	MASS > workspace > agent > model                    ● status seq:N
func renderHeader(workspaceName, agentName, agentStatus, currentModel string, maxSeq, width int) string {
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

	right := renderHeaderStatusWithSeq(agentStatus, maxSeq)
	leftWidth := lipgloss.Width(left)
	rightWidth := lipgloss.Width(right)
	gap := strings.Repeat(" ", max(0, width-leftWidth-rightWidth))

	return left + gap + right
}

// renderHeaderStatusWithSeq renders the status indicator with event sequence number.
// Format: "● status seq:N"
func renderHeaderStatusWithSeq(status string, seq int) string {
	if status == "" {
		status = "unknown"
	}
	seqText := headerItemStyle.Render(" seq:" + itoa(seq))
	switch status {
	case string(apiruntime.PhaseRunning):
		return styleStatusRunning.Render("● running") + seqText
	case string(apiruntime.PhaseRestarting):
		return styleStatusRunning.Render("● restarting") + seqText
	case string(apiruntime.PhaseIdle):
		return styleStatusIdle.Render("● idle") + seqText
	case string(apiruntime.PhaseError):
		return styleStatusError.Render("● error") + seqText
	case string(apiruntime.PhaseStopped):
		return styleStatusStopped.Render("● stopped") + seqText
	default:
		return styleDim.Render("● "+status) + seqText
	}
}

// itoa converts int to string without importing strconv (tiny helper).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf []byte
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	for n > 0 {
		buf = append(buf, byte('0'+n%10))
		n /= 10
	}
	if neg {
		buf = append(buf, '-')
	}
	// Reverse buf
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}
