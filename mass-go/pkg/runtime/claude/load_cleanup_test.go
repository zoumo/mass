package claude

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

type loadCleanupSession struct {
	agent.Session
	id                     string
	eventsErr, closeErr    error
	eventCalls, closeCalls int
}

func (s *loadCleanupSession) ID() string { return s.id }

func (*loadCleanupSession) Capabilities() agent.Capabilities { return agent.Capabilities{} }

func (s *loadCleanupSession) Events() (agent.EventStream, error) {
	s.eventCalls++
	return nil, s.eventsErr
}

func (s *loadCleanupSession) Close(ctx context.Context) error {
	s.closeCalls++
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.closeErr
}

type loadCleanupProvider struct {
	agent.Core
	native   *loadCleanupSession
	onResume func()
	resumes  int
}

func (*loadCleanupProvider) Start(context.Context) (agent.Capabilities, error) {
	return agent.Capabilities{}, nil
}

func (*loadCleanupProvider) Stop(context.Context) error { return nil }

func (p *loadCleanupProvider) Resume(_ context.Context, options agent.ResumeOptions) (agent.Session, error) {
	p.resumes++
	if options.SessionID != p.native.id {
		return nil, errors.New("unexpected resume identity")
	}
	if p.onResume != nil {
		p.onResume()
	}
	return p.native, nil
}

func TestClaudeLoadPreRelayFailureCleanup(t *testing.T) {
	for _, phase := range []string{"recovery-save", "started-marker", "relay-claim", "relay-claim-close-failure"} {
		t.Run(phase, func(t *testing.T) {
			cwd, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			config, recoveryDir := t.TempDir(), filepath.Join(t.TempDir(), "recovery")
			const id = "11111111-1111-4111-8111-111111111111"
			project := filepath.Join(config, "projects", regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(cwd, "-"))
			require.NoError(t, os.MkdirAll(project, 0o700))
			// Constructed native history is read through the actual SDK query API.
			row, err := json.Marshal(map[string]any{
				"type": "user", "uuid": "retained", "parentUuid": nil, "sessionId": id, "cwd": cwd,
				"message": map[string]any{"role": "user", "content": "retained prompt"},
			})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(project, id+".jsonl"), append(row, '\n'), 0o600))
			if phase == "recovery-save" {
				require.NoError(t, os.WriteFile(recoveryDir, []byte("blocks recovery directory creation"), 0o600))
			}
			created, err := (Factory{RecoveryDir: recoveryDir}).New("agent", runtime.Config{
				"executable": filepath.Join(cwd, "unused-synthetic-peer"), "envPolicy": "replace",
				"env": map[string]any{"CLAUDE_CONFIG_DIR": config},
			})
			require.NoError(t, err)
			driver := created.(*Driver)
			// The controlled SDK session owns no process or goroutine. Always
			// release the Driver lifetime, including when the panic regression fails.
			t.Cleanup(func() { driver.cancel(); driver.pipe.End(nil) })
			native := &loadCleanupSession{id: id, eventsErr: errors.New("synthetic Events refusal")}
			closeFailure := errors.New("synthetic native close uncertainty")
			if phase == "relay-claim-close-failure" {
				native.closeErr = &agent.Error{Code: agent.ErrorOutcomeUnknown, Reason: "close_outcome_unknown", Cause: closeFailure}
			}
			provider := &loadCleanupProvider{native: native}
			driver.provider = provider
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			_, err = driver.Start(ctx)
			require.NoError(t, err)
			stream, err := driver.Events()
			require.NoError(t, err)
			read, cancelRead := context.WithCancel(ctx)
			defer cancelRead()
			if phase == "started-marker" {
				provider.onResume = cancelRead
			}
			err = driver.LoadSession(read, id, runtime.Address{Namespace: "one", SessionName: "loaded"}, runtime.SessionSpec{Cwd: cwd}, "load")
			require.Error(t, err)
			require.Equal(t, 1, provider.resumes, "failure must occur after native resume succeeds")
			switch phase {
			case "recovery-save":
				var pathErr *os.PathError
				require.ErrorAs(t, err, &pathErr)
			case "started-marker":
				require.ErrorIs(t, err, context.Canceled)
			default:
				require.Equal(t, 1, native.eventCalls, "failure must reach relay stream acquisition")
			}
			require.Zero(t, driver.opening)
			require.Zero(t, driver.loading)
			h := driver.loads[id]
			require.NotNil(t, h)
			if phase == "relay-claim" {
				require.Nil(t, h.s, "confirmed failed relay cleanup removes its binding")
				require.NoError(t, driver.CancelHistoryLoad(ctx, id, "load"))
				require.Equal(t, 1, native.closeCalls)
				require.NoError(t, driver.Stop(ctx))
				return
			}
			require.NotNil(t, h.s)
			require.Nil(t, h.s.relay, "failure must precede relay establishment")
			require.Same(t, h.s, driver.sessions[id])
			cleanup, cancelCleanup := context.WithTimeout(ctx, 30*time.Millisecond)
			defer cancelCleanup()
			var cleanupErr error
			require.NotPanics(t, func() { cleanupErr = driver.CancelHistoryLoad(cleanup, id, "load") })
			require.Equal(t, 1, native.closeCalls, "native Close remains single attempt")
			require.Same(t, h.s, driver.sessions[id], "failed cleanup must retain ownership")
			if phase == "relay-claim-close-failure" {
				require.ErrorIs(t, cleanupErr, closeFailure)
				require.Error(t, driver.Stop(ctx), "unconfirmed native close must continue to block Stop")
				return
			}
			require.Zero(t, native.eventCalls)
			require.ErrorIs(t, cleanupErr, context.DeadlineExceeded, "the admitted failure marker must still be routed")
			marker, err := stream.Next(ctx)
			require.NoError(t, err)
			replay, ok := marker.Payload().(events.HistoryReplay)
			require.True(t, ok)
			require.Equal(t, "load", replay.LoadID)
			require.Contains(t, []string{"failed", "canceled"}, replay.Phase)
			// Advancing Next acknowledges routing of the previous marker.
			ack, cancelAck := context.WithCancel(ctx)
			cancelAck()
			_, err = stream.Next(ack)
			require.ErrorIs(t, err, context.Canceled)
			require.NoError(t, driver.CancelHistoryLoad(ctx, id, "load"))
			require.NotContains(t, driver.sessions, id)
			require.Equal(t, 1, native.closeCalls)
			require.NoError(t, driver.Stop(ctx))
		})
	}
}
