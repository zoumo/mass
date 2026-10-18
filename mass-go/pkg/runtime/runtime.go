// Package runtime defines the experimental, provider-neutral runtime boundary.
// It does not promise parity with the production Rust runtime.
package runtime

import (
	"context"
	"errors"
	"time"
)

// Runtime owns one named protocol instance and all of its native sessions.
type Runtime interface {
	Start(context.Context) error
	Status() Snapshot
	CreateSession(context.Context, SessionOptions) (CreationOperation, error)
	GetOperation(string) (CreationOperation, error)
	ContinueCreation(context.Context, string, string) (CreationOperation, error)
	CancelOperation(context.Context, string) (CreationOperation, error)
	ResumeSession(context.Context, SessionKey) (Session, error)
	CloseSession(context.Context, SessionKey, CloseGoal) (CloseReceipt, error)
	GetSession(SessionKey) (Session, error)
	ListSessions() []SessionSnapshot
	Respond(context.Context, SessionKey, string, Response) (ResponseReceipt, error)
	Stop() error
}

var (
	ErrIdentityMismatch   = errors.New("native session identity mismatch")
	ErrUnverifiable       = errors.New("native session recovery unverifiable")
	ErrInvalidInteraction = errors.New("interaction authority is invalid")
	ErrBusy               = errors.New("foreground submission unresolved")
	ErrBackpressure       = errors.New("runtime control record bound reached")
	ErrUnsupported        = errors.New("unsupported runtime capability")
	ErrCleanupIncomplete  = errors.New("owned creation cleanup unconfirmed")
	ErrOutcomeUnknown     = errors.New("native operation outcome unknown")
	ErrIncomplete         = errors.New("incomplete provisional observations")
	ErrConflict           = errors.New("runtime ownership conflict")
	ErrNotFound           = errors.New("runtime resource not found")
	ErrUnavailable        = errors.New("runtime unavailable")
	ErrInvalidInput       = errors.New("invalid runtime configuration or session key")
)

// CreationLimits bound adapter-owned provisional facts and the native open wait.
// Zero selects finite defaults, never unlimited storage or time.
type CreationLimits struct {
	Facts   int           `json:"facts,omitempty"`
	Bytes   int           `json:"bytes,omitempty"`
	Timeout time.Duration `json:"timeout,omitempty"`
}

func (l CreationLimits) Effective() CreationLimits {
	if l.Facts == 0 {
		l.Facts = 64
	}
	if l.Bytes == 0 {
		l.Bytes = 1 << 20
	}
	if l.Timeout == 0 {
		l.Timeout = 30 * time.Second
	}
	return l
}

type SessionOptions struct {
	InitialInput *string `json:"initialInput,omitempty"`
}

// CreationEvidence reports observations, not a promise of native persistence.
type CreationEvidence struct {
	NativeIdentity      Observation `json:"nativeIdentity"`
	InitialInputWritten Observation `json:"initialInputWritten"`
}
type CreationOperation struct {
	Evidence      CreationEvidence `json:"evidence"`
	OperationRef  string           `json:"operationRef"`
	RuntimeName   string           `json:"runtimeName"`
	Phase         string           `json:"phase"`
	InputRequired bool             `json:"inputRequired"`
	Key           *SessionKey      `json:"key,omitempty"`
	CleanupState  string           `json:"cleanupState"`
	Failure       string           `json:"failure,omitempty"`
}

type Config struct {
	CreationLimits CreationLimits `json:"creationLimits,omitempty"`
	Executable     string         `json:"executable"`
	Env            []string       `json:"-"` // Explicit child environment; never returned in status.
	Dir            string         `json:"dir,omitempty"`
}
type Registration struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Config Config `json:"config"`
}

// RuntimeType describes a fixed system implementation and its configuration.
type RuntimeType struct {
	Name           string        `json:"name"`
	SchemaVersion  int           `json:"schemaVersion"`
	Topology       string        `json:"topology"`
	MaxNameBytes   int           `json:"maxNameBytes"`
	ConfigFields   []ConfigField `json:"configFields"`
	CreationBounds []LimitField  `json:"creationBounds"`
}
type LimitField struct {
	Name    string `json:"name"`
	Default int64  `json:"default"`
	Min     int64  `json:"min"`
	Max     int64  `json:"max"`
}
type ConfigField struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	Required     bool   `json:"required"`
	AbsolutePath bool   `json:"absolutePath,omitempty"`
}
type Observation struct {
	State string `json:"state"`
	Value *bool  `json:"value,omitempty"`
}

func Known(value bool) Observation    { return Observation{State: "known", Value: &value} }
func UnknownObservation() Observation { return Observation{State: "unknown"} }

type Health struct {
	Configured         Observation `json:"configured"`
	Connected          Observation `json:"connected"`
	ProtocolResponsive Observation `json:"protocolResponsive"`
	ProbeScope         string      `json:"probeScope"`
	ObservedAt         time.Time   `json:"observedAt"`
}
type Snapshot struct {
	Name             string    `json:"name"`
	Type             string    `json:"type"`
	ConfigRevision   uint64    `json:"configRevision"`
	ObservedRevision uint64    `json:"observedRevision"`
	Phase            string    `json:"phase"`
	Health           Health    `json:"health"`
	ObservedAt       time.Time `json:"observedAt"`
}
type SessionKey struct {
	RuntimeName string `json:"runtimeName"`
	SessionID   string `json:"sessionId"`
}
type SessionSnapshot struct {
	Key             SessionKey         `json:"key"`
	Freshness       string             `json:"freshness"`
	Availability    string             `json:"availability"`
	ConnectionEpoch string             `json:"connectionEpoch,omitempty"`
	Foreground      ForegroundState    `json:"foreground"`
	Interactions    InteractionSummary `json:"interactions"`
	Background      BackgroundSummary  `json:"background"`
	Metadata        MetadataPayload    `json:"metadata"`
	Attachment      string             `json:"attachment"`
	ObservedAt      time.Time          `json:"observedAt"`
}

// Session is an attachment with a proven, immutable native identity.
type Session interface {
	Key() SessionKey
	Snapshot() SessionSnapshot
	Capabilities() Capabilities
	Submit(context.Context, InputCommand) (Submission, error)
	// Cancel requests interruption. Its return is not completion evidence.
	Cancel(context.Context, string) (Cancellation, error)
	GetSubmission(string) (Submission, error)
	GetCancellation(string) (Cancellation, error)
	// Next has one consumer. Context cancellation only stops this wait.
	Next(context.Context) (Event, error)
	Close() error
	// Respond resolves original authority on this attachment. Event replay creates none.
	Respond(context.Context, string, Response) (ResponseReceipt, error)
}

type Capabilities struct{ NativeTurnID, NativeAcknowledgement, VerifiedResume, NativeRelease bool }
type EventKind string

const (
	SessionReady         EventKind = "session_ready"
	TurnStarted          EventKind = "turn_started"
	TurnCompleted        EventKind = "turn_completed"
	MessageCompleted     EventKind = "message.completed"
	MessageStarted       EventKind = "message.started"
	MessageDelta         EventKind = "message.delta"
	ToolAnnounced        EventKind = "tool.announced"
	ToolUpdated          EventKind = "tool.updated"
	ToolCompleted        EventKind = "tool.completed"
	InteractionRequested EventKind = "interaction.requested"
	InteractionResolved  EventKind = "interaction.resolved"
	Failure              EventKind = "failure"
	Unknown              EventKind = "diagnostic"
	ReasoningCompleted   EventKind = "reasoning.completed"
	ReasoningDelta       EventKind = "reasoning.delta"
	Plan                 EventKind = "plan.updated"
	Metadata             EventKind = "session.metadata_changed"
)

// Diagnostic retains correlation without exposing arbitrary native payloads.
type Diagnostic struct {
	Classification string `json:"classification,omitempty"`
	CallRef        string `json:"callRef,omitempty"`
	NativeKind     string `json:"sourceKind"`
	SHA256         string `json:"sha256"`
}
