package runtime

import (
	"context"
	"errors"
	"sync"

	"github.com/zoumo/mass/mass-go/pkg/events"
)

// PendingInteractions retains native closures, never serializes handles, and
// consumes each reference before dispatch. Unknown writes cannot be retried.
type PendingInteractions struct {
	mu      sync.Mutex
	entries map[string]pendingInteraction
}
type pendingInteraction struct {
	session string
	source  events.Source
	answer  func(context.Context, map[string]any) error
}

func (p *PendingInteractions) Add(session string, source events.Source, answer func(context.Context, map[string]any) error) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.entries == nil {
		p.entries = map[string]pendingInteraction{}
	}
	if len(p.entries) >= 128 {
		return "", errors.New("pending interaction limit")
	}
	ref := events.UUID()
	p.entries[ref] = pendingInteraction{session, source, answer}
	return ref, nil
}

func (p *PendingInteractions) Answer(ctx context.Context, session string, source events.Source, ref string, answer map[string]any) error {
	p.mu.Lock()
	entry, ok := p.entries[ref]
	if !ok || entry.session != session || !entry.source.SameRun(source) || entry.source.Work() != source.Work() {
		p.mu.Unlock()
		return FailedPrecondition("interaction is not pending for this session")
	}
	delete(p.entries, ref)
	p.mu.Unlock()
	return entry.answer(ctx, answer)
}

func (p *PendingInteractions) RemoveSession(session string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for ref, entry := range p.entries {
		if entry.session == session {
			delete(p.entries, ref)
		}
	}
}

func PermissionAllowed(answer map[string]any) (bool, error) {
	if len(answer) != 2 || answer["outcome"] != "selected" {
		return false, errors.New("expected selected permission option")
	}
	switch answer["optionId"] {
	case "allow-once":
		return true, nil
	case "reject-once":
		return false, nil
	}
	return false, errors.New("invalid permission option")
}
