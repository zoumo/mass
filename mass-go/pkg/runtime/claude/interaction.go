package claude

import (
	"context"
	"encoding/json"

	sdk "github.com/zoumo/agent-sdk-go/claude"
	"github.com/zoumo/mass/mass-go/internal/managed"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

func mapPermission(p *sdk.PermissionRequest) (rt.Event, *managed.Authority) {
	event := rt.Event{Kind: rt.InteractionRequested, Diagnostic: diagnostic("can_use_tool", p.Raw())}
	spec := rt.Interaction{Kind: "permission", State: "requested", Prompt: p.ToolName(), ToolName: p.ToolName(), CallRef: p.ToolUseID(), Decisions: []string{"allow_once", "deny_once"}}
	spec.Input = &rt.ContentSet{Availability: "provided", Completeness: "complete", Blocks: []rt.ContentBlock{{Kind: "json", JSON: p.Input(), Stage: "completed", Completeness: "complete"}}}
	var question *sdk.AskUserQuestionInput
	if p.ToolName() == "AskUserQuestion" {
		question = new(sdk.AskUserQuestionInput)
		if json.Unmarshal(p.Input(), question) != nil {
			event.Kind = rt.Failure
			event.Error = &rt.SafeError{Category: "decode_error", Scope: "session"}
			return event, nil
		}
		spec.Kind = "question"
		spec.Input = nil
		spec.Decisions = nil
		for _, q := range question.Questions {
			free := true
			common := rt.Question{ID: q.Question, Prompt: q.Question, Header: q.Header, Multiple: q.MultiSelect, FreeText: &free}
			// The pinned schema defines no required or secret constraint. Nil means unknown.
			for _, o := range q.Options {
				common.Options = append(common.Options, rt.QuestionOption{Label: o.Label, Description: o.Description})
			}
			spec.Questions = append(spec.Questions, common)
		}
	}
	authority := &managed.Authority{NativeKey: p.RequestID(), Spec: spec, Bytes: len(p.Raw()), Live: func() bool { return p.Context().Err() == nil }}
	authority.Prepare = func(response rt.Response) (func(context.Context) error, error) {
		answer := sdk.PermissionAnswer{Behavior: sdk.PermissionDeny}
		if question != nil {
			answers := map[string]sdk.QuestionAnswer{}
			for id, a := range response.Answers {
				if a.Text != nil {
					answers[id] = sdk.QuestionAnswer{Text: a.Text}
				} else if len(a.Selections) == 1 {
					multi := false
					for _, q := range question.Questions {
						if q.Question == id {
							multi = q.MultiSelect
						}
					}
					if multi {
						answers[id] = sdk.QuestionAnswer{Labels: a.Selections}
					} else {
						text := a.Selections[0]
						answers[id] = sdk.QuestionAnswer{Text: &text}
					}
				} else {
					answers[id] = sdk.QuestionAnswer{Labels: a.Selections}
				}
			}
			updated, err := question.WithAnswers(answers)
			if err != nil {
				return nil, rt.ErrInvalidInput
			}
			answer.Behavior = sdk.PermissionAllow
			answer.UpdatedInput = updated
		} else if response.Decision == "allow_once" {
			answer.Behavior = sdk.PermissionAllow
			answer.UpdatedInput = p.Input()
		}
		return func(ctx context.Context) error { return p.Answer(ctx, answer) }, nil
	}
	event.Interaction = &spec
	return event, authority
}
func (s *session) Respond(ctx context.Context, ref string, r rt.Response) (rt.ResponseReceipt, error) {
	return s.conn.authorities.Respond(ctx, s.Key(), ref, r)
}
func (s *session) InvalidateInteractions(work string) { s.conn.authorities.Invalidate(work) }
