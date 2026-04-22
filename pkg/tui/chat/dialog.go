package chat

import (
	tea "charm.land/bubbletea/v2"
)

// Action represents an action returned by a dialog after handling a message.
type Action any

// ActionClose signals that the front dialog should be closed.
type ActionClose struct{}

// ActionRunCommand signals that a slash command should be executed.
type ActionRunCommand struct {
	Command *Command
	Args    string
}

// ActionCmd carries a tea.Cmd that should be batched into the update loop.
type ActionCmd struct{ Cmd tea.Cmd }

// ActionRunAgentCommand signals that a dynamic agent command should be executed.
type ActionRunAgentCommand struct{ Name string }

// Dialog is a component that can be displayed on top of the chat UI.
type Dialog interface {
	ID() string
	HandleMsg(msg tea.Msg) Action
	View(width, height int) string
}

// Overlay manages a stack of dialogs rendered over the main UI.
type Overlay struct {
	dialogs []Dialog
}

// NewOverlay creates a new empty Overlay.
func NewOverlay() *Overlay {
	return &Overlay{}
}

// HasDialogs returns true if there are any active dialogs.
func (o *Overlay) HasDialogs() bool {
	return len(o.dialogs) > 0
}

// OpenDialog pushes a dialog onto the stack.
func (o *Overlay) OpenDialog(d Dialog) {
	o.dialogs = append(o.dialogs, d)
}

// CloseDialog removes the dialog with the given ID.
func (o *Overlay) CloseDialog(id string) {
	for i, d := range o.dialogs {
		if d.ID() == id {
			o.dialogs = append(o.dialogs[:i], o.dialogs[i+1:]...)
			return
		}
	}
}

// CloseFrontDialog removes the top dialog.
func (o *Overlay) CloseFrontDialog() {
	if len(o.dialogs) == 0 {
		return
	}
	o.dialogs = o.dialogs[:len(o.dialogs)-1]
}

// Front returns the top dialog, or nil if empty.
func (o *Overlay) Front() Dialog {
	if len(o.dialogs) == 0 {
		return nil
	}
	return o.dialogs[len(o.dialogs)-1]
}

// ContainsDialog checks if a dialog with the given ID exists in the stack.
func (o *Overlay) ContainsDialog(id string) bool {
	for _, d := range o.dialogs {
		if d.ID() == id {
			return true
		}
	}
	return false
}

// HandleMsg routes a message to the front dialog and returns its action.
func (o *Overlay) HandleMsg(msg tea.Msg) Action {
	if len(o.dialogs) == 0 {
		return nil
	}
	return o.dialogs[len(o.dialogs)-1].HandleMsg(msg)
}
