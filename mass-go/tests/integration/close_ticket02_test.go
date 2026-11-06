package integration_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func (s *entrypointSuite) TestTicket02CodexCloseByNameThroughCLI() {
	s.openCodexWithoutInput()
	s.ticket02CloseRef("codex", resource.SessionRef{Namespace: "demo", Name: "session"})
}

func (s *entrypointSuite) TestTicket02ClaudeCloseByIDThroughCLI() {
	ref, _, _ := s.open("claude", "basic")
	s.ticket02CloseRef("claude", ref)
}

func (s *entrypointSuite) TestTicket02CodexCloseByIDThroughCLI() {
	ref := s.openCodexWithoutInput()
	s.ticket02CloseRef("codex", ref)
}

func (s *entrypointSuite) openCodexWithoutInput() resource.SessionRef {
	path, work := identityOnlyScenario(s.T(), "codex-session")
	ref, _, _ := s.openPrepared("codex", mock, path, work)
	return ref
}

func (s *entrypointSuite) ticket02CloseRef(kind string, ref resource.SessionRef) {
	var closed resource.Session
	s.call("session.close", workflowParams{Session: ref}, &closed)
	s.Equal(events.SessionState("offline"), closed.Status.State)
	s.Empty(closed.Status.Reason)
	s.Zero(closed.Status.AppliedGeneration)
	s.Zero(closed.Status.RuntimeGeneration)
	s.Nil(closed.Status.Capabilities)
	s.Nil(closed.Status.EffectiveFeatures)

	var idempotent resource.Session
	s.call("session.close", workflowParams{Session: ref}, &idempotent)
	s.Equal(events.SessionState("offline"), idempotent.Status.State)

	// Close preserves both read paths and does not stop the owning Runtime.
	var current resource.Session
	s.call("session.get", workflowParams{Session: ref}, &current)
	s.Equal(events.SessionState("offline"), current.Status.State)
	s.Equal(closed.Metadata, current.Metadata)
	s.history(ref)
	var ownedRuntime resource.Runtime
	s.call("runtime.get", workflowParams{Name: "agent"}, &ownedRuntime)
	s.Equal("running", ownedRuntime.Status.State)

	args := []string{"--url", s.url, "-o", "json", "session", "prompt"}
	if ref.SessionID != "" {
		args = append(args, ref.SessionID)
	} else {
		args = append(args, ref.Name, "-n", ref.Namespace)
	}
	args = append(args, "--text", "must not dispatch")
	raw, err := exec.CommandContext(ctx(s.T()), ctl, args...).CombinedOutput()
	s.Require().Error(err, "prompt after close must fail: %s", raw)
	s.Contains(string(raw), `"code":"conflict"`)
	s.T().Logf("PASS %s close through CLI, HTTP, Service and file store; Runtime remains running", kind)
}

func TestTicket02ActiveCloseCallsSDKAndIsRepeatable(t *testing.T) {
	for _, kind := range []string{"codex", "claude"} {
		t.Run(kind, func(t *testing.T) {
			d, stream, _ := lifecycleDriver(t, kind, "acknowledged")
			readCtx, stop := context.WithCancel(context.Background())
			drained := make(chan struct{})
			go func() {
				defer close(drained)
				for {
					if _, err := stream.Next(readCtx); err != nil {
						return
					}
				}
			}()
			t.Cleanup(func() { stop(); <-drained })
			work, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			opened, err := d.OpenSession(ctx(t), runtime.Address{Namespace: "default", SessionName: "active"}, runtime.SessionSpec{Cwd: work})
			require.NoError(t, err)
			_, err = d.SendPrompt(ctx(t), opened.SessionID, runtime.Input{Content: []events.ContentBlock{events.Text("still running")}})
			require.NoError(t, err)

			require.NoError(t, d.CloseSession(ctx(t), opened.SessionID), "active/sending close must reach the SDK")
			require.NoError(t, d.CloseSession(ctx(t), opened.SessionID), "repeated SDK close must be idempotent")
			_, err = d.SendPrompt(ctx(t), opened.SessionID, runtime.Input{Content: []events.ContentBlock{events.Text("closed")}})
			require.Error(t, err)
		})
	}
}
