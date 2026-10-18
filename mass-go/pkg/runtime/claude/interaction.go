package claude

import (
	"context"
	"encoding/json"
	"errors"

	sdk "github.com/zoumo/agent-sdk-go/claude"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

type responseHandle struct {
	native   *sdk.PermissionRequest
	question *sdk.AskUserQuestionInput
}

func (h *responseHandle) ID() string            { return h.native.RequestID() }
func (h *responseHandle) Done() <-chan struct{} { return h.native.Context().Done() }
func (h *responseHandle) Err() error {
	if h.native.Context().Err() != nil {
		return errors.New("claude interaction is no longer available")
	}
	return nil
}
func (h *responseHandle) Respond(ctx context.Context, response rt.Response) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	answer := sdk.PermissionAnswer{Behavior: sdk.PermissionDeny}
	if response.Allow {
		answer.Behavior = sdk.PermissionAllow
		answer.UpdatedInput = h.native.Input()
		if h.question != nil {
			answers := map[string]sdk.QuestionAnswer{}
			for _, question := range h.question.Questions {
				values, ok := response.Answers[question.Question]
				if !ok {
					continue
				}
				if question.MultiSelect {
					answers[question.Question] = sdk.QuestionAnswer{Labels: values}
				} else if len(values) == 1 {
					value := values[0]
					answers[question.Question] = sdk.QuestionAnswer{Text: &value}
				} else {
					return errors.New("claude question requires one answer")
				}
			}
			if len(answers) != len(response.Answers) {
				return errors.New("claude answer contains an unknown question")
			}
			updated, err := h.question.WithAnswers(answers)
			if err != nil {
				return errors.New("claude question answer is invalid")
			}
			answer.UpdatedInput = updated
		}
	}
	if err := h.native.Answer(ctx, answer); err != nil {
		return safeError(ctx, "respond")
	}
	return nil
}
func mapPermission(p *sdk.PermissionRequest) rt.Event {
	event := rt.Event{Kind: rt.InteractionRequested, ItemID: p.ToolUseID(), Tool: p.ToolName(), Diagnostic: diagnostic("can_use_tool", p.Raw())}
	handle := &responseHandle{native: p}
	interaction := &rt.Interaction{Kind: "approval", Prompt: p.ToolName(), Handle: handle}
	if p.ToolName() == "AskUserQuestion" {
		input := new(sdk.AskUserQuestionInput)
		if err := json.Unmarshal(p.Input(), input); err != nil {
			event.Kind = rt.Failure
			event.Status = "decode_error"
			return event
		}
		handle.question = input
		interaction.Kind = "question"
		for _, q := range input.Questions {
			question := rt.Question{ID: q.Question, Prompt: q.Question}
			for _, option := range q.Options {
				question.Options = append(question.Options, option.Label)
			}
			interaction.Questions = append(interaction.Questions, question)
		}
	}
	event.Interaction = interaction
	return event
}
