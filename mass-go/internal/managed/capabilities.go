package managed

import rt "github.com/zoumo/mass/mass-go/pkg/runtime"

// Features is the fixed local protocol declaration used by the assembly catalog.
// Conditions describe admission checks; a native reply may still be unverified.
func Features() []rt.FeatureDefinition {
	return []rt.FeatureDefinition{
		{Name: "managed_queries", Support: "supported", Conditions: []string{}},
		{Name: "runtime_events", Support: "supported", Conditions: []string{"single_consumer", "retention_available"}},
		{Name: "create_session", Support: "supported", Conditions: []string{"runtime_ready", "ownership_capacity", "explicit_initial_input_when_required"}},
		{Name: "resume_session", Support: "supported", Conditions: []string{"runtime_ready", "host_recovery_context", "original_configuration_scope", "detached_attachment", "native_identity_verification"}},
		{Name: "close_session", Support: "supported", Conditions: []string{"managed_session", "original_attachment"}},
		{Name: "session_events", Support: "supported", Conditions: []string{"managed_session", "single_consumer"}},
		{Name: "text_input", Support: "supported", Conditions: []string{"attached", "foreground_settled", "control_capacity"}},
		{Name: "cancel", Support: "supported", Conditions: []string{"attached", "original_unresolved_submission", "unused_cancellation_attempt"}},
		{Name: "respond", Support: "supported", Conditions: []string{"attached", "live_original_interaction", "unused_response_attempt"}},
		{Name: "native_release", Support: "unsupported", Conditions: []string{}, Reason: "native_release_unproven"},
		{Name: "remote_transport", Support: "unsupported", Conditions: []string{}, Reason: "transport_not_implemented"},
		{Name: "native_discovery", Support: "unsupported", Conditions: []string{}, Reason: "discovery_not_implemented"},
		{Name: "rich_input", Support: "unsupported", Conditions: []string{}, Reason: "text_only_input"},
		{Name: "session_config", Support: "unsupported", Conditions: []string{}, Reason: "configuration_mutation_not_implemented"},
	}
}
func availability(reason func(string) string) []rt.CapabilityAvailability {
	features := Features()
	result := make([]rt.CapabilityAvailability, 0, len(features))
	for _, definition := range features {
		feature := rt.CapabilityAvailability{Name: definition.Name, Support: definition.Support, Conditions: definition.Conditions, Availability: "available"}
		if definition.Support == "unsupported" {
			feature.Reason = definition.Reason
		} else {
			feature.Reason = reason(definition.Name)
		}
		if feature.Reason != "" {
			feature.Availability = "unavailable"
		}
		result = append(result, feature)
	}
	return result
}
func (r *Runtime) capabilitiesLocked() []rt.CapabilityAvailability {
	return availability(func(name string) string {
		switch name {
		case "managed_queries":
			return ""
		case "runtime_events":
			if r.lifecycle.failed() {
				return "lifecycle_incomplete"
			}
			return ""
		}
		if r.retired {
			return "registration_retired"
		}
		if r.snapshot.Phase != "ready" {
			return "runtime_not_ready"
		}
		if r.lifecycle.failed() {
			return "lifecycle_incomplete"
		}
		switch name {
		case "create_session":
			if len(r.operations) >= 256 || r.attachments >= 256 {
				return "ownership_capacity"
			}
		case "resume_session":
			if r.bindings == nil {
				return "recovery_context_required"
			}
			if r.attachments >= 256 || len(r.operations) >= 256 {
				return "ownership_capacity"
			}
		default:
			return "session_required"
		}
		return ""
	})
}
func (s *publishedSession) capabilitiesLocked() []rt.CapabilityAvailability {
	return availability(func(name string) string {
		switch name {
		case "managed_queries", "session_events":
			return ""
		case "runtime_events":
			if s.lifecycle != nil && s.lifecycle.failed() {
				return "lifecycle_incomplete"
			}
			return ""
		case "create_session":
			return "runtime_required"
		case "resume_session":
			if s.lifecycle != nil && s.lifecycle.failed() {
				return "lifecycle_incomplete"
			}
			if s.lifecycle != nil && !s.lifecycle.accepting() {
				return "runtime_not_ready"
			}
			if !s.bindingAvailable {
				return "recovery_context_required"
			}
			if s.releaseUnknown {
				return "release_unconfirmed"
			}
			if !s.closed {
				return "attachment_attached"
			}
			select {
			case <-s.closeFinished:
				return ""
			default:
				return "attachment_closing"
			}
		}
		if s.closed {
			return "attachment_closed"
		}
		if s.terminal != nil {
			return "attachment_unavailable"
		}
		if (name == "text_input" || name == "respond") && s.lifecycle != nil && !s.lifecycle.failed() && !s.lifecycle.accepting() {
			return "runtime_not_ready"
		}
		if (name == "text_input" || name == "respond") && s.lifecycle != nil && s.lifecycle.failed() {
			return "lifecycle_incomplete"
		}
		switch name {
		case "text_input":
			if s.active != nil {
				return "foreground_unresolved"
			}
			if len(s.commands) >= maxControlRecords {
				return "control_capacity"
			}
		case "cancel":
			if s.active == nil || s.active.finished {
				return "submission_required"
			}
			if s.active.cancel != nil {
				return "cancellation_attempt_consumed"
			}
		case "respond":
			if s.native.Snapshot().Interactions.Pending == 0 {
				return "interaction_required"
			}
		}
		return ""
	})
}
