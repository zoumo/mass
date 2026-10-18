package codex

import (
	"context"
	"encoding/json"
	"errors"
	sdk "github.com/zoumo/agent-sdk-go/codex"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

type responseHandle struct {
	native     sdk.AnswerHandle
	id, method string
}

func (h *responseHandle) ID() string            { return h.id }
func (h *responseHandle) Done() <-chan struct{} { return h.native.Done() }
func (h *responseHandle) Err() error {
	if h.native.Err() != nil {
		return errors.New("codex interaction is no longer available")
	}
	return nil
}
func (h *responseHandle) Respond(ctx context.Context, response rt.Response) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var result any
	switch h.method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		decision := "decline"
		if response.Allow {
			decision = "accept"
		}
		result = map[string]any{"decision": decision}
	case "item/tool/requestUserInput":
		answers := map[string]any{}
		for id, values := range response.Answers {
			answers[id] = map[string]any{"answers": values}
		}
		result = map[string]any{"answers": answers}
	default:
		return errors.New("codex interaction type is unsupported")
	}
	if err := h.native.Answer(ctx, result); err != nil {
		return safeError(ctx, "respond")
	}
	return nil
}
func mapRequest(request sdk.Request, id string) (rt.Event, rt.ResponseHandle) {
	event := rt.Event{Kind: rt.Unknown, SessionID: id, Diagnostic: diagnostic(request.Method(), request.Params())}
	kind := ""
	switch request.Method() {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		kind = "approval"
	case "item/tool/requestUserInput":
		kind = "question"
	default:
		return event, nil
	}
	if _, err := request.Data(); err != nil {
		event.Kind = rt.Failure
		event.Error = &rt.SafeError{Category: "decode_error", Scope: "session"}
		event.SessionID = ""
		return event, nil
	}
	var data struct {
		ThreadID, TurnID, ItemID string
		Reason                   string
		Questions                []struct {
			ID, Question string
			Options      []struct{ Label string }
		}
	}
	if err := json.Unmarshal(request.Params(), &data); err != nil {
		event.Kind = rt.Failure
		event.Error = &rt.SafeError{Category: "decode_error", Scope: "session"}
		return event, nil
	}
	if data.ThreadID == "" || data.ThreadID != id {
		event.SessionID = ""
		return event, nil
	}
	event.Kind = rt.InteractionRequested
	event.WorkRef = data.TurnID
	handle := &responseHandle{native: request.Handle(), id: request.ID().String(), method: request.Method()}
	interaction := &rt.Interaction{Ref: request.ID().String(), Kind: kind, Prompt: data.Reason}
	for _, q := range data.Questions {
		question := rt.Question{ID: q.ID, Prompt: q.Question}
		for _, o := range q.Options {
			question.Options = append(question.Options, o.Label)
		}
		interaction.Questions = append(interaction.Questions, question)
	}
	event.Interaction = interaction
	return event, handle
}
