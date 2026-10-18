package runtime

import "fmt"

func (e Event) validateLifecycleScope() error {
	switch e.Scope {
	case "session":
		if e.SessionID == "" || e.ConnectionEpoch == "" {
			return fmt.Errorf("invalid session envelope")
		}
		switch e.Kind {
		case RuntimeStatusChanged, OperationStateChanged, OperationFailed, SessionOpened, SessionResumed, SessionStateChanged:
			return fmt.Errorf("lifecycle event requires Runtime stream scope")
		}
	case "runtime":
		if e.SessionID != "" || e.ConnectionEpoch != "" || e.Origin.Kind != "runtime" {
			return fmt.Errorf("invalid runtime envelope")
		}
		switch e.Kind {
		case RuntimeStatusChanged:
			if e.Runtime == nil || e.Runtime.Name != e.RuntimeName || e.Runtime.Type == "" || !oneOf(e.Runtime.Phase, "registered", "starting", "ready", "stopping", "stopped", "unavailable") {
				return fmt.Errorf("invalid runtime snapshot")
			}
			return validateCapabilities(e.Runtime.Capabilities)
		case SessionOpened, SessionResumed, SessionStateChanged:
			if e.Session == nil || e.Session.Key.RuntimeName != e.RuntimeName || e.Session.Key.SessionID == "" || e.Session.ConnectionEpoch == "" {
				return fmt.Errorf("invalid lifecycle session identity")
			}
			return validateCapabilities(e.Session.Capabilities)
		default:
			return fmt.Errorf("event is not a runtime lifecycle observation")
		}
	case "operation":
		if e.OperationRef == "" || e.SessionID != "" || e.ConnectionEpoch != "" || e.Origin.Kind != "runtime" {
			return fmt.Errorf("invalid operation envelope")
		}
		switch e.Kind {
		case OperationStateChanged:
			if e.Operation == nil {
				return fmt.Errorf("missing operation payload")
			}
			return e.Operation.validate(e.OperationRef, e.RuntimeName)
		case OperationFailed:
			if e.Error == nil || e.Error.Scope != "operation" || e.Error.OperationRef != e.OperationRef || e.Error.Category == "" {
				return fmt.Errorf("invalid operation failure")
			}
		default:
			return fmt.Errorf("event is not an operation lifecycle observation")
		}
	default:
		return fmt.Errorf("invalid event scope")
	}
	return nil
}

func validateCapabilities(features []CapabilityAvailability) error {
	if len(features) == 0 {
		return fmt.Errorf("missing capability records")
	}
	seen := map[string]bool{}
	for _, feature := range features {
		if feature.Name == "" || seen[feature.Name] || !oneOf(feature.Support, "supported", "unsupported") || !oneOf(feature.Availability, "available", "unavailable") || (feature.Availability == "unavailable" && feature.Reason == "") || (feature.Support == "unsupported" && feature.Availability != "unavailable") {
			return fmt.Errorf("invalid capability availability")
		}
		seen[feature.Name] = true
	}
	return nil
}

// OperationPayload preserves distinct control receipts. It never carries input
// text, answers, an SDK handle or a fabricated native session/work identifier.
type OperationPayload struct {
	Kind         string             `json:"kind"`
	State        string             `json:"state"`
	Creation     *CreationOperation `json:"creation,omitempty"`
	Submission   *Submission        `json:"submission,omitempty"`
	Cancellation *Cancellation      `json:"cancellation,omitempty"`
	Response     *ResponseReceipt   `json:"response,omitempty"`
}

func (p OperationPayload) validate(ref, name string) error {
	count := 0
	if p.Creation != nil {
		count++
	}
	if p.Submission != nil {
		count++
	}
	if p.Cancellation != nil {
		count++
	}
	if p.Response != nil {
		count++
	}
	if count != 1 || p.State == "" {
		return fmt.Errorf("invalid operation payload")
	}
	var key *SessionKey
	var original string
	switch p.Kind {
	case "creation", "resume":
		if p.Creation == nil || p.Creation.RuntimeName != name || p.Creation.Phase != p.State {
			return fmt.Errorf("invalid creation payload")
		}
		original = p.Creation.OperationRef
		key = p.Creation.Key
		if p.State == "opened" && key == nil {
			return fmt.Errorf("opened operation requires native identity")
		}
		if !oneOf(p.State, "preparing", "awaiting_input", "awaiting_native_identity", "opened", "cleaning_up", "closed", "outcome_unknown", "rejected") {
			return fmt.Errorf("invalid creation phase")
		}
	case "submission":
		if p.Submission == nil {
			return fmt.Errorf("missing submission")
		}
		original = p.Submission.OperationRef
		key = &p.Submission.Key
	case "cancellation":
		if p.Cancellation == nil {
			return fmt.Errorf("missing cancellation")
		}
		original = p.Cancellation.OperationRef
		key = &p.Cancellation.Key
	case "response":
		if p.Response == nil {
			return fmt.Errorf("missing response")
		}
		original = p.Response.OperationRef
		key = &p.Response.Key
	default:
		return fmt.Errorf("invalid operation kind")
	}
	if original != ref {
		return fmt.Errorf("operation reference mismatch")
	}
	if key != nil && (key.RuntimeName != name || key.SessionID == "") {
		return fmt.Errorf("invalid operation session key")
	}
	return nil
}
