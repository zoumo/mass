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
	sdk "github.com/zoumo/agent-sdk-go/codex"
	ct "github.com/zoumo/agent-sdk-go/codex/types"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

// The read lifetime is separate from the retained native Session lifetime.
// Neither a read completion nor ending its context closes a successful resume.
type nativeLoad struct {
	id            string
	cancel        context.CancelFunc
	done          chan struct{}
	cleanup       sync.Mutex
	source        events.Source
	address       runtime.Address
	s             *session
	resumeUnknown bool
	terminal      bool
	read          context.Context
	home          string
}

func (d *Driver) LoadSession(ctx context.Context, id string, a runtime.Address, cwd, loadID string) (result error) {
	readCtx, cancel := context.WithCancel(ctx)
	h := &nativeLoad{id: loadID, cancel: cancel, read: readCtx, done: make(chan struct{}), source: events.NewSource(), address: a}
	d.mu.Lock()
	if d.client == nil || d.stopped || d.stopping || d.sessions[id] != nil || d.loads[id] != nil || len(d.sessions)+d.opening >= 128 {
		d.mu.Unlock()
		cancel()
		return runtime.FailedPrecondition("load unavailable")
	}
	if d.loads == nil {
		d.loads = map[string]*nativeLoad{}
	}
	d.loads[id] = h
	d.opening++
	c := d.client
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
	if !filepath.IsAbs(cwd) || !filepath.IsAbs(c.ServerInfo().CodexHome) {
		return errors.New("absolute native home and cwd required")
	}
	h.home = filepath.Clean(c.ServerInfo().CodexHome)
	if err := readCtx.Err(); err != nil {
		return err
	}
	if err := d.vault.Check(); err != nil {
		return err
	}
	before, err := c.ReadThread(readCtx, ct.ThreadReadParams{ThreadID: id, IncludeTurns: ct.Some(false)})
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
	// Once dispatched, a missing resume response cannot prove native release.
	d.mu.Lock()
	h.resumeUnknown = true
	d.mu.Unlock()
	native, err := sdk.Open(readCtx, sdk.SessionOptions{Client: c, Resume: &ct.ThreadResumeParams{ThreadID: id, ExcludeTurns: ct.Some(true)}})
	if err != nil {
		return err
	}
	response, _ := native.ResumeResponse()
	s := &session{native: native, address: a, source: h.source, done: make(chan struct{}), released: make(chan struct{}), retired: map[string]bool{}}
	d.mu.Lock()
	h.s = s
	h.resumeUnknown = false
	d.sessions[id] = s
	d.wg.Add(1)
	d.mu.Unlock()
	observing := false
	startObserve := func() {
		if !observing {
			observing = true
			go d.observe(id, s)
		}
	}
	defer startObserve() // Failed verification must still drain owned native cleanup.
	if err := verifyLoadThread(response.Thread, id, cwd, false); err != nil {
		return err
	}
	if !sameLoadResumeThread(before.Thread, response.Thread) {
		return errors.New("resume changed native source identity")
	}
	if err := d.vault.Save(id, a, runtime.SessionSpec{Cwd: cwd}, response.Thread); err != nil {
		return err
	}
	resumed := &events.ResumedSession{SessionID: id, Capabilities: caps()}
	if err := d.loadEmit(h, id, "", "", nil, events.HistoryReplay{LoadID: loadID, Phase: "started", RetainedScope: "all_retained_main_history", ResumedSession: resumed}); err != nil {
		return err
	}
	// The verified start binds this source before any genuine live observations.
	startObserve()
	first, err := d.readLoad(readCtx, c, h, id, true)
	if err != nil {
		return err
	}
	second, err := d.readLoad(readCtx, c, h, id, false)
	if err != nil {
		return err
	}
	if first != second {
		return errors.New("native ordered history changed during verification")
	}
	after, err := c.ReadThread(readCtx, ct.ThreadReadParams{ThreadID: id, IncludeTurns: ct.Some(false)})
	if err != nil {
		return err
	}
	if err := verifyLoadThread(after.Thread, id, cwd, false); err != nil {
		return err
	}
	if !sameLoadThread(response.Thread, after.Thread) {
		return errors.New("native metadata changed during history read")
	}
	d.mu.Lock()
	healthy := s.err == nil && !s.active && !s.sending && !s.closing
	d.mu.Unlock()
	if !healthy {
		return errors.New("resumed binding became unsettled")
	}
	// Verification detects observed drift; it is not a cross-page atomic snapshot.
	return d.loadEmit(h, id, "", "", map[string]any{"atomic": false, "verification": "ordered_second_read"}, events.HistoryReplay{LoadID: loadID, Phase: "native_completed", RetainedScope: "all_retained_main_history", ResumedSession: resumed})
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
	base := events.EventBase{EventID: uuid.NewString(), Namespace: h.address.Namespace, SessionName: h.address.SessionName, SessionID: id, Time: time.Now().UTC(), ItemID: item, Replay: &events.ReplaySource{LoadID: h.id, NativeRecordID: item, NativeParentID: parent}, Meta: meta}
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
func (d *Driver) readLoad(ctx context.Context, c *sdk.Client, h *nativeLoad, id string, emit bool) ([32]byte, error) {
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
		turns, err := c.ListThreadTurns(ctx, ct.ThreadTurnsListParams{ThreadID: id, SortDirection: ct.Some(ct.SortDirectionAsc), ItemsView: ct.Some(ct.TurnItemsViewFull), Limit: ct.Some(uint32(100)), Cursor: cursor})
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
				items, err := c.ListThreadItems(ctx, ct.ThreadItemsListParams{ThreadID: id, TurnID: ct.Some(turn.ID), SortDirection: ct.Some(ct.SortDirectionAsc), Limit: ct.Some(uint32(100)), Cursor: ic})
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
	d.mu.Unlock()
	if unknown {
		return errors.New("native resume outcome unknown; original ownership retained")
	}
	if s == nil {
		return nil
	}
	return d.CloseSession(ctx, id)
}
