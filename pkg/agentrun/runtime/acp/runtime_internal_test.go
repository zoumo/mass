package acp

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"

	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
)

func TestBuildSeedSystemPrompt_AppendsGuard(t *testing.T) {
	seed := buildSeedSystemPrompt("role setup")
	if seed == "role setup" {
		t.Fatalf("expected guard to be appended")
	}
	if want := "protocol and role setup"; !strings.Contains(seed, want) {
		t.Fatalf("expected seed prompt to contain %q, got %q", want, seed)
	}
	if want := "Do NOT execute any commands"; !strings.Contains(seed, want) {
		t.Fatalf("expected seed prompt to contain %q, got %q", want, seed)
	}
	if want := "Wait for the next message"; !strings.Contains(seed, want) {
		t.Fatalf("expected seed prompt to contain %q, got %q", want, seed)
	}
}

// newManagerForSessionTest builds a Manager with the sessions map
// initialized, bypassing Create() (which needs a real ACP agent
// process). Lets unit tests exercise the session bookkeeping logic
// (NewSession map insert, EndSession map delete, Sessions snapshot,
// error paths) without spinning up an agent.
//
// The returned Manager has nil conn; methods that go through ACP
// (PromptSession etc.) return "agent not started".
func newManagerForSessionTest(t *testing.T) *Manager {
	t.Helper()
	return &Manager{
		logger:   slog.Default(),
		sessions: make(map[acp.SessionId]*sessionState),
	}
}

func TestSessionIDs_EmptyWhenNoneRegistered(t *testing.T) {
	m := newManagerForSessionTest(t)
	if got := m.SessionIDs(); len(got) != 0 {
		t.Fatalf("expected empty sessions, got %v", got)
	}
}

func TestSessionIDs_SnapshotIncludesRegistered(t *testing.T) {
	m := newManagerForSessionTest(t)
	m.sessions["sess-a"] = &sessionState{id: "sess-a", cwd: "/a"}
	m.sessions["sess-b"] = &sessionState{id: "sess-b", cwd: "/b"}
	m.sessionID = "sess-a"

	got := m.SessionIDs()
	if len(got) != 2 {
		t.Fatalf("expected 2 sessions, got %v", got)
	}
	strs := make([]string, len(got))
	for i, s := range got {
		strs[i] = string(s)
	}
	sort.Strings(strs)
	want := []string{"sess-a", "sess-b"}
	for i := range want {
		if strs[i] != want[i] {
			t.Fatalf("session %d: want %q got %q", i, want[i], strs[i])
		}
	}
}

func TestEndSession_RemovesFromMap(t *testing.T) {
	m := newManagerForSessionTest(t)
	m.sessionID = "initial"
	m.sessions["initial"] = &sessionState{id: "initial"}
	m.sessions["extra"] = &sessionState{id: "extra", cwd: "/x"}

	if err := m.EndSession("extra"); err != nil {
		t.Fatalf("end extra: %v", err)
	}
	if _, ok := m.sessions["extra"]; ok {
		t.Fatalf("extra session not removed from map")
	}
	if _, ok := m.sessions["initial"]; !ok {
		t.Fatalf("initial session unexpectedly removed")
	}
}

func TestEndSession_RefusesInitialSession(t *testing.T) {
	m := newManagerForSessionTest(t)
	m.sessionID = "initial"
	m.sessions["initial"] = &sessionState{id: "initial"}

	err := m.EndSession("initial")
	if err == nil {
		t.Fatalf("expected EndSession to refuse the initial session")
	}
	if !strings.Contains(err.Error(), "initial session") {
		t.Fatalf("expected error to mention initial session, got: %v", err)
	}
	if _, ok := m.sessions["initial"]; !ok {
		t.Fatalf("initial session removed despite error")
	}
}

func TestEndSession_UnknownSessionErrors(t *testing.T) {
	m := newManagerForSessionTest(t)
	m.sessionID = "initial"
	m.sessions["initial"] = &sessionState{id: "initial"}

	err := m.EndSession("never-existed")
	if err == nil {
		t.Fatalf("expected EndSession to error on unknown session")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected error to mention 'not found', got: %v", err)
	}
}

func TestPromptSession_RejectsBeforeAgentStarted(t *testing.T) {
	// Manager with no conn (Create never ran). PromptSession should
	// return a clear error rather than panic on nil conn — protects
	// callers that race or skip Create().
	m := newManagerForSessionTest(t)
	m.sessions["known"] = &sessionState{id: "known"}

	_, err := m.PromptSession(context.Background(), "known",
		[]acp.ContentBlock{acp.TextBlock("hi")})
	if err == nil {
		t.Fatalf("expected error from PromptSession with nil conn")
	}
	if !strings.Contains(err.Error(), "agent not started") {
		t.Fatalf("expected 'agent not started' error, got: %v", err)
	}
}

func TestCancelSession_RejectsBeforeAgentStarted(t *testing.T) {
	m := newManagerForSessionTest(t)
	m.sessions["known"] = &sessionState{id: "known"}

	err := m.CancelSession(context.Background(), "known")
	if err == nil {
		t.Fatalf("expected error from CancelSession with nil conn")
	}
	if !strings.Contains(err.Error(), "agent not started") {
		t.Fatalf("expected 'agent not started' error, got: %v", err)
	}
}

// TestEndSession_RefusesBusySession verifies that EndSession returns a
// busy error when a session has in-flight Prompt work (refcount > 0).
// Without this check, ending a session mid-prompt leaves the prompt
// running against a session no longer in the map — state.json bookkeeping
// drifts silently.
func TestEndSession_RefusesBusySession(t *testing.T) {
	m := newManagerForSessionTest(t)
	m.sessionID = "initial"
	m.sessions["initial"] = &sessionState{id: "initial"}
	m.sessions["busy"] = &sessionState{id: "busy", cwd: "/b", inflight: 1}

	err := m.EndSession("busy")
	if err == nil {
		t.Fatalf("expected EndSession to refuse a busy session")
	}
	if !strings.Contains(err.Error(), "busy") {
		t.Fatalf("expected error to mention 'busy', got: %v", err)
	}
	if _, ok := m.sessions["busy"]; !ok {
		t.Fatalf("busy session removed despite error")
	}
}

// TestEndSession_AllowsAfterRefcountClears verifies that a session can
// be ended once its in-flight refcount drops back to zero — the busy
// check is per-state, not permanent.
func TestEndSession_AllowsAfterRefcountClears(t *testing.T) {
	m := newManagerForSessionTest(t)
	m.sessionID = "initial"
	m.sessions["initial"] = &sessionState{id: "initial"}
	m.sessions["s"] = &sessionState{id: "s", cwd: "/s", inflight: 1}

	if err := m.EndSession("s"); err == nil {
		t.Fatalf("expected busy error first")
	}

	m.sessions["s"].inflight = 0 // prompt completed

	if err := m.EndSession("s"); err != nil {
		t.Fatalf("expected EndSession to succeed after refcount cleared: %v", err)
	}
	if _, ok := m.sessions["s"]; ok {
		t.Fatalf("session not removed after EndSession")
	}
}

// TestPromptSession_EmptySessionIDResolvesToInitial verifies the
// single-resolution-point convention: an empty sessionID is resolved to
// m.sessionID inside PromptSession (rather than at the Service or
// Manager.Prompt-shim layer). Reaching the "session not found" branch
// would mean the empty-string convention leaked through unresolved.
func TestPromptSession_EmptySessionIDResolvesToInitial(t *testing.T) {
	m := newManagerForSessionTest(t)
	m.sessionID = "initial"
	m.sessions["initial"] = &sessionState{id: "initial"}

	// nil conn so we get "agent not started" instead of nil-deref —
	// the point is to confirm we got past the session-lookup step,
	// which would have returned "session %q not found" for the literal
	// empty string had resolution not happened.
	_, err := m.PromptSession(context.Background(), "",
		[]acp.ContentBlock{acp.TextBlock("hi")})
	if err == nil {
		t.Fatalf("expected error (agent not started)")
	}
	if strings.Contains(err.Error(), "not found") {
		t.Fatalf("empty sessionID was not resolved to initial; got: %v", err)
	}
	if !strings.Contains(err.Error(), "agent not started") {
		t.Fatalf("expected 'agent not started', got: %v", err)
	}
}

// TestPhaseFromActivePrompts verifies that state.Phase reflects the
// process-wide activePrompts counter, not a single caller's local view.
// Before this fix, two concurrent PromptSessions racing on completion
// could leave Phase=Idle while one was still running.
func TestPhaseFromActivePrompts(t *testing.T) {
	m := newManagerForSessionTest(t)
	// Non-nil conn means the agent is alive — phaseFromActivePromptsLocked
	// only writes Phase when m.conn != nil (it cedes lifecycle to Kill
	// otherwise; see TestPhaseFromActivePromptsLocked_SkipsAfterKill).
	m.conn = &acp.ClientSideConnection{}

	cases := []struct {
		name   string
		active int
		want   apiruntime.Phase
	}{
		{"zero prompts → idle", 0, apiruntime.PhaseIdle},
		{"one prompt → running", 1, apiruntime.PhaseRunning},
		{"many prompts → running", 5, apiruntime.PhaseRunning},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m.activePrompts = tc.active
			var s apiruntime.State
			m.phaseFromActivePromptsLocked(&s)
			if s.Phase != tc.want {
				t.Fatalf("activePrompts=%d: want Phase=%q got %q", tc.active, tc.want, s.Phase)
			}
		})
	}
}

// TestPhaseFromActivePromptsLocked_SkipsAfterKill verifies that the apply
// callback leaves state.Phase untouched when m.conn is nil. This is the
// post-Kill state — Kill / clearSessions owns lifecycle phase (Stopped),
// and a late-firing PromptSession writeState (from a prompt that was in
// flight when Kill ran and finally errored on the dead pipe) must NOT
// clobber Stopped with Idle/Running.
func TestPhaseFromActivePromptsLocked_SkipsAfterKill(t *testing.T) {
	m := newManagerForSessionTest(t)
	m.conn = nil      // post-Kill: clearSessions nilled the conn
	m.activePrompts = 1 // would normally cause apply to write Running

	s := apiruntime.State{Phase: apiruntime.PhaseStopped}
	m.phaseFromActivePromptsLocked(&s)

	if s.Phase != apiruntime.PhaseStopped {
		t.Fatalf("Phase should remain Stopped after Kill, got %q", s.Phase)
	}
}

// TestClearSessions verifies that the bookkeeping reset on agent
// teardown (Kill / process exit) drops every field that could leave a
// stale view behind. Without this, Sessions() would return dead IDs and
// PromptSession would pass its conn != nil guard before failing on the
// dead pipe.
func TestClearSessions(t *testing.T) {
	m := newManagerForSessionTest(t)
	m.sessionID = "initial"
	m.sessions["initial"] = &sessionState{id: "initial"}
	m.sessions["extra"] = &sessionState{id: "extra"}
	m.activePrompts = 2
	m.models = &acp.SessionModelState{}
	// conn is left nil — clearing a nil conn is a no-op, which is the
	// branch we exercise here. The non-nil-clearing path is exercised
	// indirectly via Kill()'s integration with a real process.

	m.clearSessions()

	if len(m.sessions) != 0 {
		t.Errorf("sessions not cleared: %v", m.sessions)
	}
	if m.sessionID != "" {
		t.Errorf("sessionID not cleared: %q", m.sessionID)
	}
	if m.activePrompts != 0 {
		t.Errorf("activePrompts not cleared: %d", m.activePrompts)
	}
	if m.models != nil {
		t.Errorf("models not cleared: %v", m.models)
	}
	if m.conn != nil {
		t.Errorf("conn not cleared: %v", m.conn)
	}
}

// TestDecrementPromptInflight_GuardsAgainstNegativeAfterClear pins the
// guard against a PromptSession defer racing with Kill: clearSessions
// resets activePrompts to 0, then the still-in-flight prompt's deferred
// cleanup runs. Without the > 0 check the counter would slip to -1 and
// stay there until next clearSessions; with it, the decrement is a
// no-op and the counter remains coherent.
func TestDecrementPromptInflight_GuardsAgainstNegativeAfterClear(t *testing.T) {
	m := newManagerForSessionTest(t)
	m.sessionID = "initial"
	m.sessions["initial"] = &sessionState{id: "initial"}

	// Simulate the post-clearSessions state: counters reset, session map
	// emptied (the in-flight prompt's session is already gone).
	m.activePrompts = 0
	delete(m.sessions, "initial")

	// Run the deferred cleanup that PromptSession would have queued.
	m.decrementPromptInflight("initial")

	if m.activePrompts != 0 {
		t.Errorf("activePrompts should stay at 0 after racing clearSessions, got %d", m.activePrompts)
	}
}

// TestDecrementPromptInflight_NormalPath verifies that the happy-path
// decrement (no race) still works — sess.inflight and m.activePrompts
// both go down by one.
func TestDecrementPromptInflight_NormalPath(t *testing.T) {
	m := newManagerForSessionTest(t)
	m.sessionID = "initial"
	m.sessions["initial"] = &sessionState{id: "initial", inflight: 1}
	m.activePrompts = 1

	m.decrementPromptInflight("initial")

	if got := m.sessions["initial"].inflight; got != 0 {
		t.Errorf("session inflight should be 0, got %d", got)
	}
	if m.activePrompts != 0 {
		t.Errorf("activePrompts should be 0, got %d", m.activePrompts)
	}
}
