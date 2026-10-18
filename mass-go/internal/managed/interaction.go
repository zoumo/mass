package managed

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"sync"

	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

// Authority stays inside adapters. Prepare preserves native input privately and
// performs conversion without I/O. A reserved attempt is never retried.
type Authority struct {
	NativeKey, WorkRef string
	Spec               rt.Interaction
	Bytes              int
	Live               func() bool
	Prepare            func(rt.Response) (func(context.Context) error, error)
	spent              bool
	attempted          bool
}
type Authorities struct {
	mu           sync.Mutex
	records      map[string]*Authority
	nativeRefs   map[string]string
	closed       bool
	invalidWorks map[string]bool
	workBlocked  bool
	bytes        int
}

func (a *Authorities) Add(h *Authority) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return "", rt.ErrUnavailable
	}
	if len(a.records) >= maxControlRecords || h.Bytes > 1<<20 || a.bytes+h.Bytes > 1<<20 {
		return "", rt.ErrBackpressure
	}
	if a.records == nil {
		a.records = map[string]*Authority{}
		a.nativeRefs = map[string]string{}
	}
	// Detach from the public event before callers can mutate its specification.
	data, err := json.Marshal(h.Spec)
	if err != nil {
		return "", rt.ErrInvalidInput
	}
	var spec rt.Interaction
	if json.Unmarshal(data, &spec) != nil {
		return "", rt.ErrInvalidInput
	}
	h.Spec = spec
	if h.WorkRef != "" && a.invalidWorks[h.WorkRef] {
		h.spent = true
	}
	ref := "interaction:" + rand.Text()
	if a.workBlocked {
		h.spent = true
	}
	a.bytes += h.Bytes
	a.records[ref] = h
	a.nativeRefs[h.NativeKey] = ref
	return ref, nil
}
func (a *Authorities) Invalidate(work string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if work == "" {
		a.workBlocked = true
	}
	if work != "" {
		if len(a.invalidWorks) >= maxControlRecords && !a.invalidWorks[work] {
			a.closed = true
			for _, h := range a.records {
				h.spent = true
			}
			return
		}
		if a.invalidWorks == nil {
			a.invalidWorks = map[string]bool{}
		}
		a.invalidWorks[work] = true
	}
	for _, h := range a.records {
		if work == "" || h.WorkRef == work {
			h.spent = true
		}
	}
}
func (a *Authorities) BeginWork() { a.mu.Lock(); a.workBlocked = false; a.mu.Unlock() }
func (a *Authorities) Close() {
	a.mu.Lock()
	a.closed = true
	for _, h := range a.records {
		h.spent = true
	}
	a.mu.Unlock()
}
func (a *Authorities) Resolve(native string) *rt.Interaction {
	a.mu.Lock()
	defer a.mu.Unlock()
	ref := a.nativeRefs[native]
	h := a.records[ref]
	if h == nil {
		return nil
	}
	h.spent = true
	return &rt.Interaction{Ref: ref, Kind: h.Spec.Kind, State: "resolved"}
}
func (a *Authorities) Respond(ctx context.Context, key rt.SessionKey, ref string, r rt.Response) (rt.ResponseReceipt, error) {
	receipt := rt.ResponseReceipt{Key: key, InteractionRef: ref, Attempt: "not_started", Outcome: "rejected", HandedToSDK: rt.Known(false), WriteCompleted: rt.UnknownObservation(), NativeResolution: rt.UnknownObservation()}
	if err := ctx.Err(); err != nil {
		return receipt, err
	}

	a.mu.Lock()
	h := a.records[ref]
	unavailable := a.closed || h == nil || h.spent
	a.mu.Unlock()
	category := "invalid_interaction"
	var send func(context.Context) error
	var err error
	if unavailable || !h.Live() {
		err = rt.ErrInvalidInteraction
	} else {
		err = h.Spec.Validate(r)
		if err == nil {
			send, err = h.Prepare(r)
		}
		if err != nil {
			category = "invalid_input"
			if errors.Is(err, rt.ErrUnsupported) {
				category = "unsupported_capability"
			}
		}
	}
	if err != nil {
		receipt.Failure = failure(category, "", key)
		return receipt, receipt.Failure
	}
	if err := ctx.Err(); err != nil {
		return receipt, err
	}
	// Native inspection/conversion happens outside the registry lock. Lifecycle
	// observations and competing attempts serialize only at the final reservation.
	a.mu.Lock()
	if a.closed || h.spent {
		a.mu.Unlock()
		receipt.Failure = failure("invalid_interaction", "", key)
		return receipt, receipt.Failure
	}
	h.spent = true
	h.attempted = true
	receipt.OperationRef = "respond:" + rand.Text()
	receipt.Attempt = "handed_off"
	receipt.HandedToSDK = rt.Known(true)
	a.mu.Unlock()
	// Reservation is the handoff boundary. No registry lock spans SDK I/O.
	err = send(ctx)
	if err != nil {
		receipt.Outcome = "unknown"
		receipt.Failure = failure("outcome_unknown", receipt.OperationRef, key)
		return receipt, receipt.Failure
	}
	receipt.Attempt = "response_written"
	receipt.Outcome = "written"
	receipt.WriteCompleted = rt.Known(true)
	return receipt, nil
}
func (s *publishedSession) Respond(ctx context.Context, ref string, r rt.Response) (rt.ResponseReceipt, error) {
	if err := ctx.Err(); err != nil {
		return rt.ResponseReceipt{}, err
	}
	s.mu.Lock()
	closed := s.closed || (s.lifecycle != nil && !s.lifecycle.accepting())
	backpressure := s.lifecycle != nil && s.lifecycle.failed()
	s.mu.Unlock()
	if backpressure {
		key := s.Key()
		receipt := rt.ResponseReceipt{Key: key, InteractionRef: ref, Attempt: "not_started", Outcome: "rejected", HandedToSDK: rt.Known(false), WriteCompleted: rt.UnknownObservation(), NativeResolution: rt.UnknownObservation(), Failure: failure("backpressure", "", key)}
		return receipt, receipt.Failure
	}
	if closed {
		key := s.Key()
		receipt := rt.ResponseReceipt{Key: key, InteractionRef: ref, Attempt: "not_started", Outcome: "rejected", HandedToSDK: rt.Known(false), WriteCompleted: rt.UnknownObservation(), NativeResolution: rt.UnknownObservation(), Failure: failure("invalid_interaction", "", key)}
		return receipt, receipt.Failure
	}
	receipt, err := s.native.Respond(ctx, ref, r)
	s.mu.Lock()
	if receipt.OperationRef != "" {
		s.emitCommandLocked(receipt.OperationRef, rt.OperationPayload{Kind: "response", State: receipt.Outcome, Response: &receipt}, receipt.Failure)
	}
	s.emitStateLocked()
	s.mu.Unlock()
	return receipt, err
}
func (r *Runtime) Respond(ctx context.Context, key rt.SessionKey, ref string, response rt.Response) (rt.ResponseReceipt, error) {
	s, err := r.GetSession(key)
	if err != nil {
		return rt.ResponseReceipt{}, err
	}
	return s.Respond(ctx, ref, response)
}

func (a *Authorities) Summary() rt.InteractionSummary {
	a.mu.Lock()
	var result rt.InteractionSummary
	var live []func() bool
	for _, h := range a.records {
		if h.attempted {
			result.Consumed++
		} else if h.spent || a.closed {
			result.Invalidated++
		} else {
			live = append(live, h.Live)
		}
	}
	a.mu.Unlock()
	for _, check := range live {
		if check() {
			result.Pending++
		} else {
			result.Invalidated++
		}
	}
	return result
}
