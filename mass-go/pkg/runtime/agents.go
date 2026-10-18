package runtime

import "encoding/json"

// EventSubject identifies the observed source, independently of the owning session.
// AssociationRef is SDK-local and meaningful only within this event's attachment.
// A nil Stale means that the source did not supply a stale/current classification.
type EventSubject struct {
	SessionID        string `json:"sessionId,omitempty"`
	AgentID          string `json:"agentId,omitempty"`
	RunID            string `json:"runId,omitempty"`
	TaskID           string `json:"taskId,omitempty"`
	ParentToolRef    string `json:"parentToolRef,omitempty"`
	AssociationRef   string `json:"associationRef,omitempty"`
	AssociationScope string `json:"associationScope,omitempty"`
	Stale            *bool  `json:"stale,omitempty"`
}

type AgentAssociationPayload struct {
	State                  string        `json:"state"`
	Parent                 *EventSubject `json:"parent,omitempty"`
	PreviousAssociationRef string        `json:"previousAssociationRef,omitempty"`
	Evidence               string        `json:"evidence"`
	Provenance             string        `json:"provenance"`
	Completeness           string        `json:"completeness"`
}
type AgentCreatedPayload struct {
	Parent     *EventSubject `json:"parent,omitempty"`
	Evidence   string        `json:"evidence"`
	Provenance string        `json:"provenance"`
}

// Terminal describes a run observation. It does not delete an agent entity.
type AgentStatePayload struct {
	State        string `json:"state"`
	Outcome      string `json:"outcome,omitempty"`
	Terminal     bool   `json:"terminal"`
	Completeness string `json:"completeness"`
}
type TaskPayload struct {
	TaskRef       string       `json:"taskRef"`
	SourceScope   string       `json:"sourceScope"`
	Subject       EventSubject `json:"subject"`
	Background    Observation  `json:"background"`
	State         string       `json:"state"`
	StateObserved bool         `json:"stateObserved"`
	Progress      *string      `json:"progress,omitempty"`
	Result        *string      `json:"result,omitempty"`
	Completeness  string       `json:"completeness"`
}
type TaskSnapshotPayload struct {
	Mode         string        `json:"mode"`
	SourceScope  string        `json:"sourceScope"`
	Tasks        []TaskPayload `json:"tasks"`
	Completeness string        `json:"completeness"`
}

// Usage observations are never summed by the Runtime. Counters preserve numeric
// JSON (including nested counter groups) without float64 conversion.
type UsagePayload struct {
	Counters        map[string]json.RawMessage `json:"counters"`
	Mode            string                     `json:"mode"`
	AccountingScope string                     `json:"accountingScope"`
	ChildInclusion  string                     `json:"childInclusion"`
}
type ObservationPayload struct {
	State        string `json:"state"`
	Reason       string `json:"reason,omitempty"`
	Completeness string `json:"completeness"`
}

func TaskState(native string) string {
	switch native {
	case "pending", "running", "waiting", "completed", "failed", "cancelled":
		return native
	case "killed", "interrupted":
		return "cancelled"
	default:
		return "unknown"
	}
}
