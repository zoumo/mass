package agentrelay

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

// Interactions remembers the kind of each live request on one SDK Session.
// The SDK owns native answer authority. MASS keeps accepted URL flows pending
// when the SDK closes only its request/response exchange.
type Interactions struct {
	mu      sync.Mutex
	pending map[agent.InteractionRef]*interaction
}

type interaction struct {
	kind         agent.InteractionKind
	claudeURL    claudeElicitation
	answering    bool
	acceptURL    bool
	acknowledged bool
	completed    bool
}

type claudeElicitation struct {
	server string
	id     string
}

func (p *Interactions) Answer(ctx context.Context, session agent.Session, ref agent.InteractionRef, value any) (agent.InteractionReceipt, error) {
	p.mu.Lock()
	entry := p.pending[ref]
	if entry == nil || entry.answering {
		p.mu.Unlock()
		return agent.InteractionReceipt{}, runtime.FailedPrecondition("interaction is no longer answerable")
	}
	answer, err := parseAnswer(entry.kind, value)
	if err != nil {
		p.mu.Unlock()
		return agent.InteractionReceipt{}, err
	}
	entry.answering = true
	if url, ok := answer.(agent.URLAnswer); ok {
		entry.acceptURL = url.Outcome == agent.URLAccept
	}
	p.mu.Unlock()

	receipt, err := session.Answer(ctx, ref, answer)
	sent := receipt.Acceptance.Sent != nil && *receipt.Acceptance.Sent
	if err != nil && !sent && agent.CodeOf(err) != agent.ErrorOutcomeUnknown {
		p.mu.Lock()
		if p.pending[ref] == entry && !entry.acknowledged && !entry.completed {
			entry.answering = false
			entry.acceptURL = false
		}
		p.mu.Unlock()
	}
	return receipt, err
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
			if !valid || content == nil || events.CheckDynamicJSON(content) != nil {
				return nil, runtime.InvalidArgument("form content must be a JSON object")
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

func (p *Interactions) observe(event events.SessionEvent) (events.SessionEvent, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch payload := event.Payload().(type) {
	case events.InteractionRequested:
		if p.pending == nil {
			p.pending = make(map[agent.InteractionRef]*interaction)
		}
		if _, exists := p.pending[payload.Interaction]; exists {
			return events.SessionEvent{}, false, errors.New("SDK reused a live interaction reference")
		}
		entry := &interaction{kind: payload.Request.Kind}
		if payload.Request.Kind == agent.InteractionURL {
			entry.claudeURL = claudeURLRequest(event)
		}
		p.pending[payload.Interaction] = entry
	case events.InteractionClosed:
		if entry := p.pending[payload.Interaction]; entry != nil {
			if entry.completed {
				delete(p.pending, payload.Interaction)
				return events.SessionEvent{}, false, nil
			}
			if entry.acceptURL && payload.Outcome == agent.InteractionAnswered {
				entry.acknowledged = true
				return events.SessionEvent{}, false, nil
			}
			delete(p.pending, payload.Interaction)
		}
	case events.SessionError:
		if payload.Reason == "native_event_unsupported" {
			return p.claudeCompletion(event)
		}
	}
	return event, true, nil
}

func claudeURLRequest(event events.SessionEvent) claudeElicitation {
	meta, _ := event.Meta["claude"].(map[string]any)
	if meta["type"] != "control_request" || meta["subtype"] != "elicitation" {
		return claudeElicitation{}
	}
	raw, _ := meta["raw"].(map[string]any)
	request, _ := raw["request"].(map[string]any)
	server, _ := request["mcp_server_name"].(string)
	id, _ := request["elicitation_id"].(string)
	return claudeElicitation{server: server, id: id}
}

func (p *Interactions) claudeCompletion(event events.SessionEvent) (events.SessionEvent, bool, error) {
	meta, _ := event.Meta["claude"].(map[string]any)
	if meta["type"] != "system" || meta["subtype"] != "elicitation_complete" {
		return event, true, nil
	}
	raw, _ := meta["raw"].(map[string]any)
	server, _ := raw["mcp_server_name"].(string)
	id, _ := raw["elicitation_id"].(string)
	if raw["session_id"] != event.SessionID || server == "" || id == "" {
		return event, true, nil
	}
	var match agent.InteractionRef
	for ref, entry := range p.pending {
		if entry.claudeURL != (claudeElicitation{server: server, id: id}) {
			continue
		}
		if match != "" {
			return event, true, nil
		}
		match = ref
	}
	if match == "" || !p.pending[match].acceptURL {
		return event, true, nil
	}
	entry := p.pending[match]
	if entry.completed {
		return events.SessionEvent{}, false, nil
	}
	identity, err := uuid.NewRandom()
	if err != nil {
		return events.SessionEvent{}, false, err
	}
	// The SDK diagnostic supplies native evidence for a separate MASS
	// lifecycle event. Do not reuse the diagnostic's event identity.
	base := event.EventBase
	base.EventID = identity.String()
	base.Meta = nil
	closed, err := events.NewEvent(base, events.InteractionClosed{
		Interaction: match,
		Outcome:     agent.InteractionCompleted,
	}, event.Source())
	if err != nil {
		return events.SessionEvent{}, false, err
	}
	if entry.acknowledged {
		delete(p.pending, match)
	} else {
		// The matching native completion can precede the SDK's acknowledgment.
		// Keep the reference only to absorb that later closure.
		entry.completed = true
	}
	return closed, true, nil
}

// Expire emits lifecycle evidence only after the owner has released the native
// Session and drained observation. A failed send leaves the entry retryable.
func (p *Interactions) Expire(ctx context.Context, sessionID string, address runtime.Address, source events.Source, sink Sink) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for ref, entry := range p.pending {
		if !entry.acceptURL || entry.completed {
			continue
		}
		id, err := uuid.NewRandom()
		if err != nil {
			return err
		}
		event, err := events.NewEvent(events.EventBase{
			EventID: id.String(), Time: time.Now().UTC(), SessionID: sessionID,
			Namespace: address.Namespace, SessionName: address.SessionName,
		}, events.InteractionClosed{Interaction: ref, Outcome: agent.InteractionExpired, Reason: "session_closed"}, source)
		if err != nil {
			return err
		}
		if err := sink(ctx, event); err != nil {
			return err
		}
		delete(p.pending, ref)
	}
	return nil
}
