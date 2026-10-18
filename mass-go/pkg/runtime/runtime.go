// Package runtime defines the experimental, provider-neutral runtime boundary.
// It does not promise parity with the production Rust runtime.
package runtime

import "context"

// Connection owns a provider process and supports one session. Close releases it
// even when no caller is consuming events.
type Connection interface {
	NewSession(context.Context) (Session, error)
	Close() error
}

type Session interface {
	Identity() Identity
	Capabilities() Capabilities
	Submit(context.Context, string) (Submission, error)
	// Cancel requests interruption. Its return is not completion evidence.
	Cancel(context.Context, string) error
	// Next has one consumer. Context cancellation only stops this wait.
	Next(context.Context) (Event, error)
	Close() error
}

type Identity struct {
	Provider, SessionID string
	Pending             bool
}
type Capabilities struct{ NativeTurnID, NativeAcknowledgement bool }
type Evidence string

const (
	SDKWriteSuccess    Evidence = "sdk_write_success"
	NativeAcknowledged Evidence = "native_acknowledged"
)

type Submission struct {
	TurnID   string
	Evidence Evidence
}
type EventKind string

const (
	SessionReady         EventKind = "session_ready"
	TurnStarted          EventKind = "turn_started"
	TurnCompleted        EventKind = "turn_completed"
	Message              EventKind = "message"
	Tool                 EventKind = "tool"
	InteractionRequested EventKind = "interaction_requested"
	Failure              EventKind = "failure"
	Unknown              EventKind = "unknown"
)

type Event struct {
	Kind                                          EventKind
	SessionID, TurnID, ItemID, Text, Tool, Status string
	Diagnostic                                    Diagnostic
	Interaction                                   *Interaction
}

// Diagnostic retains correlation without exposing arbitrary native payloads.
type Diagnostic struct{ NativeKind, SHA256 string }
type Interaction struct {
	Kind, Prompt string
	Questions    []Question
	Handle       ResponseHandle
}
type Question struct {
	ID, Prompt string
	Options    []string
}
type Response struct {
	Allow   bool
	Answers map[string][]string
}

// ResponseHandle is a connection-bound, one-shot capability. Copies refer to
// the same native request. A successful response write is not native acceptance.
type ResponseHandle interface {
	ID() string
	Respond(context.Context, Response) error
	Done() <-chan struct{}
	Err() error
}
