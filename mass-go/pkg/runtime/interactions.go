package runtime

import (
	"context"
	"errors"
	"sync"

	"github.com/google/uuid"

	"github.com/zoumo/mass/mass-go/pkg/events"
)

// PendingInteractions retains native closures, never serializes handles, and
// reserves each reference during dispatch. Only a confirmed unsent attempt on
// the original live capability can be retried.
type PendingInteractions struct {
	mu      sync.Mutex
	entries map[string]*pendingInteraction
}
type pendingInteraction struct {
	session string
	source  events.Source
	answer  func(context.Context, map[string]any) error
	live    func() bool
	unsent  func(error) bool
	busy    bool
}

func (p *PendingInteractions) Add(session string, source events.Source, live func() bool, unsent func(error) bool, answer func(context.Context, map[string]any) error) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.entries == nil {
		p.entries = map[string]*pendingInteraction{}
	}
	for ref, entry := range p.entries {
		if !entry.busy && !entry.live() {
			delete(p.entries, ref)
		}
	}
	if len(p.entries) >= 128 {
		return "", errors.New("pending interaction limit")
	}
	ref := uuid.NewString()
	if !source.SameRun(source) || live == nil || unsent == nil || answer == nil || !live() {
		return "", FailedPrecondition("native interaction unavailable")
	}
	p.entries[ref] = &pendingInteraction{session: session, source: source, answer: answer, live: live, unsent: unsent}
	return ref, nil
}

func (p *PendingInteractions) Answer(ctx context.Context, session string, source events.Source, ref string, answer map[string]any) error {
	p.mu.Lock()
	entry, ok := p.entries[ref]
	if !ok || entry.session != session || !entry.source.SameRun(source) || entry.source.Work() != source.Work() {
		p.mu.Unlock()
		return FailedPrecondition("interaction is not pending for this session")
	}
	if entry.busy || !entry.live() {
		if !entry.busy {
			delete(p.entries, ref)
		}
		p.mu.Unlock()
		return FailedPrecondition("native interaction is no longer answerable")
	}
	entry.busy = true
	p.mu.Unlock()
	err := entry.answer(ctx, answer)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.entries[ref] == entry {
		if err != nil && entry.unsent(err) && entry.live() {
			entry.busy = false
		} else {
			delete(p.entries, ref)
		}
	}
	return err
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
		return false, InvalidArgument("expected selected permission option")
	}
	switch answer["optionId"] {
	case "allow-once":
		return true, nil
	case "reject-once":
		return false, nil
	}
	return false, InvalidArgument("invalid permission option")
}
