package codex

import (
	"encoding/json"
	"github.com/zoumo/agent-sdk-go/codex/types"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

func derivedEvent(native types.Event) (rt.Event, bool) {
	e := rt.Event{Origin: rt.EventOrigin{Kind: "sdk_derived", Attribution: "child", Completeness: "partial", SourceKind: "relationship"}}
	var child, parent string
	var proof *types.SubagentEvidence
	switch n := native.Data.(type) {
	case *types.SubagentAssociated:
		child, parent, proof = n.ThreadID, n.ParentThreadID, n.Evidence
		e.Kind = rt.AgentAssociated
	case *types.SubagentCreated:
		child, parent, proof = n.ThreadID, n.ParentThreadID, n.Evidence
		e.Kind = rt.AgentCreated
	case *types.ObservationState:
		e.Kind = rt.ObservationChanged
		e.Origin.Attribution = "unresolved"
		e.Subject = &rt.EventSubject{AgentID: n.UnresolvedThreadID, SessionID: n.UnresolvedThreadID}
		e.Observation = &rt.ObservationPayload{State: n.State, Reason: n.Reason, Completeness: "partial"}
		return e, true
	default:
		return rt.Event{}, false
	}
	e.Subject = &rt.EventSubject{AgentID: child, SessionID: child}
	provenance, evidence := "unknown", "sdk_relationship"
	if proof != nil {
		e.Subject.ParentToolRef = proof.Source.ItemID
		if proof.Method != "" {
			e.Origin.SourceKind = proof.Method
		}
		provenance = proof.Provenance
		evidence = proof.Tool
		if proof.Activity != nil {
			evidence = string(proof.Activity.Kind)
		}
		if evidence == "" {
			evidence = proof.Method
		}
		if evidence == "" {
			evidence = "history_relation"
		}
	}
	p := &rt.EventSubject{AgentID: parent, SessionID: parent}
	if proof != nil {
		p.RunID = proof.Source.TurnID
	}
	if e.Kind == rt.AgentCreated {
		e.Creation = &rt.AgentCreatedPayload{Parent: p, Evidence: evidence, Provenance: provenance}
	} else {
		e.Association = &rt.AgentAssociationPayload{State: "associated", Parent: p, Evidence: evidence, Provenance: provenance, Completeness: "partial"}
	}
	return e, true
}
func attributedEvents(native types.Event, id string) []rt.Event {
	if e, ok := derivedEvent(native); ok {
		return []rt.Event{e}
	}
	e := mapEvent(native, id)
	var source struct {
		ThreadID, TurnID string
		Turn             struct{ ID string }
	}
	_ = json.Unmarshal(native.Params, &source)
	child := source.ThreadID != "" && source.ThreadID != id
	if child {
		run := source.TurnID
		if source.Turn.ID != "" {
			run = source.Turn.ID
		}
		e.Subject = &rt.EventSubject{AgentID: source.ThreadID, SessionID: source.ThreadID, RunID: run}
		e.Origin = rt.EventOrigin{Kind: "native", Attribution: "child", Completeness: "complete", SourceKind: native.Method}
		// WorkRef belongs to root command correlation. Child run identity has its
		// own namespace and can never complete or acquire the current root operation.
		e.WorkRef = ""
		if e.Work != nil {
			state, outcome := "running", ""
			if e.Kind == rt.TurnCompleted {
				state = "terminal"
				outcome = e.Work.Status
			}
			e.Kind = rt.AgentStateChanged
			e.Work = nil
			e.AgentState = &rt.AgentStatePayload{State: state, Outcome: outcome, Terminal: state == "terminal", Completeness: "complete"}
		}
	}
	if native.Method == "thread/status/changed" {
		var n struct{ Status struct{ Type string } }
		if json.Unmarshal(native.Params, &n) != nil || n.Status.Type == "" {
			e.Kind = rt.Failure
			e.Error = &rt.SafeError{Category: "decode_error", Scope: "session"}
			return []rt.Event{e}
		}
		state := "unknown"
		switch n.Status.Type {
		case "idle":
			state = "idle"
		case "active":
			state = "running"
		}
		if e.Subject == nil {
			e.Subject = &rt.EventSubject{SessionID: source.ThreadID}
		}
		e.Kind = rt.AgentStateChanged
		e.AgentState = &rt.AgentStatePayload{State: state, Completeness: "partial"}
		return []rt.Event{e}
	}
	if native.Method != "thread/tokenUsage/updated" {
		return []rt.Event{e}
	}
	var n struct {
		TokenUsage struct{ Total, Last map[string]json.RawMessage }
	}
	if json.Unmarshal(native.Params, &n) != nil || native.DecodeError != nil || n.TokenUsage.Total == nil || n.TokenUsage.Last == nil {
		e.Kind = rt.Failure
		e.Error = &rt.SafeError{Category: "decode_error", Scope: "session"}
		return []rt.Event{e}
	}
	events := []rt.Event{}
	for _, part := range []struct {
		mode     string
		counters map[string]json.RawMessage
	}{{"cumulative", n.TokenUsage.Total}, {"delta", n.TokenUsage.Last}} {
		v := e
		if e.Subject != nil {
			subject := *e.Subject
			v.Subject = &subject
		}
		v.Kind = rt.UsageUpdated
		v.WorkRef = source.TurnID
		if child {
			v.WorkRef = ""
		}
		scope := "thread"
		if child {
			scope = "agent_run"
		}
		v.Usage = &rt.UsagePayload{Counters: part.counters, Mode: part.mode, AccountingScope: scope, ChildInclusion: "unknown"}
		events = append(events, v)
	}
	return events
}
