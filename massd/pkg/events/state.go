package events

import "github.com/zoumo/agent-sdk-go/agent"

type SessionState = agent.SessionState

const (
	Unknown        SessionState = "unknown"
	Idle                        = agent.StateIdle
	Running                     = agent.StateRunning
	RequiresAction              = agent.StateRequiresAction
)
