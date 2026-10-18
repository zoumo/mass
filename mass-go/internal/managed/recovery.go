package managed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

type recoveryContext struct {
	Key            rt.SessionKey
	Scope          string
	ReleaseUnknown bool
}

func recoveryScope(name, kind string, config rt.Config) string {
	// Hash execution scope without returning environment values to the binding port.
	// Operational provisional limits do not change account/data ownership.
	data, _ := json.Marshal(struct {
		Name, Kind, Executable, Dir string
		Env                         []string
	}{name, kind, config.Executable, config.Dir, config.Env})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func (r *Runtime) saveBinding(ctx context.Context, key rt.SessionKey) error {
	return r.saveRecovery(ctx, key, false)
}
func (r *Runtime) saveRecovery(ctx context.Context, key rt.SessionKey, releaseUnknown bool) error {
	if r.bindings == nil {
		return nil
	}
	data, _ := json.Marshal(recoveryContext{Key: key, Scope: r.scope, ReleaseUnknown: releaseUnknown})
	if err := r.bindings.Save(ctx, key, rt.RecoveryBinding{Version: 1, Data: data}); err != nil {
		return rt.ErrUnverifiable
	}
	return nil
}
func (r *Runtime) ResumeSession(ctx context.Context, key rt.SessionKey) (rt.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	if key.RuntimeName != r.snapshot.Name || key.SessionID == "" {
		r.mu.Unlock()
		return nil, rt.ErrNotFound
	}
	if r.resuming[key] || r.closing[key] || r.closeUnknown[key] {
		r.mu.Unlock()
		return nil, rt.ErrConflict
	}
	if r.snapshot.Phase != "ready" {
		r.mu.Unlock()
		return nil, rt.ErrUnavailable
	}
	for _, s := range r.sessions {
		if s.Key() == key && s.Snapshot().Attachment != "closed" {
			r.mu.Unlock()
			return nil, rt.ErrConflict
		}
	}
	if r.bindings == nil {
		r.mu.Unlock()
		return nil, rt.ErrUnverifiable
	}
	if r.attachments >= 256 {
		r.mu.Unlock()
		return nil, rt.ErrBackpressure
	}
	r.attachments++
	r.resuming[key] = true
	r.creates.Add(1)
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.resuming, key); r.mu.Unlock(); r.creates.Done() }()
	binding, err := r.bindings.Load(ctx, key)
	if err != nil || binding.Version != 1 {
		return nil, rt.ErrUnverifiable
	}
	var saved recoveryContext
	if json.Unmarshal(binding.Data, &saved) != nil || saved.Key != key {
		return nil, rt.ErrUnverifiable
	}
	if saved.ReleaseUnknown {
		return nil, rt.ErrConflict
	}
	if saved.Scope != r.scope {
		return nil, rt.ErrConflict
	}
	openCtx, cancel := context.WithTimeout(ctx, r.limits.Timeout)
	defer cancel()
	session, err := r.driver.ResumeSession(openCtx, key)
	if err != nil {
		return nil, err
	}
	if session.Key() != key {
		_ = AbandonSession(session)
		return nil, rt.ErrIdentityMismatch
	}
	r.mu.Lock()
	if r.snapshot.Phase != "ready" {
		r.mu.Unlock()
		_ = AbandonSession(session)
		return nil, rt.ErrUnavailable
	}
	replaced := false
	for i, s := range r.sessions {
		if s.Key() == key {
			r.retiredSessions = append(r.retiredSessions, s)
			r.sessions[i] = session
			replaced = true
			break
		}
	}
	if !replaced {
		r.sessions = append(r.sessions, session)
	}
	r.mu.Unlock()
	return session, nil
}
func (r *Runtime) CloseSession(ctx context.Context, key rt.SessionKey, goal rt.CloseGoal) (rt.CloseReceipt, error) {
	receipt := rt.CloseReceipt{Key: key, Outcome: "rejected", NativeRelease: "unsupported", LocalAttachmentClosed: rt.UnknownObservation(), OwnedProcessReaped: rt.Observation{State: "not_applicable"}}
	if err := ctx.Err(); err != nil {
		return receipt, err
	}
	if goal != rt.CloseDetach && goal != rt.CloseRelease {
		return receipt, rt.ErrInvalidInput
	}
	r.mu.Lock()
	busy := r.resuming[key] || r.closing[key]
	if !busy {
		r.closing[key] = true
	}
	r.mu.Unlock()
	if busy {
		return receipt, rt.ErrConflict
	}
	clearClosing := true
	defer func() {
		if clearClosing {
			r.mu.Lock()
			delete(r.closing, key)
			r.mu.Unlock()
		}
	}()
	session, err := r.GetSession(key)
	if err != nil {
		return receipt, err
	}
	kind := r.Status().Type
	if kind == "claude" {
		receipt.OwnedProcessReaped = rt.UnknownObservation()
	}
	// Persist a conservative fence before releasing ownership. A fresh Runtime
	// using the same host store must not forget an uncertain close.
	if err := r.saveRecovery(ctx, key, true); err != nil {
		return receipt, err
	}
	// Reserve a release fence before I/O. An uncertain release never permits a
	// replacement attachment to infer that native work has ended.
	r.mu.Lock()
	if goal == rt.CloseRelease {
		r.closeUnknown[key] = true
	}
	r.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- session.Close() }()
	select {
	case err = <-done:
		receipt.LocalAttachmentClosed = rt.Known(true)
		if kind == "claude" {
			if err == nil {
				receipt.OwnedProcessReaped = rt.Known(true)
			}
		}
	case <-ctx.Done():
		err = ctx.Err()
		clearClosing = false
		go func() { <-done; r.mu.Lock(); delete(r.closing, key); r.closeUnknown[key] = true; r.mu.Unlock() }()
	}
	r.mu.Lock()
	releaseBlocked := r.closeUnknown[key]
	r.mu.Unlock()
	if err == nil && goal == rt.CloseDetach && !releaseBlocked {
		if saveErr := r.saveBinding(ctx, key); saveErr != nil {
			err = saveErr
		}
	}
	receipt.IncompleteFacts = true // Local detach aborts delivery; native effects are unknown.
	receipt.Outcome = "closed"
	if err != nil || goal == rt.CloseRelease {
		r.mu.Lock()
		r.closeUnknown[key] = true
		r.mu.Unlock()
		receipt.Outcome = "close_unknown"
		return receipt, rt.ErrOutcomeUnknown
	}
	return receipt, nil
}
