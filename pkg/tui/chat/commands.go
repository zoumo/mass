package chat

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// CommandCategory classifies a command for display and dispatch.
type CommandCategory string

const (
	CmdCategorySystem  CommandCategory = "system"
	CmdCategorySession CommandCategory = "session"
)

// Command defines a slash command available in the chat TUI.
type Command struct {
	Name        string
	Aliases     []string
	Description string
	Category    CommandCategory
	Handler     func(m *chatModel, args string) []tea.Cmd
	// ArgCompleter, if non-nil, is called when the user has typed the full
	// command name followed by a space and is now entering arguments.
	// It returns completion entries for the current argument prefix.
	// Returning nil or an empty slice disables arg completion for this command.
	ArgCompleter func(m *chatModel, prefix string) []completionEntry
}

// commandRegistry is the ordered list of built-in slash commands.
// Populated in init() to avoid an initialization cycle with handler functions.
var commandRegistry []Command

func init() {
	commandRegistry = []Command{
		{Name: "help", Category: CmdCategorySystem, Description: "Show available commands", Handler: cmdHelp},
		{Name: "clear", Category: CmdCategorySystem, Description: "Clear chat messages", Handler: cmdClear},
		{Name: "cancel", Category: CmdCategorySystem, Description: "Cancel the current turn", Handler: cmdCancel},
		{Name: "exit", Category: CmdCategorySystem, Aliases: []string{"quit", "q"}, Description: "Exit chat", Handler: cmdExit},
		{Name: "model", Category: CmdCategorySession, Description: "Show or switch the current model", Handler: cmdModel, ArgCompleter: modelArgCompleter},
		{Name: "status", Category: CmdCategorySession, Description: "Show agent status", Handler: cmdStatus},
	}
}

// parseSlashCommand checks if text starts with "/" and returns the matching
// command, remaining args, and whether a match was found.
func parseSlashCommand(text string) (cmd *Command, args string, found bool) {
	if !strings.HasPrefix(text, "/") {
		return nil, "", false
	}
	rest := text[1:]
	name, args, _ := strings.Cut(rest, " ")
	name = strings.ToLower(name)
	args = strings.TrimSpace(args)
	cmd = lookupCommand(name)
	if cmd == nil {
		return nil, "", false
	}
	return cmd, args, true
}

// lookupCommand searches the registry by name and aliases.
func lookupCommand(name string) *Command {
	for i := range commandRegistry {
		c := &commandRegistry[i]
		if c.Name == name {
			return c
		}
		for _, alias := range c.Aliases {
			if alias == name {
				return c
			}
		}
	}
	return nil
}
