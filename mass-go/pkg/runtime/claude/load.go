package claude

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/google/uuid"
	sdk "github.com/zoumo/agent-sdk-go/claude"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

type nativeLoad struct {
	id              string
	cancel          context.CancelFunc
	done            chan struct{}
	cleanup         sync.Mutex
	s               *session
	unknown         bool
	read            context.Context
	source          events.Source
	address         runtime.Address
	config, project string
	coverage        sdk.SessionMessagesCoverage
	queries         int
}

func (d *Driver) LoadSession(ctx context.Context, id string, a runtime.Address, cwd, loadID string) (result error) {
	read, cancel := context.WithCancel(ctx)
	h := &nativeLoad{id: loadID, cancel: cancel, done: make(chan struct{}), read: read, source: events.NewSource(), address: a, config: d.options.Env["CLAUDE_CONFIG_DIR"], project: cwd}
	d.mu.Lock()
	if !d.started || d.stopped || d.sessions[id] != nil || d.loads[id] != nil || len(d.sessions)+d.opening >= 128 {
		d.mu.Unlock()
		cancel()
		return runtime.FailedPrecondition("load unavailable")
	}
	if d.loads == nil {
		d.loads = map[string]*nativeLoad{}
	}
	d.loads[id] = h
	d.opening++
	d.mu.Unlock()
	defer func() {
		if result != nil {
			phase := "failed"
			if errors.Is(result, context.Canceled) {
				phase = "canceled"
			}
			meta := claudeReadEvidence(h)
			var query *sdk.SessionQueryError
			if errors.As(result, &query) {
				meta["queryErrorKind"] = query.Kind
			}
			_ = d.loadEmit(h, id, "", "", meta, events.HistoryReplay{LoadID: loadID, Phase: phase, RetainedScope: "all_retained_main_history", Error: map[string]any{"code": "unavailable", "reason": "native_history_failed", "message": "Native history read did not complete"}})
		}
		d.mu.Lock()
		d.opening--
		d.mu.Unlock()
		cancel()
		close(h.done)
	}()
	// Never consult inherited HOME/CLAUDE_CONFIG_DIR for an import source.
	if !filepath.IsAbs(h.config) || !filepath.IsAbs(cwd) {
		return errors.New("explicit absolute config and project required")
	}
	if err := d.vault.Check(); err != nil {
		return err
	}
	before, err := readClaudeHistory(read, h, id, 0, 10000)
	if err != nil {
		return err
	}
	d.mu.Lock()
	h.unknown = true
	d.mu.Unlock()
	native, err := sdk.Open(read, sdk.SessionOptions{ResumeID: id, CLIPath: d.options.Executable, WorkDir: cwd, Env: d.options.Environment("claude")})
	if err != nil {
		return err
	}
	s := &session{client: native, id: id, address: a, source: h.source, ready: make(chan struct{}), done: make(chan struct{}), tools: map[string]string{}, results: map[string]string{}, submitted: map[string]bool{}, attribution: "unknown", requireLink: true}
	d.mu.Lock()
	h.s = s
	h.unknown = false
	d.sessions[id] = s
	d.mu.Unlock()
	close(s.ready)
	observing := false
	observe := func() {
		if !observing {
			observing = true
			go d.observe(s, nil)
		}
	}
	defer observe() // Failed verification still drains its owned process during cleanup.
	for {
		r, receiveErr := native.Receive(read)
		if receiveErr != nil {
			return receiveErr
		}
		if r.Event == nil {
			continue
		}
		e := r.Event
		if e.Type != "system" || e.Subtype != "init" {
			return errors.New("unexpected native content before verified resume")
		}
		observed, known := native.SessionID()
		if !known || observed != id || e.SessionID != id || native.RequestedResumeID() != id {
			return errors.New("native resume identity mismatch")
		}
		value, decodeErr := decode(e.Raw)
		if decodeErr != nil {
			return decodeErr
		}
		init, ok := value.(map[string]any)
		if !ok || init["cwd"] != cwd {
			return errors.New("native resume project mismatch")
		}
		break
	}
	baseline, err := readClaudeHistory(read, h, id, 0, 10000)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(before.Messages, baseline.Messages) {
		return errors.New("native retained history changed during resume")
	}
	if err := d.vault.Save(id, a, runtime.SessionSpec{Cwd: cwd}, map[string]string{"id": id, "cwd": cwd}); err != nil {
		return err
	}
	resumed := &events.ResumedSession{SessionID: id, Capabilities: caps()}
	if err := d.loadEmit(h, id, "", "", map[string]any{"requestedSessionId": id, "observedSessionId": s.id}, events.HistoryReplay{LoadID: loadID, Phase: "started", RetainedScope: "all_retained_main_history", ResumedSession: resumed}); err != nil {
		return err
	}
	observe()
	tools := map[string]retainedTool{}
	// Each offset query scans the full bounded source. Ordered raw+UUID comparison
	// detects observed drift; separate calls have no atomic snapshot guarantee.
	for offset := 0; ; offset += 100 {
		page, err := readClaudeHistory(read, h, id, offset, 100)
		if err != nil {
			return err
		}
		end := min(offset+100, len(baseline.Messages))
		if offset > len(baseline.Messages) || !sameClaudeCoverage(baseline.Coverage, page.Coverage) || !reflect.DeepEqual(page.Messages, baseline.Messages[offset:end]) {
			return errors.New("native ordered history changed across offset reads")
		}
		for _, m := range page.Messages {
			if err := d.loadMessage(h, id, m, tools, offset); err != nil {
				return err
			}
		}
		if len(page.Messages) < 100 {
			break
		}
		if offset+100 >= 10000 {
			tail, err := readClaudeHistory(read, h, id, offset+100, 1)
			if err != nil {
				return err
			}
			if len(tail.Messages) != 0 || !sameClaudeCoverage(baseline.Coverage, tail.Coverage) {
				return errors.New("native history exceeds materialization bound or changed")
			}
			break
		}
	}
	final, err := readClaudeHistory(read, h, id, 0, 10000)
	if err != nil {
		return err
	}
	if !sameClaudeCoverage(baseline.Coverage, final.Coverage) || !reflect.DeepEqual(baseline.Messages, final.Messages) {
		return errors.New("native ordered history changed during delivery")
	}
	s.mu.Lock()
	healthy := s.err == nil && !s.active && !s.sending && !s.closing && s.client.Err() == nil
	s.mu.Unlock()
	if !healthy {
		return errors.New("resumed binding became unsettled")
	}
	meta := claudeReadEvidence(h)
	meta["verification"] = "ordered_raw_uuid_revalidation"
	return d.loadEmit(h, id, "", "", meta, events.HistoryReplay{LoadID: loadID, Phase: "native_completed", RetainedScope: "all_retained_main_history", ResumedSession: resumed})
}

func readClaudeHistory(ctx context.Context, h *nativeLoad, id string, offset, limit int) (sdk.SessionMessagesPage, error) {
	page, err := sdk.GetSessionMessages(ctx, id, sdk.SessionMessagesOptions{Scope: sdk.SessionQueryScope{ConfigDir: h.config, ProjectDir: h.project, MaxEntries: 10000}, Offset: offset, Limit: limit, MaxMessages: 10000, MaxBytes: 64 << 20, MaxLines: 100000, MaxRecords: 100000, MaxLineBytes: 8 << 20})
	h.coverage = page.Coverage
	h.queries++
	if err != nil {
		return page, err
	}
	if page.Coverage.Status != "complete" || page.Coverage.Atomic || page.Coverage.SessionID != id || page.Coverage.ConfigDir != h.config || page.Coverage.ProjectDir != h.project {
		return page, errors.New("native source coverage unverified")
	}
	for _, m := range page.Messages {
		if m.SessionID != id || m.UUID == "" {
			return page, errors.New("native record identity mismatch")
		}
		v, err := decode(m.Raw)
		if err != nil {
			return page, err
		}
		raw, ok := v.(map[string]any)
		if !ok {
			return page, errors.New("native record invalid")
		}
		if cwd, present := raw["cwd"]; present && cwd != h.project {
			return page, errors.New("native record project mismatch")
		}
	}
	return page, nil
}

// Keep partial scan facts without leaking SDK error Raw, Path or OS diagnostics.
func claudeReadEvidence(h *nativeLoad) map[string]any {
	c := h.coverage
	return map[string]any{"atomic": false, "scan": "full_file_per_call", "queries": h.queries, "coverage": map[string]any{"status": c.Status, "bytes": c.Bytes, "lines": c.Lines, "records": c.Records, "entries": c.Entries}}
}

func sameClaudeCoverage(a, b sdk.SessionMessagesCoverage) bool {
	a.Limits = sdk.SessionMessagesOptions{}
	b.Limits = sdk.SessionMessagesOptions{}
	return reflect.DeepEqual(a, b)
}

func (d *Driver) loadEmit(h *nativeLoad, id, item, parent string, meta map[string]any, p events.Payload) error {
	if meta == nil {
		meta = map[string]any{}
	}
	base := events.EventBase{EventID: uuid.NewString(), Namespace: h.address.Namespace, SessionName: h.address.SessionName, SessionID: id, ItemID: item, Time: time.Now().UTC(), Meta: meta, Replay: &events.ReplaySource{LoadID: h.id, NativeRecordID: item, NativeParentID: parent}}
	if nativeTime, ok := meta["nativeTime"].(string); ok {
		at, err := time.Parse(time.RFC3339Nano, nativeTime)
		if err != nil {
			return err
		}
		base.Time = at
	}
	e, err := events.NewEvent(base, p, h.source)
	if err != nil {
		return err
	}
	ctx := h.read
	if marker, ok := p.(events.HistoryReplay); ok && (marker.Phase == "failed" || marker.Phase == "canceled") {
		ctx = context.Background()
	}
	return d.pipe.Send(ctx, e)
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
	unknown, s := h.unknown, h.s
	d.mu.Unlock()
	if unknown {
		return errors.New("native resume outcome unknown; original ownership retained")
	}
	if s == nil {
		return nil
	}
	return d.CloseSession(ctx, id)
}
