package chat

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// infoMessage is a temporary message shown in the status bar.
type infoMessage struct {
	text    string
	expires time.Time
}

// infoExpiredMsg signals that the current info message has expired.
type infoExpiredMsg struct{}

// setInfo creates an info message with a TTL and returns a tea.Cmd that
// fires infoExpiredMsg when the TTL elapses.
func setInfo(m *chatModel, text string, ttl time.Duration) tea.Cmd {
	m.infoMessage = &infoMessage{
		text:    text,
		expires: time.Now().Add(ttl),
	}
	return tea.Tick(ttl, func(time.Time) tea.Msg {
		return infoExpiredMsg{}
	})
}
