package claude

import (
	"encoding/json"
	sdk "github.com/zoumo/agent-sdk-go/claude"
	"github.com/zoumo/agent-sdk-go/claude/types"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

func sdkSubject(s sdk.SubagentIdentity) *rt.EventSubject {
	p := &rt.EventSubject{SessionID: s.SessionID, AgentID: s.AgentID, RunID: s.RunID, TaskID: s.TaskID, ParentToolRef: s.ToolUseID, AssociationRef: s.AssociationID}
	if p.AssociationRef != "" {
		p.AssociationScope = "sdk_attachment"
	}
	return p
}
func derivedEvents(received sdk.ReceivedEvent) []rt.Event {
	var info sdk.SubagentInfo
	kind := rt.ObservationChanged
	switch n := received.(type) {
	case sdk.SubagentAssociated:
		info = n.SubagentInfo
		kind = rt.AgentAssociated
	case sdk.SubagentCreated:
		info = n.SubagentInfo
		kind = rt.AgentCreated
	case sdk.ObservationState:
		info = n.SubagentInfo
	default:
		return []rt.Event{{Kind: rt.Unknown, Diagnostic: diagnostic("sdk_derived/unsupported", nil), Origin: rt.EventOrigin{Kind: "sdk_derived", Attribution: "unresolved", Completeness: "unknown"}}}
	}
	attribution := info.Scope
	if attribution != "root" && attribution != "child" {
		attribution = "unresolved"
	}
	e := rt.Event{Kind: kind, Subject: sdkSubject(info.Source), Origin: rt.EventOrigin{Kind: "sdk_derived", Attribution: attribution, Completeness: "partial", SourceKind: "relationship"}}
	if info.RunScope == "native" {
		stale := info.Stale
		e.Subject.Stale = &stale
	}
	var parent *rt.EventSubject
	if info.Parent != nil {
		parent = sdkSubject(*info.Parent)
	}
	switch kind {
	case rt.AgentAssociated:
		state := "associated"
		if info.Reason == "identity-enriched" {
			state = "enriched"
		}
		if info.PreviousAssociationID != "" {
			state = "redirected"
		}
		e.Association = &rt.AgentAssociationPayload{State: state, Parent: parent, PreviousAssociationRef: info.PreviousAssociationID, Evidence: info.Evidence, Provenance: info.Provenance, Completeness: "partial"}
		if e.Association.Evidence == "" {
			e.Association.Evidence = info.Reason
		}
	case rt.AgentCreated:
		e.Creation = &rt.AgentCreatedPayload{Parent: parent, Evidence: info.Evidence, Provenance: info.Provenance}
	default:
		e.Observation = &rt.ObservationPayload{State: info.State, Reason: info.Reason, Completeness: "partial"}
	}
	events := []rt.Event{e}
	if info.RunStatus != "" || info.RunTerminal {
		state := "unknown"
		outcome := ""
		if info.RunTerminal {
			state = "terminal"
			outcome = rt.TaskState(info.RunStatus)
		} else if info.RunStatus == "running" || info.RunStatus == "waiting" {
			state = info.RunStatus
		}
		e.Kind = rt.AgentStateChanged
		e.Association = nil
		e.Creation = nil
		e.Observation = nil
		e.AgentState = &rt.AgentStatePayload{State: state, Terminal: info.RunTerminal, Outcome: outcome, Completeness: "partial"}
		events = append(events, e)
	}
	return events
}
func nativeSubject(n types.Event) *rt.EventSubject {
	var fields struct {
		RunID string `json:"run_id"`
	}
	_ = json.Unmarshal(n.Raw, &fields)
	p := &rt.EventSubject{SessionID: n.SessionID, RunID: fields.RunID}
	if n.AgentID != nil {
		p.AgentID = *n.AgentID
	}
	if n.ParentToolUseID != nil {
		p.ParentToolRef = *n.ParentToolUseID
	}
	if n.System != nil {
		p.TaskID = n.System.TaskID
		if n.System.ToolUseID != nil {
			p.ParentToolRef = *n.System.ToolUseID
		}
	}
	return p
}
func taskSource(p rt.EventSubject) string {
	// A task inventory is scoped by its native reporting session and agent, never
	// by whichever foreground operation happens to be active.
	b, _ := json.Marshal([]string{p.SessionID, p.AgentID, p.ParentToolRef})
	return string(b)
}
func taskPayload(raw json.RawMessage, source string) (rt.TaskPayload, error) {
	var n struct {
		TaskID      string  `json:"task_id"`
		RunID       string  `json:"run_id"`
		AgentID     string  `json:"agent_id"`
		ToolUseID   string  `json:"tool_use_id"`
		Status      string  `json:"status"`
		Background  *bool   `json:"is_backgrounded"`
		Description *string `json:"description"`
		Summary     *string `json:"summary"`
	}
	err := json.Unmarshal(raw, &n)
	if err != nil || n.TaskID == "" {
		return rt.TaskPayload{}, rt.ErrInvalidInput
	}
	p := rt.TaskPayload{TaskRef: n.TaskID, SourceScope: source, Subject: rt.EventSubject{TaskID: n.TaskID, RunID: n.RunID, AgentID: n.AgentID, ParentToolRef: n.ToolUseID}, State: rt.TaskState(n.Status), Background: rt.UnknownObservation(), Progress: n.Description, Result: n.Summary, Completeness: "partial"}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	_, p.StateObserved = fields["status"]
	if n.Background != nil {
		p.Background = rt.Known(*n.Background)
	}
	return p, nil
}
func taskEvents(n types.Event, base rt.Event) ([]rt.Event, bool) {
	if n.System == nil {
		return nil, false
	}
	sys := n.System
	fail := func() ([]rt.Event, bool) {
		base.Kind = rt.Failure
		base.Error = &rt.SafeError{Category: "decode_error", Scope: "session"}
		return []rt.Event{base}, true
	}
	if sys.Subtype == "session_state_changed" {
		state := "unknown"
		if sys.State != nil {
			switch *sys.State {
			case "running":
				state = "running"
			case "idle":
				state = "idle"
			case "requires_action":
				state = "waiting"
			}
		}
		base.Kind = rt.AgentStateChanged
		base.AgentState = &rt.AgentStatePayload{State: state, Completeness: "partial"}
		return []rt.Event{base}, true
	}
	if sys.Subtype == "background_tasks_changed" {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(n.Raw, &fields)
		raw, ok := fields["tasks"]
		if !ok {
			return []rt.Event{base}, true
		}
		if string(raw) == "null" || sys.Tasks == nil {
			return fail()
		}
		scope := taskSource(*base.Subject)
		p := &rt.TaskSnapshotPayload{Mode: "replace", SourceScope: scope, Tasks: []rt.TaskPayload{}, Completeness: "complete"}
		seen := map[string]bool{}
		for _, raw := range sys.Tasks {
			t, err := taskPayload(raw, scope)
			if err != nil {
				return fail()
			}
			keyBytes, _ := json.Marshal([]string{t.TaskRef, t.Subject.RunID})
			key := string(keyBytes)
			if seen[key] {
				return fail()
			}
			seen[key] = true
			t.Subject.SessionID = n.SessionID
			t.Background = rt.Known(true)
			p.Tasks = append(p.Tasks, t)
		}
		base.Kind = rt.TaskSnapshot
		base.TaskSnapshot = p
		return []rt.Event{base}, true
	}
	switch sys.Subtype {
	case "task_started", "task_updated", "task_progress", "task_notification":
	default:
		return nil, false
	}
	if sys.TaskID == "" {
		return fail()
	}
	// Task updates are partial source observations. Patch omission cannot clear
	// previously observed state. No run ID is borrowed from another observation.
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(n.Raw, &raw)
	if len(sys.Patch) > 0 && string(sys.Patch) != "null" {
		var patch map[string]json.RawMessage
		if json.Unmarshal(sys.Patch, &patch) != nil {
			return fail()
		}
		for _, k := range []string{"status", "is_backgrounded", "description", "summary"} {
			if v, ok := patch[k]; ok {
				raw[k] = v
			}
		}
	}
	if sys.Subtype == "task_started" {
		raw["status"] = json.RawMessage(`"running"`)
	}
	data, _ := json.Marshal(raw)
	sourceSubject := rt.EventSubject{SessionID: n.SessionID}
	scope := taskSource(sourceSubject)
	p, err := taskPayload(data, scope)
	if err != nil {
		return fail()
	}
	p.Subject.SessionID = n.SessionID
	base.Kind = rt.TaskChanged
	base.Task = &p
	// Native task ID is not an agent ID; the SDK relation stream carries its
	// independently proven association/enrichment and stale qualifiers.
	base.Subject = &p.Subject
	base.Origin.Attribution = "child"
	events := []rt.Event{base}
	return appendUsage(events, base, sys.Usage, "task"), true
}
func appendUsage(events []rt.Event, base rt.Event, raw json.RawMessage, scope string) []rt.Event {
	if len(raw) == 0 || string(raw) == "null" {
		return events
	}
	var counters map[string]json.RawMessage
	if json.Unmarshal(raw, &counters) != nil || counters == nil {
		return append(events, rt.Event{Kind: rt.Failure, Origin: base.Origin, Subject: base.Subject, Error: &rt.SafeError{Category: "decode_error", Scope: "session"}})
	}
	e := base
	e.Message = nil
	e.Work = nil
	e.Task = nil
	e.AgentState = nil
	e.Kind = rt.UsageUpdated
	e.Usage = &rt.UsagePayload{Counters: counters, Mode: "unknown", AccountingScope: scope, ChildInclusion: "unknown"}
	return append(events, e)
}
