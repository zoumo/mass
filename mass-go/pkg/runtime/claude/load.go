package claude

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/zoumo/agent-sdk-go/agent"
	nativeclaude "github.com/zoumo/agent-sdk-go/claude"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/agentrelay"
)

type nativeLoad struct {
	id      string
	cancel  context.CancelFunc
	done    chan struct{}
	cleanup sync.Mutex
	s       *session
	unknown bool
	read    context.Context
	source  events.Source
	address runtime.Address

	config, project string
	coverage        nativeclaude.SessionMessagesCoverage
	queries         int
}

type loadEventIdentity struct {
	sessionID      string
	itemID         string
	nativeRecordID string
	nativeParentID string
}

func (d *Driver) LoadSession(ctx context.Context, id string, address runtime.Address, spec runtime.SessionSpec, loadID string) (result error) {
	cwd := spec.Cwd
	read, cancel := context.WithCancel(ctx)
	config := d.options.Env["CLAUDE_CONFIG_DIR"]
	h := &nativeLoad{
		id:      loadID,
		cancel:  cancel,
		done:    make(chan struct{}),
		read:    read,
		source:  events.NewSource(),
		address: address,
		config:  config,
		project: cwd,
	}
	d.mu.Lock()
	if !d.started || d.stopped || d.stopping || d.sessions[id] != nil || d.loads[id] != nil || len(d.sessions)+d.opening >= 128 {
		d.mu.Unlock()
		cancel()
		return runtime.FailedPrecondition("load unavailable")
	}
	if d.loads == nil {
		d.loads = map[string]*nativeLoad{}
	}
	d.loads[id] = h
	d.opening++
	d.loading++
	d.mu.Unlock()
	defer func() {
		if result != nil {
			phase := "failed"
			if errors.Is(result, context.Canceled) {
				phase = "canceled"
			}
			meta := claudeReadEvidence(h)
			var query *nativeclaude.SessionQueryError
			if errors.As(result, &query) {
				meta["queryErrorKind"] = query.Kind
			}
			_ = d.loadEmit(h, loadEventIdentity{sessionID: id}, meta, events.HistoryReplay{
				LoadID:        loadID,
				Phase:         phase,
				RetainedScope: "all_retained_main_history",
				Error:         map[string]any{"code": "unavailable", "reason": "native_history_failed", "message": "Native history read did not complete"},
			})
		}
		d.mu.Lock()
		d.opening--
		d.loading--
		d.mu.Unlock()
		cancel()
		close(h.done)
	}()

	// Never infer an import source from inherited HOME or CLAUDE_CONFIG_DIR.
	if !filepath.IsAbs(config) || !filepath.IsAbs(cwd) {
		return errors.New("explicit absolute config and project required")
	}
	if spec.SystemPrompt != "" || len(spec.AdditionalDirs) > 0 {
		return runtime.UnsupportedOption("system_prompt_or_additional_dirs_unsupported", "Claude runtime does not support system prompts or additional directories")
	}
	if err := d.vault.Check(); err != nil {
		return err
	}
	scope := nativeclaude.SessionQueryScope{ConfigDir: config, ProjectDir: cwd, MaxEntries: 10000}
	metadata, err := nativeclaude.GetSessionInfo(read, id, nativeclaude.SessionInfoOptions{
		Scope:       scope,
		MaxBytes:    16 << 20,
		MaxLines:    10000,
		SampleBytes: 64 << 10,
	})
	if err != nil {
		return err
	}
	if metadata.Coverage.Status != "complete" ||
		metadata.Coverage.ConfigDir != config ||
		metadata.Coverage.ProjectDir != cwd ||
		metadata.Info == nil ||
		metadata.Info.SessionID != id ||
		metadata.Info.Cwd == nil ||
		!runtime.EqualNativePath(*metadata.Info.Cwd, cwd) {
		return errors.New("native session metadata identity or project mismatch")
	}

	before, err := readClaudeHistory(read, h, id, 0, 10000)
	if err != nil {
		return err
	}
	effective := runtime.ResolveSessionFeatures(spec.Features, supportedSessionFeatures)
	servers, err := claudeMCPServers(spec.MCPServers, d.options)
	if err != nil {
		return err
	}
	h.unknown = true
	native, err := d.provider.Resume(read, agent.ResumeOptions{
		SessionID: id,
		OpenOptions: agent.OpenOptions{
			Cwd:        cwd,
			Features:   agent.Features{AgentQuestions: effective.AgentQuestions},
			MCPServers: servers,
		},
	})
	if err != nil {
		h.unknown = failedNativeCleanup(err)
		return mapClaudeError(err)
	}
	if native.ID() != id {
		cleanup := native.Close(context.Background())
		h.unknown = cleanup != nil
		return errors.Join(&runtime.OperationError{
			Code:    "native_rejected",
			Reason:  "resume_identity_mismatch",
			Message: "Claude resumed a different native session",
		}, mapClaudeError(cleanup))
	}
	s := &session{native: native, address: address, source: h.source}
	d.mu.Lock()
	if d.sessions[id] != nil {
		d.mu.Unlock()
		cleanup := native.Close(context.Background())
		h.unknown = cleanup != nil
		return errors.Join(errors.New("duplicate native identity"), mapClaudeError(cleanup))
	}
	d.sessions[id] = s
	h.s = s
	d.mu.Unlock()
	h.unknown = false
	if err := d.vault.Save(id, address, spec, map[string]string{"id": id, "cwd": cwd}); err != nil {
		return err
	}

	resumed := &events.ResumedSession{
		SessionID:         id,
		Capabilities:      agentrelay.MapCapabilities(native.Capabilities()),
		EffectiveFeatures: events.ResumedSessionFeatures{AgentQuestions: effective.AgentQuestionsEnabled()},
	}
	if err := d.loadEmit(h, loadEventIdentity{sessionID: id}, map[string]any{
		"requestedSessionId": id,
		"observedSessionId":  s.native.ID(),
	}, events.HistoryReplay{
		LoadID:         loadID,
		Phase:          "started",
		RetainedScope:  "all_retained_main_history",
		ResumedSession: resumed,
	}); err != nil {
		return err
	}
	if err := d.startRelay(s); err != nil {
		if cleanup := s.arcClose(); cleanup != nil {
			return errors.Join(err, cleanup)
		}
		d.mu.Lock()
		if d.sessions[id] == s {
			delete(d.sessions, id)
		}
		d.mu.Unlock()
		h.s = nil
		h.unknown = false
		return err
	}

	baseline, err := readClaudeHistory(read, h, id, 0, 10000)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(before.Messages, baseline.Messages) {
		return errors.New("native retained history changed during resume")
	}
	tools := map[string]retainedTool{}
	// Each offset query scans the full bounded source. Ordered raw+UUID comparison
	// detects observed drift; separate calls have no atomic snapshot guarantee.
	for offset := 0; ; offset += 100 {
		page, err := readClaudeHistory(read, h, id, offset, 100)
		if err != nil {
			return err
		}
		end := min(offset+100, len(baseline.Messages))
		if offset > len(baseline.Messages) ||
			!sameClaudeCoverage(baseline.Coverage, page.Coverage) ||
			!reflect.DeepEqual(page.Messages, baseline.Messages[offset:end]) {
			return errors.New("native ordered history changed across offset reads")
		}
		for _, message := range page.Messages {
			if err := d.loadMessage(h, id, message, tools, offset); err != nil {
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
	healthy := s.err == nil && !s.closing && native.Err() == nil && s.relay.Err() == nil
	s.mu.Unlock()
	if !healthy {
		return errors.New("resumed binding became unsettled")
	}
	meta := claudeReadEvidence(h)
	meta["verification"] = "ordered_raw_uuid_revalidation"
	return d.loadEmit(h, loadEventIdentity{sessionID: id}, meta, events.HistoryReplay{
		LoadID:         loadID,
		Phase:          "native_completed",
		RetainedScope:  "all_retained_main_history",
		ResumedSession: resumed,
	})
}

func readClaudeHistory(ctx context.Context, h *nativeLoad, id string, offset, limit int) (nativeclaude.SessionMessagesPage, error) {
	page, err := nativeclaude.GetSessionMessages(ctx, id, nativeclaude.SessionMessagesOptions{
		Scope:        nativeclaude.SessionQueryScope{ConfigDir: h.config, ProjectDir: h.project, MaxEntries: 10000},
		Offset:       offset,
		Limit:        limit,
		MaxMessages:  10000,
		MaxBytes:     64 << 20,
		MaxLines:     100000,
		MaxRecords:   100000,
		MaxLineBytes: 8 << 20,
	})
	h.coverage = page.Coverage
	h.queries++
	if err != nil {
		return page, err
	}
	if page.Coverage.Status != "complete" ||
		page.Coverage.Atomic ||
		page.Coverage.SessionID != id ||
		page.Coverage.ConfigDir != h.config ||
		page.Coverage.ProjectDir != h.project {
		return page, errors.New("native source coverage unverified")
	}
	for _, message := range page.Messages {
		if message.SessionID != id || message.UUID == "" {
			return page, errors.New("native record identity mismatch")
		}
		value, err := decode(message.Raw)
		if err != nil {
			return page, err
		}
		raw, ok := value.(map[string]any)
		if !ok {
			return page, errors.New("native record invalid")
		}
		if cwd, present := raw["cwd"]; present {
			value, ok := cwd.(string)
			if !ok || !runtime.EqualNativePath(value, h.project) {
				return page, errors.New("native record project mismatch")
			}
		}
	}
	return page, nil
}

// Keep partial scan facts without leaking SDK error Raw, Path or OS diagnostics.
func claudeReadEvidence(h *nativeLoad) map[string]any {
	coverage := h.coverage
	return map[string]any{
		"atomic":  false,
		"scan":    "full_file_per_call",
		"queries": h.queries,
		"coverage": map[string]any{
			"status":  coverage.Status,
			"bytes":   coverage.Bytes,
			"lines":   coverage.Lines,
			"records": coverage.Records,
			"entries": coverage.Entries,
		},
	}
}

func sameClaudeCoverage(left, right nativeclaude.SessionMessagesCoverage) bool {
	left.Limits = nativeclaude.SessionMessagesOptions{}
	right.Limits = nativeclaude.SessionMessagesOptions{}
	return reflect.DeepEqual(left, right)
}

func (d *Driver) loadEmit(h *nativeLoad, identity loadEventIdentity, meta map[string]any, payload events.Payload) error {
	if meta == nil {
		meta = map[string]any{}
	}
	base := events.EventBase{
		EventID:     uuid.NewString(),
		Namespace:   h.address.Namespace,
		SessionName: h.address.SessionName,
		SessionID:   identity.sessionID,
		ItemID:      identity.itemID,
		Time:        time.Now().UTC(),
		Meta:        meta,
		Replay: &events.ReplaySource{
			LoadID:         h.id,
			NativeRecordID: identity.nativeRecordID,
			NativeParentID: identity.nativeParentID,
		},
	}
	if nativeTime, ok := meta["nativeTime"].(string); ok {
		at, err := time.Parse(time.RFC3339Nano, nativeTime)
		if err != nil {
			return err
		}
		base.Time = at
	}
	event, err := events.NewEvent(base, payload, h.source)
	if err != nil {
		return err
	}
	ctx := h.read
	if marker, ok := payload.(events.HistoryReplay); ok && (marker.Phase == "failed" || marker.Phase == "canceled") {
		ctx = context.Background()
	}
	return d.pipe.Send(ctx, event)
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
	if h.unknown {
		return errors.New("native resume outcome unknown; original ownership retained")
	}
	if h.s == nil {
		return nil
	}
	return d.CloseSession(ctx, id)
}
