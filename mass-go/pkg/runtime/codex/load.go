package codex

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/zoumo/agent-sdk-go/agent"
	sdk "github.com/zoumo/agent-sdk-go/codex"
	ct "github.com/zoumo/agent-sdk-go/codex/types"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/agentrelay"
)

// The read lifetime is separate from the retained SDK Session lifetime.
// Neither a read completion nor ending its context closes a successful resume.
type nativeLoad struct {
	id            string
	cancel        context.CancelFunc
	done          chan struct{}
	cleanup       sync.Mutex
	source        events.Source
	address       runtime.Address
	s             *session
	native        agent.Session
	resumeUnknown bool
	terminal      bool
	read          context.Context
	home          string
}

func (d *Driver) LoadSession(ctx context.Context, id string, a runtime.Address, spec runtime.SessionSpec, loadID string) (result error) {
	cwd := spec.Cwd
	readCtx, cancel := context.WithCancel(ctx)
	h := &nativeLoad{id: loadID, cancel: cancel, read: readCtx, done: make(chan struct{}), source: events.NewSource(), address: a}
	d.mu.Lock()
	if !d.providerReady || d.stopped || d.stopping || d.sessions[id] != nil || d.loads[id] != nil || len(d.sessions)+d.opening >= 128 {
		d.mu.Unlock()
		cancel()
		return runtime.FailedPrecondition("load unavailable")
	}
	if d.loads == nil {
		d.loads = map[string]*nativeLoad{}
	}
	d.loads[id] = h
	d.opening++
	provider := d.provider
	d.mu.Unlock()
	defer func() {
		if result != nil {
			phase := "failed"
			if errors.Is(result, context.Canceled) {
				phase = "canceled"
			}
			_ = d.loadEmit(h, id, "", "", nil, events.HistoryReplay{LoadID: loadID, Phase: phase, RetainedScope: "all_retained_main_history", Error: map[string]any{"code": "unavailable", "reason": "native_history_failed", "message": "Native history read did not complete"}})
		}
		d.mu.Lock()
		h.terminal = true
		d.opening--
		d.mu.Unlock()
		cancel()
		close(h.done)
	}()

	reader, err := d.reader()
	if err != nil {
		return err
	}
	if !filepath.IsAbs(cwd) || !filepath.IsAbs(reader.ServerInfo().CodexHome) {
		return errors.New("absolute native home and cwd required")
	}
	h.home = filepath.Clean(reader.ServerInfo().CodexHome)
	if err := readCtx.Err(); err != nil {
		return err
	}
	if err := (agent.OpenOptions{Config: spec.Config.SDKConfig(), Cwd: spec.Cwd, AdditionalSystemPrompt: spec.SystemPrompt}).Validate(); err != nil {
		return agentrelay.MapError(err)
	}
	if err := d.vault.Check(); err != nil {
		return err
	}
	if len(spec.AdditionalDirs) > 0 {
		return runtime.UnsupportedOption("additional_dirs_unsupported", "Additional directories are not supported by this Codex runtime")
	}
	mcpServers, err := codexMCPServers(spec.MCPServers, d.options)
	if err != nil {
		return err
	}
	before, err := reader.ReadThread(readCtx, ct.ThreadReadParams{ThreadID: id, IncludeTurns: ct.Some(false)})
	if err != nil {
		return err
	}
	if err := verifyLoadThread(before.Thread, id, cwd, true); err != nil {
		return err
	}
	if p, ok := before.Thread.Path.Get(); ok {
		rel, err := filepath.Rel(h.home, p)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errors.New("native source is outside initialized home")
		}
	}
	if err := readCtx.Err(); err != nil {
		return err
	}
	// Once dispatched, a missing resume response cannot prove native release.
	h.resumeUnknown = true
	native, err := provider.Resume(readCtx, agent.ResumeOptions{
		SessionID: id,
		OpenOptions: agent.OpenOptions{
			Cwd:                    cwd,
			AdditionalSystemPrompt: spec.SystemPrompt,
			Config:                 spec.Config.SDKConfig(),
			Features:               agentFeatures(spec.Features),
			MCPServers:             mcpServers,
		},
	})
	if err != nil {
		h.resumeUnknown = resumeOutcomeUnknown(err)
		return agentrelay.MapError(err)
	}
	if native == nil {
		return errors.New("resume returned no native session")
	}
	h.native = native
	h.resumeUnknown = false

	after, err := reader.ReadThread(readCtx, ct.ThreadReadParams{ThreadID: id, IncludeTurns: ct.Some(false)})
	if err != nil {
		return err
	}
	if err := verifyLoadThread(after.Thread, id, cwd, false); err != nil {
		return err
	}
	if native.ID() != id {
		return errors.New("resume changed native session identity")
	}
	if !sameLoadResumeThread(before.Thread, after.Thread) {
		return errors.New("resume changed native source identity")
	}
	if err := d.vault.Save(id, a, spec, after.Thread); err != nil {
		return err
	}
	resumed := &events.ResumedSession{
		SessionID:         id,
		Capabilities:      agentrelay.MapCapabilities(native.Capabilities()),
		EffectiveFeatures: events.ResumedSessionFeatures{AgentQuestions: native.EffectiveFeatures().AgentQuestions},
	}
	if err := d.loadEmit(h, id, "", "", nil, events.HistoryReplay{LoadID: loadID, Phase: "started", RetainedScope: "all_retained_main_history", ResumedSession: resumed}); err != nil {
		return err
	}

	s, err := d.bindSession(native, a, h.source)
	if err != nil {
		return err
	}
	h.s = s
	first, err := d.readLoad(readCtx, reader, h, id, true)
	if err != nil {
		return err
	}
	second, err := d.readLoad(readCtx, reader, h, id, false)
	if err != nil {
		return err
	}
	if first != second {
		return errors.New("native ordered history changed during verification")
	}
	final, err := reader.ReadThread(readCtx, ct.ThreadReadParams{ThreadID: id, IncludeTurns: ct.Some(false)})
	if err != nil {
		return err
	}
	if err := verifyLoadThread(final.Thread, id, cwd, false); err != nil {
		return err
	}
	if !sameLoadThread(after.Thread, final.Thread) {
		return errors.New("native metadata changed during history read")
	}
	d.mu.Lock()
	healthy := d.sessions[id] == s && !d.stopping && !d.stopped && s.ordinaryAvailable()
	d.mu.Unlock()
	if !healthy {
		return errors.New("resumed binding became unsettled")
	}
	// Verification detects observed drift; it is not a cross-page atomic snapshot.
	return d.loadEmit(h, id, "", "", map[string]any{"atomic": false, "verification": "ordered_second_read"}, events.HistoryReplay{LoadID: loadID, Phase: "native_completed", RetainedScope: "all_retained_main_history", ResumedSession: resumed})
}

func resumeOutcomeUnknown(err error) bool {
	if agent.CodeOf(err) == agent.ErrorOutcomeUnknown {
		return true
	}
	var call *sdk.CallError
	return errors.As(err, &call) && call.Dispatch == sdk.OutcomeUnknown
}

func verifyLoadThread(t ct.Thread, id, cwd string, beforeResume bool) error {
	if t.ID != id || t.SessionID == "" || t.Cwd != cwd || t.Ephemeral || t.ModelProvider == "" || t.CreatedAt <= 0 {
		return errors.New("native identity or source context mismatch")
	}
	if t.Status.Idle == nil && (!beforeResume || t.Status.NotLoaded == nil) {
		return errors.New("native current work cannot be proven settled")
	}
	if len(t.Source.Unknown) > 0 || t.Source.SubAgent != nil || t.Source.Value == nil || *t.Source.Value == ct.SessionSourceValueUnknown {
		return errors.New("unsupported native source")
	}
	if p, ok := t.Path.Get(); ok && !filepath.IsAbs(p) {
		return errors.New("invalid native source path")
	}
	return nil
}

func sameLoadThread(a, b ct.Thread) bool {
	// Metadata and source, not the request ID, establish independent ownership.
	a.Turns = nil
	b.Turns = nil
	return reflect.DeepEqual(a, b)
}

func sameLoadResumeThread(a, b ct.Thread) bool {
	// Only cold resume may materialize loaded metadata. All other fields,
	// including unknown status metadata, and subsequent reads remain strict.
	if cold := a.Status.NotLoaded; cold != nil && b.Status.Idle != nil {
		if a.UpdatedAt < a.CreatedAt || b.UpdatedAt < a.UpdatedAt {
			return false
		}
		a.UpdatedAt = b.UpdatedAt
		if direct, ok := b.CanAcceptDirectInput.Get(); a.CanAcceptDirectInput.IsNull() && ok && direct {
			a.CanAcceptDirectInput = b.CanAcceptDirectInput
		}
		if loaded, ok := b.Environments.Get(); a.Environments.IsNull() && ok {
			for _, environment := range loaded {
				if len(environment.UnknownFields) != 0 {
					return false
				}
			}
			a.Environments = b.Environments
		}
		a.Status = ct.ThreadStatus{Idle: &ct.IdleThreadStatus{Type: ct.IdleThreadStatusTypeIdle, UnknownFields: cold.UnknownFields}}
	}
	return sameLoadThread(a, b)
}

func (d *Driver) loadEmit(h *nativeLoad, id, item, parent string, meta map[string]any, p events.Payload) error {
	if meta == nil {
		meta = map[string]any{}
	}
	meta["nativeHome"] = h.home
	base := events.EventBase{
		EventID:     uuid.NewString(),
		Namespace:   h.address.Namespace,
		SessionName: h.address.SessionName,
		SessionID:   id,
		Time:        time.Now().UTC(),
		ItemID:      item,
		Replay:      &events.ReplaySource{LoadID: h.id, NativeRecordID: item, NativeParentID: parent},
		Meta:        meta,
	}
	if raw, ok := meta["native"].(map[string]any); ok {
		for _, key := range []string{"completedAtMs", "startedAtMs"} {
			n, ok := raw[key].(json.Number)
			if !ok {
				continue
			}
			ms, err := n.Int64()
			if err != nil {
				return err
			}
			base.Time = time.UnixMilli(ms).UTC()
			meta["timeSource"] = key
			break
		}
	}
	e, err := events.NewEvent(base, p, h.source)
	if err != nil {
		return err
	}
	ctx := h.read
	if marker, ok := p.(events.HistoryReplay); ok && (marker.Phase == "failed" || marker.Phase == "canceled") {
		ctx = d.life
	}
	return d.pipe.Send(ctx, e)
}

// Bounds cover the entire enumeration (not merely a page). Cursors are opaque;
// explicit null, rather than an omitted member, is the exhaustion evidence.
func (d *Driver) readLoad(ctx context.Context, reader codexReadClient, h *nativeLoad, id string, emit bool) ([32]byte, error) {
	hash := sha256.New()
	pages, records, size := 0, 0, 0
	record := func(v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		size += len(b)
		if size > 64<<20 {
			return errors.New("history byte bound exceeded")
		}
		_, err = hash.Write(b)
		return err
	}
	var cursor ct.Optional[string]
	seen := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return [32]byte{}, err
		}
		pages++
		if pages > 4096 {
			return [32]byte{}, errors.New("history page bound exceeded")
		}
		turns, err := reader.ListThreadTurns(ctx, ct.ThreadTurnsListParams{ThreadID: id, SortDirection: ct.Some(ct.SortDirectionAsc), ItemsView: ct.Some(ct.TurnItemsViewFull), Limit: ct.Some(uint32(100)), Cursor: cursor})
		if err != nil {
			return [32]byte{}, err
		}
		if err := record(turns); err != nil {
			return [32]byte{}, err
		}
		for _, turn := range turns.Data {
			records++
			if records > 100000 {
				return [32]byte{}, errors.New("history record bound exceeded")
			}
			if turn.ID == "" || (turn.Status != ct.TurnStatusCompleted && turn.Status != ct.TurnStatusFailed && turn.Status != ct.TurnStatusInterrupted) || turn.ItemsView.ValueOr("") != ct.TurnItemsViewFull {
				return [32]byte{}, errors.New("unsettled turn or incomplete item view")
			}
			var ic ct.Optional[ct.ThreadItemsListCursor]
			is := map[string]bool{}
			for {
				if err := ctx.Err(); err != nil {
					return [32]byte{}, err
				}
				pages++
				if pages > 4096 {
					return [32]byte{}, errors.New("history page bound exceeded")
				}
				items, err := reader.ListThreadItems(ctx, ct.ThreadItemsListParams{ThreadID: id, TurnID: ct.Some(turn.ID), SortDirection: ct.Some(ct.SortDirectionAsc), Limit: ct.Some(uint32(100)), Cursor: ic})
				if err != nil {
					return [32]byte{}, err
				}
				if err := record(items); err != nil {
					return [32]byte{}, err
				}
				for _, entry := range items.Data {
					records++
					if records > 100000 {
						return [32]byte{}, errors.New("history record bound exceeded")
					}
					if entry.TurnID != turn.ID {
						return [32]byte{}, errors.New("native item parent mismatch")
					}
					if emit {
						pagination := map[string]any{"direction": "asc", "itemsView": "full", "turnId": turn.ID}
						if v, ok := cursor.Get(); ok {
							pagination["turnCursor"] = v
						}
						if v, ok := ic.Get(); ok {
							if v.Value != nil {
								pagination["itemCursor"] = *v.Value
							}
						}
						if items.NextCursor.IsSet() {
							var next any
							if v, ok := items.NextCursor.Get(); ok {
								next = v
							}
							pagination["nextItemCursor"] = next
						}
						if turns.NextCursor.IsSet() {
							var next any
							if v, ok := turns.NextCursor.Get(); ok {
								next = v
							}
							pagination["nextTurnCursor"] = next
						}
						if err := d.loadItem(h, id, entry, pagination); err != nil {
							return [32]byte{}, err
						}
					}
				}
				next, end, err := loadCursor(items.NextCursor, is)
				if err != nil {
					return [32]byte{}, err
				}
				if end {
					break
				}
				ic = ct.Some(ct.ThreadItemsListCursor{Value: new(next)})
			}
		}
		next, end, err := loadCursor(turns.NextCursor, seen)
		if err != nil {
			return [32]byte{}, err
		}
		if end {
			break
		}
		cursor = ct.Some(next)
	}
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}

func loadCursor(c ct.Optional[string], seen map[string]bool) (string, bool, error) {
	if !c.IsSet() {
		return "", false, errors.New("missing native exhaustion evidence")
	}
	if c.IsNull() {
		return "", true, nil
	}
	v, _ := c.Get()
	if v == "" || seen[v] {
		return "", false, errors.New("nonadvancing native cursor")
	}
	seen[v] = true
	return v, false, nil
}

func (d *Driver) CancelHistoryLoad(ctx context.Context, id, loadID string) error {
	d.mu.Lock()
	h := d.loads[id]
	d.mu.Unlock()
	if h == nil || h.id != loadID {
		return runtime.FailedPrecondition("original native load unavailable")
	}
	h.cancel()
	select {
	case <-h.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	h.cleanup.Lock()
	defer h.cleanup.Unlock()
	d.mu.Lock()
	unknown := h.resumeUnknown
	s := h.s
	native := h.native
	d.mu.Unlock()
	if unknown {
		return errors.New("native resume outcome unknown; original ownership retained")
	}
	if s != nil {
		return d.CloseSession(ctx, id)
	}
	if native == nil {
		return nil
	}
	if err := native.Close(context.WithoutCancel(ctx)); err != nil {
		return agentrelay.MapError(err)
	}
	return nil
}
