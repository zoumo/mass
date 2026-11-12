package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/codex"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

func TestTicket18StructuredApproval(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "ticket18-peer")
	out, err := exec.CommandContext(ctx(t), "go", "build", "-o", bin, "./testdata/ticket18-approval-peer").CombinedOutput()
	require.NoError(t, err, string(out))
	binary, err := os.ReadFile(bin)
	require.NoError(t, err)
	t.Logf("owned peer artifact sha256=%x", sha256.Sum256(binary))
	decision := `{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["printf","ticket18-safe"]}}`
	t.Run("execpolicy", func(t *testing.T) {
		ticket18Roundtrip(t, bin, `["accept",`+decision+`,"cancel"]`, decision, "execution policy", "allow_always")
	})
	for _, scenario := range []struct{ name, decision, label, kind string }{
		{"network-allow", `{"applyNetworkPolicyAmendment":{"network_policy_amendment":{"host":"safe.example.test","action":"allow"}}}`, "network policy", "allow_always"},
		{"network-deny", `{"applyNetworkPolicyAmendment":{"network_policy_amendment":{"host":"safe.example.test","action":"deny"}}}`, "network policy", "reject_always"},
		{"accept-string", `"accept"`, "Allow once", "allow_once"},
		{"session-string", `"acceptForSession"`, "Allow for this session", "allow_always"},
		{"decline-string", `"decline"`, "Reject once", "reject_once"},
		{"cancel-string", `"cancel"`, "Cancel", "reject_once"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			first := `"accept"`
			if scenario.decision == `"accept"` {
				first = `"decline"`
			}
			ticket18Roundtrip(t, bin, `[`+first+`,`+scenario.decision+`]`, scenario.decision, scenario.label, scenario.kind)
		})
	}
	// Distinct amendments of the same variant must not overwrite each other or
	// collide with the backward-compatible string option IDs.
	t.Run("structured-only", func(t *testing.T) {
		ticket18Roundtrip(t, bin, `[{"applyNetworkPolicyAmendment":{"network_policy_amendment":{"action":"deny","host":"safe.example.test"}}},`+decision+`]`, decision, "execution policy", "allow_always")
	})
	t.Run("distinct-amendments", func(t *testing.T) {
		other := `{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["printf","other-scope"]}}`
		ticket18Roundtrip(t, bin, `[`+other+`,`+decision+`,"accept","acceptForSession","decline","cancel"]`, decision, "execution policy", "allow_always")
	})
	for _, scenario := range []struct{ name, choices, decision, label, kind string }{
		{"unknown-member", `["accept",{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["printf"],"futureScope":true}}]`, `{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["printf"],"futureScope":true}}`, "execution policy", "allow_always"},
		{"unknown-outer-member", `["accept",{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["printf"]},"futureScope":true}]`, `{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["printf"]},"futureScope":true}`, "execution policy", "allow_always"},
		{"unknown-policy-member", `["accept",{"applyNetworkPolicyAmendment":{"network_policy_amendment":{"host":"safe.example.test","action":"allow","futureScope":true}}}]`, `{"applyNetworkPolicyAmendment":{"network_policy_amendment":{"host":"safe.example.test","action":"allow","futureScope":true}}}`, "network policy", "allow_always"},
		{"unknown-network-body-member", `["accept",{"applyNetworkPolicyAmendment":{"network_policy_amendment":{"host":"safe.example.test","action":"allow"},"futureScope":true}}]`, `{"applyNetworkPolicyAmendment":{"network_policy_amendment":{"host":"safe.example.test","action":"allow"},"futureScope":true}}`, "network policy", "allow_always"},
		{"unknown-network-outer-member", `["accept",{"applyNetworkPolicyAmendment":{"network_policy_amendment":{"host":"safe.example.test","action":"allow"}},"futureScope":true}]`, `{"applyNetworkPolicyAmendment":{"network_policy_amendment":{"host":"safe.example.test","action":"allow"}},"futureScope":true}`, "network policy", "allow_always"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ticket18Roundtrip(t, bin, scenario.choices, scenario.decision, scenario.label, scenario.kind)
		})
	}
	for _, scenario := range []struct{ name, choices, reason string }{
		{"unknown-string", `["accept","futureApprove"]`, "permission_option_unsupported"},
		{"unknown-variant", `["accept",{"futureApproval":{}}]`, "permission_option_unsupported"},
		{"unknown-action", `["accept",{"applyNetworkPolicyAmendment":{"network_policy_amendment":{"host":"safe.example.test","action":"future"}}}]`, "permission_option_unsupported"},
		{"missing-amendment", `["accept",{"acceptWithExecpolicyAmendment":{}}]`, "invalid_native_request"},
		{"null-amendment", `["accept",{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":null}}]`, "invalid_native_request"},
		{"wrong-type", `["accept",{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":[1]}}]`, "invalid_native_request"},
		{"null-element", `["accept",{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":[null]}}]`, "invalid_native_request"},
		{"missing-host", `["accept",{"applyNetworkPolicyAmendment":{"network_policy_amendment":{"action":"allow"}}}]`, "invalid_native_request"},
		{"null-host", `["accept",{"applyNetworkPolicyAmendment":{"network_policy_amendment":{"action":"allow","host":null}}}]`, "invalid_native_request"},
		{"ambiguous-variant", `[{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":[]},"applyNetworkPolicyAmendment":{"network_policy_amendment":{"action":"allow","host":"safe.example.test"}}}]`, "invalid_native_request"},
		{"empty", `[]`, "permission_option_unsupported"},
		{"invalid-kind", `["accept",true]`, "invalid_native_request"},
		{"null-choice", `["accept",null]`, "invalid_native_request"},
		{"duplicate-string", `["accept","accept"]`, "permission_option_unsupported"},
		{"duplicate-structured", `[` + decision + `,` + decision + `]`, "permission_option_unsupported"},
		{"duplicate-reordered-policy", `[{"applyNetworkPolicyAmendment":{"network_policy_amendment":{"action":"allow","host":"safe.example.test"}}},{"applyNetworkPolicyAmendment":{"network_policy_amendment":{"host":"safe.example.test","action":"allow"}}}]`, "permission_option_unsupported"},
		{"duplicate-json-member", `[{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["printf"],"execpolicy_amendment":["other"]}}]`, "invalid_native_request"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ticket18Rejected(t, bin, scenario.choices, scenario.reason)
		})
	}
}

func ticket18Roundtrip(t *testing.T, bin, choices, decision, name, kind string) {
	t.Helper()
	ticket18RoundtripCase(t, bin, choices, decision, name, kind, "")
}

func ticket18Rejected(t *testing.T, bin, choices, reason string) {
	t.Helper()
	ticket18RoundtripCase(t, bin, choices, "", "", "", reason)
}

func ticket18RoundtripCase(t *testing.T, bin, choices, decision, name, kind, rejectReason string) {
	t.Helper()
	dir := t.TempDir()
	st, err := file.Open(filepath.Join(dir, "store"), false)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, st.Close()) })
	reg := runtime.NewRegistry(map[string]runtime.Factory{"codex": codex.Factory{RecoveryDir: filepath.Join(dir, "recovery")}})
	svc := service.NewWithPolicy(st, st, reg, nil)
	// Independent finalizers: a failed shutdown assertion must not skip store close
	// or process readback, including on the deliberately failing RED run.
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, stopErr := svc.StopRuntime(stopCtx, "agent")
		stopCancel()
		assert.NoError(t, stopErr)
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		assert.NoError(t, svc.Shutdown(shutdownCtx))
		shutdownCancel()
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		pid, readErr := os.ReadFile(filepath.Join(dir, "pid"))
		if assert.NoError(t, readErr) {
			out, psErr := exec.CommandContext(cleanup, "ps", "-p", strings.TrimSpace(string(pid)), "-o", "pid=,lstart=,state=,args=").Output()
			var exitErr *exec.ExitError
			// Only a completed ps no-match proves absence; observation failure does not.
			reaped := cleanup.Err() == nil && errors.As(psErr, &exitErr) && exitErr.ExitCode() == 1 && len(out) == 0
			assert.True(t, reaped, "owned peer must be reaped, not merely signaled: ps err=%v context=%v output=%q", psErr, cleanup.Err(), out)
			t.Logf("owned peer pid=%s reaped=%t", pid, reaped)
		}
	})
	_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "codex", Config: runtime.Config{"executable": bin, "envPolicy": "replace", "env": map[string]any{"EVIDENCE": dir, "CHOICES": choices}}}})
	require.NoError(t, err)
	_, err = svc.StartRuntime(ctx(t), "agent")
	require.NoError(t, err)
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	opened, err := svc.CreateSession(ctx(t), resource.Session{Metadata: resource.Metadata{Namespace: "ticket18", Name: "approval"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: cwd}}})
	require.NoError(t, err)
	ref := resource.SessionRef{SessionID: opened.Status.SessionID}
	pid, err := os.ReadFile(filepath.Join(dir, "pid"))
	require.NoError(t, err)
	identity, err := exec.CommandContext(ctx(t), "ps", "-p", strings.TrimSpace(string(pid)), "-o", "pid=,lstart=,state=,args=").Output()
	require.NoError(t, err)
	require.Contains(t, string(identity), bin)
	t.Logf("owned live peer identity: %s", identity)
	handler, err := transport.New(svc, withTestWriteTimeout(transport.Options{Token: "synthetic"}))
	require.NoError(t, err)
	t.Cleanup(handler.Shutdown)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := newHTTPClient(t, server.URL, "synthetic")
	watch, err := client.Subscribe(ctx(t), ref)
	require.NoError(t, err)
	t.Cleanup(func() { _ = watch.Close() })
	_, err = svc.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("authored offline approval")}})
	require.NoError(t, err)
	answered := false
	childAnswered := false
	terminal := false
	var interaction string
	expectRejected := rejectReason != ""
	var childRejected, childNotice, wrongWorkRejected, retiredSeen bool
	seenInteractions := map[string]bool{}
	codexMeta := func(e events.SessionEvent) map[string]any {
		meta, _ := e.Meta["codex"].(map[string]any)
		return meta
	}
approvalLoop:
	for {
		e, err := watch.Next(ctx(t))
		require.NoError(t, err)
		if p, ok := e.Payload().(events.SessionError); ok {
			meta := codexMeta(e)
			itemID, _ := meta["itemId"].(string)
			switch {
			case p.Reason == "child_thread_notification_unsupported":
				require.Equal(t, "thread/started", meta["method"])
				require.Equal(t, "approval-child", meta["sourceThreadId"])
				childNotice = true
				continue
			case expectRejected && itemID == "foreign-request":
				require.Equal(t, "foreign_thread_request", p.Reason)
				require.Equal(t, true, meta["requestRejected"])
				continue
			case expectRejected && (itemID == "child-request" || itemID == "wrong-work-request" || itemID == "approval-request"):
				require.Equal(t, rejectReason, p.Reason)
				require.Equal(t, true, meta["requestRejected"])
				switch itemID {
				case "child-request":
					require.False(t, childRejected)
					childRejected = true
					continue
				case "wrong-work-request":
					require.False(t, wrongWorkRejected)
					wrongWorkRejected = true
					continue
				case "approval-request":
					require.True(t, childRejected)
					require.True(t, wrongWorkRejected)
					require.True(t, childNotice)
					require.False(t, seenInteractions["child-request"], "rejected choices must not expose the child request")
					require.False(t, seenInteractions["approval-request"], "rejected choices must not expose the root request")
					_, statErr := os.Stat(filepath.Join(dir, "answer.json"))
					require.ErrorIs(t, statErr, os.ErrNotExist, "rejected choices must not dispatch any native answer")
					require.NoError(t, client.CloseSession(ctx(t), ref, nil))
					return
				}
			case !expectRejected && p.Reason == "stale_native_request" && itemID == "wrong-work-request":
				require.Equal(t, true, meta["requestRejected"])
				wrongWorkRejected = true
				continue
			case !expectRejected && p.Reason == "stale_native_request" && itemID == "retired-request":
				require.True(t, terminal, "retired request is ordered after native terminal settlement")
				require.Equal(t, true, meta["requestRejected"])
				retiredSeen = true
				require.True(t, childAnswered, "confirmed child approval must be answerable through its root session")
				require.True(t, childNotice, "known child-thread notification gap must be surfaced")
				require.True(t, wrongWorkRejected, "wrong-work request must be rejected")
				require.False(t, seenInteractions["foreign-request"], "foreign request must not be exposed")
				require.False(t, seenInteractions["retired-request"], "retired request must not be exposed")
				break approvalLoop
			}
			t.Fatalf("unexpected session error: %+v item=%s meta=%+v", p, itemID, meta)
		}
		if p, ok := e.Payload().(events.InteractionRequested); ok {
			require.False(t, seenInteractions[p.Request.ToolCallID], "interaction must be exposed once")
			seenInteractions[p.Request.ToolCallID] = true
			if p.Request.ToolCallID == "child-request" {
				require.False(t, childAnswered)
				require.NotEmpty(t, decision, "invalid offered choices must fail closed as a whole, not expose a filtered subset")
				require.Equal(t, "approval-child", codexMeta(e)["sourceThreadId"])
				require.Equal(t, "approval-session", codexMeta(e)["parentThreadId"])
				require.Equal(t, "child-turn", codexMeta(e)["turnId"])
				var receipt runtime.InteractionReceipt
				childAnswer := map[string]any{"outcome": "selected", "optionId": p.Request.Options[0].OptionID}
				require.NoError(t, client.Answer(ctx(t), ref, string(p.Interaction), childAnswer, &receipt))
				require.Equal(t, runtime.Yes(), receipt.Evidence.Sent)
				childAnswered = true
				continue
			}
			require.Equal(t, "approval-request", p.Request.ToolCallID, "foreign, stale and retired requests must not gain session authority")
			require.NotEmpty(t, decision, "invalid offered choices must fail closed as a whole, not expose a filtered subset")
			require.False(t, answered)
			interaction = string(p.Interaction)
			var offered []json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(choices), &offered))
			require.Len(t, p.Request.Options, len(offered))
			ids := map[string]bool{}
			for _, option := range p.Request.Options {
				require.False(t, ids[option.OptionID], "every offered choice needs distinct authority")
				ids[option.OptionID] = true
			}
			require.Contains(t, p.Request.Options[1].Name, name)
			require.Equal(t, events.PermissionOptionKind(kind), p.Request.Options[1].Kind)
			if !ids["allow-once"] {
				require.Error(t, client.Answer(ctx(t), ref, string(p.Interaction), map[string]any{"outcome": "selected", "optionId": "allow-once"}, nil), "no default accept may be invented when only structured decisions were offered")
			}
			_, err := os.Stat(filepath.Join(dir, "answer.json"))
			require.ErrorIs(t, err, os.ErrNotExist, "publishing an option must never auto-approve it")
			var receipt runtime.InteractionReceipt
			valid := map[string]any{"outcome": "selected", "optionId": p.Request.Options[1].OptionID}
			for _, invalid := range []map[string]any{
				{"outcome": "selected", "optionId": "not-offered"},
				{"outcome": "selected", "optionId": p.Request.Options[1].OptionID, "updatedInput": map[string]any{"command": "widened"}},
				{"outcome": "selected", "optionId": p.Request.Options[1].OptionID, "decision": "accept"},
				{"outcome": "cancelled", "optionId": p.Request.Options[1].OptionID}, //nolint:misspell // Public permission protocol spelling.
			} {
				require.Error(t, client.Answer(ctx(t), ref, string(p.Interaction), invalid, nil))
			}
			require.Error(t, client.Answer(ctx(t), resource.SessionRef{SessionID: "foreign-session"}, string(p.Interaction), valid, nil))
			driver, err := reg.Get("agent")
			require.NoError(t, err)
			canceled, cancel := context.WithCancel(context.Background())
			cancel()
			_, err = driver.AnswerInteraction(canceled, ref.SessionID, string(p.Interaction), valid)
			require.Error(t, err, "NotSent must retain the still-live native capability")
			require.NoError(t, client.Answer(ctx(t), ref, string(p.Interaction), valid, &receipt))
			require.Equal(t, runtime.Yes(), receipt.Evidence.Sent)
			require.Nil(t, receipt.Evidence.NativeAccepted)
			require.Error(t, client.Answer(ctx(t), ref, string(p.Interaction), valid, nil), "consumed authority cannot answer twice")
			answered = true
		}
		if p, ok := e.Payload().(events.StateUpdate); ok && p.State != nil && *p.State == events.Idle && answered {
			terminal = true
		}
	}
	require.True(t, retiredSeen)
	answer, err := os.ReadFile(filepath.Join(dir, "answer.json"))
	require.NoError(t, err)
	require.JSONEq(t, `{"decision":`+decision+`}`, string(answer), "native response must preserve the exact original typed decision and scope")
	var original map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(answer, &original))
	require.Len(t, original, 1)
	require.NoError(t, client.CloseSession(ctx(t), ref, nil))
	require.Error(t, client.Answer(ctx(t), ref, interaction, map[string]any{"outcome": "selected", "optionId": "allow-once"}, nil), "close must not resurrect old authority")
}
