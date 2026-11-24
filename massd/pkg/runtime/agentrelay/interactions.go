package agentrelay

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/massd/pkg/runtime"
)

// Interactions remembers the kind of each live request on one SDK Session.
// The SDK owns native answer authority and settlement.
type Interactions struct {
	mu      sync.Mutex
	pending map[agent.InteractionRef]*interaction
}

type interaction struct {
	kind      agent.InteractionKind
	answering bool
}

func (p *Interactions) Answer(ctx context.Context, session agent.Session, ref agent.InteractionRef, value any, mapNativeError func(error) error) (runtime.Acceptance, error) {
	p.mu.Lock()
	entry := p.pending[ref]
	if entry == nil || entry.answering {
		p.mu.Unlock()
		return runtime.Acceptance{}, runtime.FailedPrecondition("interaction is no longer answerable")
	}
	kind := entry.kind
	p.mu.Unlock()

	answer, err := parseAnswer(kind, value)
	if err != nil {
		return runtime.Acceptance{}, err
	}

	p.mu.Lock()
	if p.pending[ref] != entry || entry.answering {
		p.mu.Unlock()
		return runtime.Acceptance{}, runtime.FailedPrecondition("interaction is no longer answerable")
	}
	entry.answering = true
	p.mu.Unlock()

	receipt, err := session.Answer(ctx, ref, answer)
	sent := receipt.Acceptance.Sent != nil && *receipt.Acceptance.Sent
	if err != nil && !sent && agent.CodeOf(err) != agent.ErrorOutcomeUnknown {
		p.mu.Lock()
		if p.pending[ref] == entry {
			entry.answering = false
		}
		p.mu.Unlock()
	}
	return MapAcceptance(receipt.Acceptance), mapNativeError(err)
}

func parseAnswer(kind agent.InteractionKind, value any) (agent.Answer, error) {
	fields, ok := value.(map[string]any)
	if !ok || fields == nil {
		return nil, runtime.InvalidArgument("interaction requires an object answer")
	}
	var answer agent.Answer
	switch kind {
	case agent.InteractionPermission:
		for key := range fields {
			if key != "outcome" && key != "optionId" {
				return nil, runtime.InvalidArgument("permission answer contains unsupported fields")
			}
		}
		outcome, _ := fields["outcome"].(string)
		option, _ := fields["optionId"].(string)
		if _, exists := fields["optionId"]; exists && option == "" {
			return nil, runtime.InvalidArgument("permission optionId must be a nonempty string")
		}
		answer = agent.PermissionAnswer{Outcome: agent.PermissionOutcome(outcome), OptionID: option}
	case agent.InteractionForm:
		for key := range fields {
			if key != "action" && key != "content" {
				return nil, runtime.InvalidArgument("form answer contains unsupported fields")
			}
		}
		action, _ := fields["action"].(string)
		var content map[string]any
		if raw, exists := fields["content"]; exists {
			if action != string(agent.FormAccept) {
				return nil, runtime.InvalidArgument("declined or canceled form answer must not contain content")
			}
			var valid bool
			content, valid = raw.(map[string]any)
			if !valid || content == nil {
				return nil, runtime.InvalidArgument("form content must be a JSON object")
			}
			if _, err := json.Marshal(content); err != nil {
				return nil, runtime.InvalidArgument("form content must be JSON-compatible")
			}
		}
		answer = agent.FormAnswer{Outcome: agent.FormOutcome(action), Content: content}
	case agent.InteractionURL:
		action, _ := fields["action"].(string)
		if len(fields) != 1 || action == "" {
			return nil, runtime.InvalidArgument("URL answer requires only action")
		}
		answer = agent.URLAnswer{Outcome: agent.URLOutcome(action)}
	default:
		return nil, runtime.InvalidArgument("unsupported interaction kind")
	}
	if err := agent.ValidateAnswer(kind, answer); err != nil {
		return nil, MapError(err)
	}
	return answer, nil
}

func (p *Interactions) observe(event events.SessionEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch payload := event.Payload().(type) {
	case events.InteractionRequested:
		if p.pending == nil {
			p.pending = make(map[agent.InteractionRef]*interaction)
		}
		if _, exists := p.pending[payload.Interaction]; exists {
			return errors.New("SDK reused a live interaction reference")
		}
		p.pending[payload.Interaction] = &interaction{kind: payload.Request.Kind}
	case events.InteractionClosed:
		delete(p.pending, payload.Interaction)
	}
	return nil
}
