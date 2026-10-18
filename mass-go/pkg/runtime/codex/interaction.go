package codex

import (
	"context"
	"encoding/json"

	sdk "github.com/zoumo/agent-sdk-go/codex"
	"github.com/zoumo/mass/mass-go/internal/managed"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

func mapRequest(request sdk.Request, id string) (rt.Event, *managed.Authority) {
	event := rt.Event{Kind: rt.Unknown, SessionID: id, Diagnostic: diagnostic(request.Method(), request.Params())}
	kind := "permission"
	switch request.Method() {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
	case "item/tool/requestUserInput":
		kind = "question"
	default:
		return event, nil
	}
	var data struct {
		ThreadID, TurnID, ItemID, Reason string
		AvailableDecisions               json.RawMessage
		Questions                        []struct {
			ID, Question, Header string
			IsOther, IsSecret    *bool
			Options              []rt.QuestionOption
		}
	}
	_, err := request.Data()
	if err != nil || json.Unmarshal(request.Params(), &data) != nil {
		event.Kind = rt.Failure
		event.Error = &rt.SafeError{Category: "decode_error", Scope: "session"}
		return event, nil
	}
	if data.ThreadID == "" || data.ThreadID != id {
		event.SessionID = ""
		return event, nil
	}
	// A request explicitly attributed to a child is never promoted to root.
	var scope struct{ AgentID, ParentToolUseID string }
	_ = json.Unmarshal(request.Params(), &scope)
	if scope.AgentID != "" || scope.ParentToolUseID != "" {
		return event, nil
	}
	spec := rt.Interaction{Kind: kind, State: "requested", Prompt: data.Reason, CallRef: data.ItemID}
	if kind == "permission" {
		spec.ToolName = "commandExecution"
		if request.Method() == "item/fileChange/requestApproval" {
			spec.ToolName = "fileChange"
		}
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(request.Params(), &fields)
		context := map[string]json.RawMessage{}
		for _, name := range []string{"command", "cwd", "fileChanges", "grantRoot"} {
			if value, ok := fields[name]; ok {
				context[name] = value
			}
		}
		if len(context) > 0 {
			data, _ := json.Marshal(context)
			spec.Input = &rt.ContentSet{Availability: "provided", Completeness: "complete", Blocks: []rt.ContentBlock{{Kind: "json", JSON: data, Stage: "completed", Completeness: "complete"}}}
		}
		spec.Decisions = []string{"allow_once", "deny_once"}
		if request.Method() == "item/commandExecution/requestApproval" && len(data.AvailableDecisions) > 0 && string(data.AvailableDecisions) != "null" {
			var native []json.RawMessage
			if json.Unmarshal(data.AvailableDecisions, &native) != nil {
				event.Kind = rt.Failure
				event.Error = &rt.SafeError{Category: "decode_error", Scope: "session"}
				return event, nil
			}
			spec.Decisions = nil
			for _, raw := range native {
				var decision string
				if json.Unmarshal(raw, &decision) != nil {
					continue
				}
				switch decision {
				case "accept":
					spec.Decisions = append(spec.Decisions, "allow_once")
				case "decline":
					spec.Decisions = append(spec.Decisions, "deny_once")
				}
			}
		}
	}
	for _, q := range data.Questions {
		free := q.IsOther
		if len(q.Options) == 0 {
			v := true
			free = &v
		}
		spec.Questions = append(spec.Questions, rt.Question{ID: q.ID, Prompt: q.Question, Header: q.Header, Options: q.Options, FreeText: free, Secret: q.IsSecret})
	}
	handle := request.Handle()
	authority := &managed.Authority{NativeKey: request.ID().String(), WorkRef: data.TurnID, Spec: spec, Bytes: len(request.Params()), Live: func() bool { return handle.Err() == nil }}
	authority.Prepare = func(response rt.Response) (func(context.Context) error, error) {
		var result any
		if kind == "permission" {
			decision := "decline"
			if response.Decision == "allow_once" {
				decision = "accept"
			}
			result = map[string]any{"decision": decision}
		} else {
			answers := map[string]any{}
			for id, a := range response.Answers {
				values := a.Selections
				if a.Text != nil {
					values = []string{*a.Text}
				}
				answers[id] = map[string]any{"answers": values}
			}
			result = map[string]any{"answers": answers}
		}
		return func(ctx context.Context) error { return handle.Answer(ctx, result) }, nil
	}
	event.Kind = rt.InteractionRequested
	event.WorkRef = data.TurnID
	event.Interaction = &spec
	return event, authority
}
func (s *session) Respond(ctx context.Context, ref string, r rt.Response) (rt.ResponseReceipt, error) {
	return s.authorities.Respond(ctx, s.Key(), ref, r)
}
func (s *session) InvalidateInteractions(work string) { s.authorities.Invalidate(work) }
