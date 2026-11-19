package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

// The real daemon, CLI, Service, Drivers and pinned SDK use an owned native
// protocol peer. No model or external channel is called.
func injectDaemon(t *testing.T, kind string, version ...string) (*client.Client, string, *configNativePeer, resource.SessionRef) {
	return injectDaemonWithShutdown(t, kind, "", version...)
}

func injectDaemonWithShutdown(t *testing.T, kind, shutdownReason string, version ...string) (*client.Client, string, *configNativePeer, resource.SessionRef) {
	t.Helper()
	liveConfigBuild.Do(func() {
		liveConfigBinary = filepath.Join(root, "live-config-peer")
		out, err := exec.CommandContext(ctx(t), "go", "build", "-o", liveConfigBinary, "./testdata/live-config-peer").CombinedOutput()
		if err != nil {
			liveConfigBuildErr = fmt.Errorf("build peer: %w: %s", err, out)
		}
	})
	require.NoError(t, liveConfigBuildErr)
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	peer := newConfigNativePeer(t, kind, cwd, version...)
	var daemon *entrypointSuite
	if shutdownReason == "" {
		daemon = ticket08Daemon(t)
	} else {
		daemon = ticket08Daemon(t, shutdownReason)
	}
	c := newHTTPClient(t, daemon.url, "")
	t.Cleanup(func() { require.NoError(t, c.Close()) })
	r := resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: kind, Config: runtime.Config{"executable": liveConfigBinary, "envPolicy": "replace", "env": map[string]any{"CONFIG_PEER_ADDR": peer.listener.Addr().String(), "CLAUDE_CONFIG_DIR": t.TempDir()}}}}
	require.NoError(t, c.CreateRuntime(ctx(t), r, nil))
	var created resource.Session
	require.NoError(t, c.CreateSession(ctx(t), resource.Session{Metadata: resource.Metadata{Namespace: "one", Name: "context"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: cwd}}}, &created))
	ref := resource.SessionRef{SessionID: created.Status.SessionID}
	require.Eventually(t, func() bool {
		var v resource.Session
		err := c.GetSession(ctx(t), ref, &v)
		return err == nil && v.Status.State == events.Idle
	}, time.Second, time.Millisecond)
	return c, daemon.url, peer, ref
}

type injectResult struct {
	receipt runtime.InjectReceipt
	err     error
}

func injectAsync(t *testing.T, c *client.Client, ref resource.SessionRef) <-chan injectResult {
	t.Helper()
	done := make(chan injectResult, 1)
	go func() {
		var r runtime.InjectReceipt
		err := c.Inject(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("context only")}}, &r)
		done <- injectResult{r, err}
	}()
	return done
}

func injectResultOf(t *testing.T, done <-chan injectResult) injectResult {
	t.Helper()
	select {
	case r := <-done:
		return r
	case <-ctx(t).Done():
		t.Fatal("inject did not settle")
		return injectResult{}
	}
}

func injectProblem(t *testing.T, err error, code string) *runtime.OperationError {
	t.Helper()
	var p *runtime.OperationError
	require.ErrorAs(t, err, &p)
	require.Equal(t, code, p.Code)
	require.NotContains(t, p.Message, "SECRET")
	return p
}

func TestInjectNativeDaemonSuccess(t *testing.T) {
	for _, kind := range []string{"codex", "claude"} {
		t.Run(kind, func(t *testing.T) {
			c, url, peer, ref := injectDaemon(t, kind)
			for _, cli := range []bool{true, false} {
				var done <-chan injectResult
				var command *exec.Cmd
				var stdout, stderr bytes.Buffer
				var cliDone chan error
				if cli {
					command = exec.CommandContext(ctx(t), ctl, "--url", url, "-o", "json", "session", "inject", "context", "-n", "one", "--input-file", "-")
					command.Stdin = strings.NewReader(`{"content":[{"type":"text","text":"context only"}]}`)
					command.Stdout = &stdout
					command.Stderr = &stderr
					cliDone = make(chan error, 1)
					go func() { cliDone <- command.Run() }()
				} else {
					done = injectAsync(t, c, ref)
				}
				call := peer.next(t)
				expected := runtime.Acceptance{Sent: runtime.Yes()}
				if kind == "codex" {
					require.Equal(t, "thread/inject_items", call.method)
					require.Equal(t, ref.SessionID, call.params["threadId"])
					items := call.params["items"].([]any)
					require.Len(t, items, 1)
					require.Equal(t, map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "context only"}}}, items[0])
					call.respond(map[string]any{})
					expected.NativeAccepted = runtime.Yes()
				} else {
					require.Equal(t, "user", call.method)
					require.Equal(t, false, call.params["shouldQuery"])
					require.NotContains(t, call.params, "priority")
					require.NotContains(t, call.params, "uuid")
					require.Equal(t, "context only", call.params["message"].(map[string]any)["content"])
				}
				if cli {
					require.NoError(t, <-cliDone, stderr.String())
					var receipt runtime.InjectReceipt
					require.NoError(t, json.Unmarshal(stdout.Bytes(), &receipt))
					require.Equal(t, expected, receipt.Acceptance)
					require.NotContains(t, stdout.String(), "submission")
				} else {
					result := injectResultOf(t, done)
					require.NoError(t, result.err)
					require.Equal(t, expected, result.receipt.Acceptance)
				}
			}
			// No work identity was created, and no turn was implicitly requested.
			var canceled runtime.CancelReceipt
			require.NoError(t, c.Cancel(ctx(t), ref, &canceled))
			require.Equal(t, "noActiveWork", canceled.Result)
			require.Empty(t, canceled.Target)
			var current resource.Session
			require.NoError(t, c.GetSession(ctx(t), ref, &current))
			require.Equal(t, events.Idle, current.Status.State)
			require.NoError(t, c.CloseSession(ctx(t), ref, nil))
			r := injectResultOf(t, injectAsync(t, c, ref))
			injectProblem(t, r.err, "conflict")
			require.Equal(t, runtime.Bool(false), r.receipt.Acceptance.Sent)
			peer.mu.Lock()
			trace := strings.Join(peer.trace, "\n")
			peer.mu.Unlock()
			require.NotContains(t, trace, `"method":"turn/start"`)
			require.NotContains(t, trace, `"method":"turn/steer"`)
			t.Logf("synthetic %s native trace: %s", kind, trace)
		})
	}
}

func TestInjectNativeDaemonRejectionAndUnsupported(t *testing.T) {
	for _, code := range []int{-32601, -32602} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			c, url, peer, ref := injectDaemon(t, "codex")
			var stdout, stderr bytes.Buffer
			cmd := exec.CommandContext(ctx(t), ctl, "--url", url, "-o", "json", "session", "inject", ref.SessionID, "--text", "context only")
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			done := make(chan error, 1)
			go func() { done <- cmd.Run() }()
			call := peer.next(t)
			if code == -32602 {
				call.reject()
			} else {
				call.rejectCode(code)
			}
			require.Error(t, <-done)
			var receipt runtime.InjectReceipt
			require.NoError(t, json.Unmarshal(stdout.Bytes(), &receipt))
			require.Equal(t, runtime.Yes(), receipt.Acceptance.Sent)
			require.Equal(t, runtime.Bool(false), receipt.Acceptance.NativeAccepted)
			var p runtime.OperationError
			require.NoError(t, json.Unmarshal(stderr.Bytes(), &p))
			want := "native_rejected"
			if code == -32601 {
				want = "unsupported"
			}
			require.Equal(t, want, p.Code)
			require.NotContains(t, p.Message, "SECRET")
			// Explicit rejection leaves the input gate usable and never falls back.
			next := injectAsync(t, c, ref)
			peer.next(t).respond(map[string]any{})
			require.NoError(t, injectResultOf(t, next).err)
			require.NoError(t, c.CloseSession(ctx(t), ref, nil))
		})
	}
	t.Run("claude_old_version", func(t *testing.T) {
		c, _, peer, ref := injectDaemon(t, "claude", "2.0.0")
		result := injectResultOf(t, injectAsync(t, c, ref))
		injectProblem(t, result.err, "unsupported")
		require.Equal(t, runtime.Bool(false), result.receipt.Acceptance.Sent)
		require.NoError(t, c.CloseSession(ctx(t), ref, nil))
		require.Empty(t, peer.calls)
	})
}

func TestInjectNativeDaemonConcurrencyAndClosing(t *testing.T) {
	c, _, peer, ref := injectDaemon(t, "codex")
	first := injectAsync(t, c, ref)
	call := peer.next(t)
	second := injectResultOf(t, injectAsync(t, c, ref))
	p := injectProblem(t, second.err, "conflict")
	require.Equal(t, "input_in_flight", p.Reason)
	require.Equal(t, runtime.Bool(false), second.receipt.Acceptance.Sent)
	var prompt runtime.InputReceipt
	err := c.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("must not steer")}}, &prompt)
	injectProblem(t, err, "conflict")
	closeDone := make(chan error, 1)
	go func() { closeDone <- c.CloseSession(ctx(t), ref, nil) }()
	require.Eventually(t, func() bool {
		var r resource.Session
		err := c.GetSession(ctx(t), ref, &r)
		return err == nil && r.Status.State == events.SessionState("closing")
	}, time.Second, time.Millisecond)
	r := injectResultOf(t, injectAsync(t, c, ref))
	injectProblem(t, r.err, "conflict")
	require.Equal(t, runtime.Bool(false), r.receipt.Acceptance.Sent)
	select {
	case err := <-closeDone:
		t.Fatalf("close settled before input: %v", err)
	default:
	}
	call.respond(map[string]any{})
	require.NoError(t, injectResultOf(t, first).err)
	require.NoError(t, <-closeDone)
	require.Empty(t, peer.calls)
}

func TestInjectNativeDaemonUnknown(t *testing.T) {
	c, _, peer, ref := injectDaemonWithShutdown(t, "codex", "session_observation_eof")
	first := injectAsync(t, c, ref)
	peer.next(t).disconnect()
	result := injectResultOf(t, first)
	injectProblem(t, result.err, "outcome_unknown")
	require.Equal(t, runtime.Yes(), result.receipt.Acceptance.Sent)
	require.Nil(t, result.receipt.Acceptance.NativeAccepted)
	next := injectResultOf(t, injectAsync(t, c, ref))
	require.Error(t, next.err)
	require.Empty(t, peer.calls, "unknown input must not be retried")
	t.Logf("unknown receipt=%+v error=%v; later input=%v", result.receipt, result.err, next.err)
}

func TestInjectNativeDaemonPromptExcludesInject(t *testing.T) {
	c, _, peer, ref := injectDaemon(t, "codex")
	done := make(chan error, 1)
	go func() {
		var r runtime.InputReceipt
		done <- c.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("work")}}, &r)
	}()
	call := peer.next(t)
	require.Equal(t, "turn/start", call.method)
	injected := injectResultOf(t, injectAsync(t, c, ref))
	p := injectProblem(t, injected.err, "conflict")
	require.Equal(t, "input_in_flight", p.Reason)
	require.Equal(t, runtime.Bool(false), injected.receipt.Acceptance.Sent)
	call.respond(map[string]any{"turn": map[string]any{"id": "turn-work", "status": "inProgress", "items": []any{}}})
	require.NoError(t, <-done)
	require.NoError(t, c.CloseSession(ctx(t), ref, nil))
	require.Empty(t, peer.calls)
}
