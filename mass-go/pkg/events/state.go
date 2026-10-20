package events

type SessionState string

const (
	Unknown        SessionState = "unknown"
	Idle           SessionState = "idle"
	Running        SessionState = "running"
	RequiresAction SessionState = "requires_action"
)
