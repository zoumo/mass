package integration_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/claude"
	"github.com/zoumo/mass/mass-go/pkg/runtime/codex"
)

func lifecycleDriver(t *testing.T, kind, control string) (runtime.Driver, events.Stream, string) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "lifecycle-peer")
	out, err := exec.CommandContext(ctx(t), "go", "build", "-o", bin, "./testdata/lifecycle-peer").CombinedOutput()
	require.NoError(t, err, string(out))
	pidFile := filepath.Join(t.TempDir(), "peer.pid")
	var factory runtime.Factory = codex.Factory{RecoveryDir: t.TempDir()}
	if kind == "claude" {
		factory = claude.Factory{RecoveryDir: t.TempDir()}
	}
	d, err := factory.New("test", runtime.Config{"executable": bin, "envPolicy": "replace", "env": map[string]any{"PEER_PID": pidFile, "PEER_KIND": kind, "PEER_CONTROL": control}})
	require.NoError(t, err)
	stream, err := d.Events()
	require.NoError(t, err)
	_, err = d.Start(ctx(t))
	require.NoError(t, err)
	t.Cleanup(func() {
		data, err := os.ReadFile(pidFile)
		require.NoError(t, err)
		pid, err := strconv.Atoi(string(data))
		require.NoError(t, err)
		process, err := os.FindProcess(pid)
		require.NoError(t, err)
		if err := process.Kill(); err != nil {
			require.ErrorIs(t, err, os.ErrProcessDone)
		}
		// The SDK owns Wait. Read back process disappearance rather than treating
		// a successful signal as release evidence.
		deadline, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for {
			out, err := exec.CommandContext(deadline, "ps", "-p", strconv.Itoa(pid), "-o", "pid=").Output()
			if err != nil && len(out) == 0 && deadline.Err() == nil {
				break
			}
			require.NoError(t, deadline.Err(), "owned peer was not reaped")
		}
	})
	return d, stream, pidFile
}

func TestTicket04CodexCloseReturnsSDKFailureAndKeepsSharedClient(t *testing.T) {
	d, stream, _ := lifecycleDriver(t, "codex", "acknowledged")
	work, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	for _, name := range []string{"healthy", "damaged"} {
		_, err = d.OpenSession(ctx(t), runtime.Address{Namespace: "default", SessionName: name}, runtime.SessionSpec{Cwd: work})
		require.NoError(t, err)
	}
	_, err = d.SendPrompt(ctx(t), "damaged", runtime.Input{Content: []events.ContentBlock{events.Text("finish then queue final facts")}})
	require.NoError(t, err)
	for {
		e, err := stream.Next(ctx(t))
		require.NoError(t, err)
		if state, ok := e.Payload().(events.StateUpdate); ok && state.State != nil && *state.State == events.Running {
			break
		}
	}
	// This response follows all damaged-session frames on the same wire. The
	// SDK reader has admitted them even though the Driver consumer is withheld.
	receipt, err := d.SendPrompt(ctx(t), "healthy", runtime.Input{Content: []events.ContentBlock{events.Text("hello")}})
	require.NoError(t, err)
	require.Equal(t, "native-turn-healthy", receipt.Submission)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	err = d.CloseSession(canceled, "damaged")
	require.Error(t, err)
	require.NotErrorIs(t, err, context.Canceled, "caller cancellation must not replace the SDK close result")
	require.Contains(t, err.Error(), "close_failed", "the SDK cleanup failure is returned without native diagnostics")
	seen := make(chan int, 1)
	var reopened atomic.Int32
	var drained atomic.Int32
	receiveCtx, stopReceive := context.WithCancel(context.Background())
	received := make(chan struct{})
	t.Cleanup(func() { stopReceive(); <-received })
	go func() {
		defer close(received)
		count := 0
		notified := false
		for {
			e, err := stream.Next(receiveCtx)
			if err != nil {
				return
			}
			if _, ok := e.Payload().(events.AgentMessage); ok {
				count++
				drained.Store(int32(count))
			}
			if _, ok := e.Payload().(events.InteractionRequested); ok {
				reopened.Add(1)
			}
			if count == 160 && !notified {
				notified = true
				seen <- count
				// Keep consuming facts that remain in the shared event stream.
			}
		}
	}()
	closeErr := d.CloseSession(ctx(t), "damaged")
	require.Error(t, closeErr)
	require.NotErrorIs(t, closeErr, context.Canceled)
	require.Contains(t, closeErr.Error(), "close_failed")
	require.NotContains(t, closeErr.Error(), "observation", "driver close must not probe observation health")
	require.Zero(t, reopened.Load(), "a queued request must not reopen answer authority during Close")
	select {
	case count := <-seen:
		require.Equal(t, 160, count)
	case <-ctx(t).Done():
		t.Fatalf("admitted final facts did not drain: %d", drained.Load())
	}
	_, err = d.SendPrompt(ctx(t), "damaged", runtime.Input{Content: []events.ContentBlock{events.Text("rejected")}})
	require.Error(t, err)
	first, err := d.Cancel(ctx(t), "healthy")
	require.NoError(t, err)
	second, err := d.Cancel(ctx(t), "healthy")
	require.NoError(t, err, "same-work control must not be dispatched twice")
	require.Equal(t, first, second)
}

func TestTicket04DriverCloseWaitsForEventRouting(t *testing.T) {
	for _, kind := range []string{"codex", "claude"} {
		t.Run(kind, func(t *testing.T) {
			bin := mock
			var factory runtime.Factory = codex.Factory{RecoveryDir: t.TempDir()}
			if kind == "claude" {
				bin = claudeMock
				factory = claude.Factory{RecoveryDir: t.TempDir()}
			}
			path, work := nativeScenario(t, kind, "ticket04-close-drain")
			d, err := factory.New("test", runtime.Config{"executable": bin, "scenario": path})
			require.NoError(t, err)
			stream, err := d.Events()
			require.NoError(t, err)
			_, err = d.Start(ctx(t))
			require.NoError(t, err)
			opened, err := d.OpenSession(ctx(t), runtime.Address{Namespace: "default", SessionName: "close"}, runtime.SessionSpec{Cwd: work})
			require.NoError(t, err)
			drain := func() func() {
				readCtx, stop := context.WithCancel(context.Background())
				drained := make(chan struct{})
				go func() {
					defer close(drained)
					for {
						if _, receiveErr := stream.Next(readCtx); receiveErr != nil {
							return
						}
					}
				}()
				return func() {
					stop()
					<-drained
				}
			}
			t.Cleanup(func() {
				stop := drain()
				cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = d.CloseSession(cleanup, opened.SessionID)
				_ = d.Stop(cleanup)
				stop()
			})

			_, err = d.SendPrompt(ctx(t), opened.SessionID, runtime.Input{Content: []events.ContentBlock{events.Text("hello")}})
			require.NoError(t, err)
			_, err = stream.Next(ctx(t))
			require.NoError(t, err, "one admitted event is now awaiting its Next acknowledgement")

			closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err = d.CloseSession(closeCtx, opened.SessionID)
			require.ErrorIs(t, err, context.DeadlineExceeded, "close must not pass the EventPipe routing watermark")

			stop := drain()
			require.NoError(t, d.CloseSession(ctx(t), opened.SessionID))
			stop()
			require.NoError(t, d.Stop(ctx(t)))
		})
	}
}

func TestTicket04CodexCloseAndStopAfterObservationFailure(t *testing.T) {
	path, work := nativeScenario(t, "codex", "native-unknown")
	d, err := (codex.Factory{RecoveryDir: t.TempDir()}).New("test", runtime.Config{"executable": mock, "scenario": path})
	require.NoError(t, err)
	stream, err := d.Events()
	require.NoError(t, err)
	_, err = d.Start(ctx(t))
	require.NoError(t, err)
	opened, err := d.OpenSession(ctx(t), runtime.Address{Namespace: "default", SessionName: "failed-observation"}, runtime.SessionSpec{Cwd: work})
	require.NoError(t, err)

	readCtx, stopRead := context.WithCancel(context.Background())
	readDone := make(chan struct{})
	failure := make(chan events.SessionError, 1)
	go func() {
		defer close(readDone)
		for {
			event, nextErr := stream.Next(readCtx)
			if nextErr != nil {
				return
			}
			if problem, ok := event.Payload().(events.SessionError); ok {
				select {
				case failure <- problem:
				default:
				}
			}
		}
	}()
	t.Cleanup(func() {
		stopRead()
		<-readDone
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = d.CloseSession(cleanup, opened.SessionID)
		_ = d.Stop(cleanup)
	})

	_, err = d.SendPrompt(ctx(t), opened.SessionID, runtime.Input{Content: []events.ContentBlock{events.Text("hello")}})
	require.NoError(t, err)
	select {
	case problem := <-failure:
		require.Equal(t, "unsupported_native_item", problem.Reason)
	case <-ctx(t).Done():
		t.Fatal("unsupported native observation was not surfaced")
	}
	require.Eventually(t, func() bool {
		_, sendErr := d.SendPrompt(ctx(t), opened.SessionID, runtime.Input{Content: []events.ContentBlock{events.Text("must not send")}})
		return sendErr != nil
	}, time.Second, time.Millisecond)

	closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, d.CloseSession(closeCtx, opened.SessionID), "prior observation failure must not replace successful SDK close")
	require.NoError(t, d.Stop(closeCtx), "a settled session must not block provider stop")
}

func TestTicket04CancelDispatchEvidence(t *testing.T) {
	for _, kind := range []string{"codex", "claude"} {
		for _, control := range []string{"acknowledged", "unknown", "rejected", "write-failure"} {
			t.Run(kind+"/"+control, func(t *testing.T) {
				d, stream, _ := lifecycleDriver(t, kind, control)
				work, err := filepath.EvalSymlinks(t.TempDir())
				require.NoError(t, err)
				opened, err := d.OpenSession(ctx(t), runtime.Address{Namespace: "default", SessionName: "healthy"}, runtime.SessionSpec{Cwd: work})
				require.NoError(t, err)
				_, err = d.SendPrompt(ctx(t), opened.SessionID, runtime.Input{Content: []events.ContentBlock{events.Text("hello")}})
				require.NoError(t, err)
				// Known non-dispatch must allow an explicit, fresh attempt.
				canceled, stop := context.WithCancel(context.Background())
				stop()
				_, err = d.Cancel(canceled, opened.SessionID)
				require.ErrorContains(t, err, "not_sent")
				if control == "write-failure" {
					for {
						e, err := stream.Next(ctx(t))
						require.NoError(t, err)
						if _, ok := e.Payload().(events.AgentMessage); ok {
							break
						}
					}
				}
				cancelCtx, cancel := context.WithCancel(ctx(t))
				defer cancel()
				type result struct {
					receipt runtime.CancelReceipt
					err     error
				}
				done := make(chan result, 1)
				go func() { r, e := d.Cancel(cancelCtx, opened.SessionID); done <- result{r, e} }()
				if control == "unknown" {
					for {
						e, err := stream.Next(ctx(t))
						require.NoError(t, err)
						if _, ok := e.Payload().(events.AgentMessage); ok {
							break
						}
					}
					cancel() // Native evidence, not elapsed time, triggers abandonment.
				}
				var first result
				select {
				case first = <-done:
				case <-ctx(t).Done():
					t.Fatal("cancel did not settle")
				}
				second, secondErr := d.Cancel(ctx(t), opened.SessionID)
				switch control {
				case "acknowledged":
					require.NoError(t, first.err)
					require.NoError(t, secondErr)
					require.Equal(t, "controlAcknowledged", first.receipt.Result)
					require.Equal(t, first.receipt, second)
				case "unknown":
					require.ErrorContains(t, first.err, "outcome_unknown")
					require.ErrorIs(t, first.err, context.Canceled)
					require.Equal(t, first.err, secondErr, "unknown controls must never be replayed")
				case "rejected":
					require.ErrorContains(t, first.err, "native_rejected")
					require.Equal(t, first.err, secondErr)
				case "write-failure":
					require.ErrorContains(t, first.err, "outcome_unknown")
					require.Equal(t, first.err, secondErr, "failed writes must not be replayed")
				}
				receipt, err := d.SendPrompt(ctx(t), opened.SessionID, runtime.Input{Content: []events.ContentBlock{events.Text("steer current work")}})
				if kind != "codex" || control == "write-failure" {
					require.Error(t, err)
				} else {
					require.NoError(t, err, "the SDK decides whether a new input may join current work")
					require.Equal(t, agent.InputAppliedCurrentWork, receipt.Result)
				}
			})
		}
	}
}

func TestTicket04ClaudeCloseReturnsSDKFailureWithBackloggedEOF(t *testing.T) {
	d, stream, pidFile := lifecycleDriver(t, "claude", "eof")
	work, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	opened, err := d.OpenSession(ctx(t), runtime.Address{Namespace: "default", SessionName: "healthy"}, runtime.SessionSpec{Cwd: work})
	require.NoError(t, err)
	_, err = d.SendPrompt(ctx(t), opened.SessionID, runtime.Input{Content: []events.ContentBlock{events.Text("finish then EOF")}})
	require.NoError(t, err)
	for {
		e, err := stream.Next(ctx(t))
		require.NoError(t, err)
		if state, ok := e.Payload().(events.StateUpdate); ok && state.State != nil && *state.State == events.Idle && state.StopReason == "success" {
			break
		}
	}
	pid, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	deadline := ctx(t)
	for {
		out, err := exec.CommandContext(deadline, "ps", "-p", string(pid), "-o", "pid=").Output()
		if err != nil && len(out) == 0 && deadline.Err() == nil {
			break
		}
		require.NoError(t, deadline.Err(), "controlled EOF peer did not exit")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	err = d.CloseSession(canceled, opened.SessionID)
	require.Error(t, err)
	require.NotErrorIs(t, err, context.Canceled, "caller cancellation must not replace the SDK close result")

	count := 0
	for count < 160 {
		e, nextErr := stream.Next(ctx(t))
		require.NoError(t, nextErr)
		if _, ok := e.Payload().(events.AgentMessage); ok {
			count++
		}
	}
	require.Equal(t, 160, count, "SDK close must not discard admitted facts")

	err = d.CloseSession(ctx(t), opened.SessionID)
	require.Error(t, err, "unexpected native EOF must retain release responsibility")
}
