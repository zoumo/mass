package runtime

import "context"

// RecoveryBinding is private adapter context retained only by an authorized host.
// It is never included in session snapshots or events. Treat Data as opaque.
type RecoveryBinding struct {
	Version int    `json:"version"`
	Data    []byte `json:"data"`
}

// BindingStore is the host's retention port. Implementations must copy bytes and
// honor cancellation. Durable storage and cross-host trust are host concerns.
type BindingStore interface {
	Save(context.Context, SessionKey, RecoveryBinding) error
	Load(context.Context, SessionKey) (RecoveryBinding, error)
}

type CloseGoal string

const (
	CloseDetach  CloseGoal = "local_detach"
	CloseRelease CloseGoal = "native_release"
)

type CloseReceipt struct {
	Key                   SessionKey  `json:"key"`
	LocalAttachmentClosed Observation `json:"localAttachmentClosed"`
	NativeRelease         string      `json:"nativeRelease"`
	OwnedProcessReaped    Observation `json:"ownedProcessReaped"`
	IncompleteFacts       bool        `json:"incompleteFacts"`
	Outcome               string      `json:"outcome"`
}
