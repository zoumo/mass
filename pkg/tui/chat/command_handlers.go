package chat

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	runapi "github.com/zoumo/mass/pkg/agentrun/api"
	runclient "github.com/zoumo/mass/pkg/agentrun/client"
	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
	"github.com/zoumo/mass/pkg/tui/component"
)

// agentCommand is an internal representation of a dynamic command from the agent runtime.
type agentCommand struct {
	Name        string
	Description string
}

func cmdHelp(m *chatModel, _ string) []tea.Cmd {
	var sb strings.Builder
	sb.WriteString("Available commands:\n")
	for _, c := range commandRegistry {
		line := fmt.Sprintf("  /%s", c.Name)
		if len(c.Aliases) > 0 {
			line += fmt.Sprintf(" (%s)", strings.Join(prefixAll(c.Aliases, "/"), ", "))
		}
		line += fmt.Sprintf(" [%s] — %s", c.Category, c.Description)
		sb.WriteString(line + "\n")
	}
	if len(m.agentCommands) > 0 {
		sb.WriteString("\nAgent commands:\n")
		for _, ac := range m.agentCommands {
			fmt.Fprintf(&sb, "  /%s — %s\n", ac.Name, ac.Description)
		}
	}
	m.chat.AppendMessages(component.NewSystemItem(m.nextID("sys"), sb.String(), styleDim))
	return nil
}

func cmdClear(m *chatModel, _ string) []tea.Cmd {
	m.chat.ClearMessages()
	return []tea.Cmd{setInfo(m, "Chat cleared", 3*time.Second)}
}

func cmdStatus(m *chatModel, _ string) []tea.Cmd {
	status := m.agentStatus
	if status == "" {
		status = "unknown"
	}
	model := m.currentModel
	if model == "" {
		model = "unknown"
	}
	msg := fmt.Sprintf("Agent status: %s  model: %s", status, model)
	m.chat.AppendMessages(component.NewSystemItem(m.nextID("sys"), msg, styleDim))
	return nil
}

func cmdCancel(m *chatModel, _ string) []tea.Cmd {
	if !m.waiting || m.client == nil {
		m.chat.AppendMessages(component.NewSystemItem(m.nextID("sys"), "Nothing to cancel", styleDim))
		return nil
	}
	m.chat.AppendMessages(component.NewSystemItem(m.nextID("sys"), "[canceling…]", styleDim))
	return []tea.Cmd{cancelPromptCmd(m.client)}
}

func cmdExit(m *chatModel, _ string) []tea.Cmd {
	m.cleanup()
	return []tea.Cmd{tea.Quit}
}

func cmdModel(m *chatModel, args string) []tea.Cmd {
	if args == "" {
		// Show current model and available options.
		cur := m.currentModel
		if cur == "" {
			cur = "unknown"
		}
		msg := fmt.Sprintf("Current model: %s", cur)
		m.chat.AppendMessages(component.NewSystemItem(m.nextID("sys"), msg, styleDim))
		return nil
	}
	if m.client == nil {
		m.chat.AppendMessages(component.NewSystemItem(m.nextID("sys"), "Not connected", styleDim))
		return nil
	}
	modelID := strings.TrimSpace(args)
	return []tea.Cmd{setModelCmd(m.client, modelID)}
}

func setModelCmd(sc *runclient.Client, modelID string) tea.Cmd {
	return safeCmd(func() tea.Msg {
		_, err := sc.SetModel(context.Background(), &runapi.SessionSetModelParams{ModelID: modelID})
		if err != nil {
			return promptErrMsg{err: fmt.Errorf("set model: %w", err)}
		}
		return setModelResult{modelID: modelID}
	})
}

func prefixAll(ss []string, prefix string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = prefix + s
	}
	return out
}

// matchAgentCommand checks if text is a slash-prefixed agent command.
func (m *chatModel) matchAgentCommand(text string) (name string, found bool) {
	if !strings.HasPrefix(text, "/") {
		return "", false
	}
	cmdName, _, _ := strings.Cut(text[1:], " ")
	for _, ac := range m.agentCommands {
		if strings.EqualFold(ac.Name, cmdName) {
			return ac.Name, true
		}
	}
	return "", false
}

// cmdAgentCommand sends an agent command (with optional args) as a prompt.
func cmdAgentCommand(m *chatModel, text string) []tea.Cmd {
	if m.client == nil {
		m.chat.AppendMessages(component.NewSystemItem(m.nextID("sys"), "Not connected", styleDim))
		return nil
	}
	return []tea.Cmd{sendPromptCmd(m.client, text)}
}

// modelArgCompleter returns completion entries for the /model command's argument,
// populated from the chatModel's cached available-models list.
func modelArgCompleter(m *chatModel, _ string) []completionEntry {
	entries := make([]completionEntry, 0, len(m.availableModels))
	for _, mi := range m.availableModels {
		desc := mi.Name
		if mi.Description != nil && *mi.Description != "" {
			desc = mi.Name + " — " + *mi.Description
		}
		entries = append(entries, completionEntry{
			Name:        mi.ModelId,
			Description: desc,
			Category:    categorySession,
			IsArg:       true,
		})
	}
	return entries
}

// updateAgentCommands converts runtime AvailableCommand types to the internal
// agentCommand representation.
func (m *chatModel) updateAgentCommands(cmds any) {
	switch v := cmds.(type) {
	case []apiruntime.AvailableCommand:
		m.agentCommands = make([]agentCommand, len(v))
		for i, c := range v {
			m.agentCommands[i] = agentCommand{Name: c.Name, Description: singleLine(c.Description)}
		}
	case []runapi.AvailableCommand:
		m.agentCommands = make([]agentCommand, len(v))
		for i, c := range v {
			m.agentCommands[i] = agentCommand{Name: c.Name, Description: singleLine(c.Description)}
		}
	}
}

// singleLine collapses all whitespace runs (including newlines) into a single space.
func singleLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
