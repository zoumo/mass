package integration_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/massd/pkg/runtime"
	"github.com/zoumo/mass/massd/pkg/runtime/claude"
	"github.com/zoumo/mass/massd/pkg/runtime/codex"
)

func questionAnswer(kind string) map[string]any {
	answers := map[string]any{"format": map[string]any{"answers": []any{"json"}}}
	if kind == "claude" {
		answers = map[string]any{"Which format?": "json"}
	}
	return map[string]any{"action": "accept", "content": map[string]any{"answers": answers}}
}

type questionDriver struct {
	runtime.Driver
	stream   events.Stream
	cwd      string
	sessions []string
}

func startQuestionDriver(t *testing.T, kind string) *questionDriver {
	t.Helper()
	path, work := nativeScenario(t, kind, "question")
	dir := t.TempDir()
	var factory runtime.Factory = codex.Factory{RecoveryDir: dir}
	bin := mock
	if kind == "claude" {
		factory = claude.Factory{RecoveryDir: dir}
		bin = claudeMock
	}
	driver, err := factory.New("questions", runtime.Config{"executable": bin, "scenario": path})
	require.NoError(t, err)
	stream, err := driver.Events()
	require.NoError(t, err)
	d := &questionDriver{Driver: driver, stream: stream, cwd: work}
	t.Cleanup(func() {
		// CloseSession flushes the shared stream through its last acknowledgement.
		cleanup := ctx(t)
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				if _, err := stream.Next(cleanup); err != nil {
					return
				}
			}
		}()
		for _, id := range d.sessions {
			if err := driver.CloseSession(cleanup, id); err != nil {
				t.Errorf("close session %s: %v", id, err)
			}
		}
		if err := driver.Stop(cleanup); err != nil {
			t.Errorf("stop driver: %v", err)
		}
		<-done
	})
	_, err = driver.Start(ctx(t))
	require.NoError(t, err)
	return d
}

func (d *questionDriver) openQuestion(t *testing.T, name string) (sessionID, ref string) {
	t.Helper()
	ctx := ctx(t)
	s, err := d.OpenSession(ctx, runtime.Address{Namespace: "default", SessionName: name}, runtime.SessionSpec{Cwd: d.cwd})
	require.NoError(t, err)
	d.sessions = append(d.sessions, s.SessionID)
	_, err = d.SendPrompt(ctx, s.SessionID, runtime.Input{Content: []events.ContentBlock{events.Text("hello")}})
	require.NoError(t, err)
	for {
		e, err := d.stream.Next(ctx)
		require.NoError(t, err)
		if request, ok := e.Payload().(events.InteractionRequested); ok {
			require.Equal(t, s.SessionID, e.SessionID)
			return s.SessionID, string(request.Interaction)
		}
	}
}

func TestDriverAnswerCanceledBeforeWrite(t *testing.T) {
	for _, kind := range []string{"claude", "codex"} {
		t.Run(kind, func(t *testing.T) {
			d := startQuestionDriver(t, kind)
			other, err := d.Events()
			require.Error(t, err)
			require.Nil(t, other)
			// The original stream still delivers the real session's request.
			id, ref := d.openQuestion(t, "one")
			answer := questionAnswer(kind)
			canceled, cancel := context.WithCancel(t.Context())
			cancel()
			evidence, err := d.AnswerInteraction(canceled, id, ref, answer)
			require.ErrorIs(t, err, context.Canceled)
			require.Equal(t, runtime.Bool(false), evidence.Sent)
			evidence, err = d.AnswerInteraction(ctx(t), id, ref, answer)
			require.NoError(t, err)
			require.Equal(t, runtime.Bool(true), evidence.Sent)
			_, err = d.AnswerInteraction(ctx(t), id, ref, answer)
			require.ErrorIs(t, err, runtime.ErrFailedPrecondition)
		})
	}
}

// Hold the caller at the SDK's pre-write cancellation check. The peer can
// finish the request immediately after release.
type answerWriteGate struct {
	context.Context
	once             sync.Once
	entered, release chan struct{}
}

func (g *answerWriteGate) Err() error {
	g.once.Do(func() {
		close(g.entered)
		select {
		case <-g.release:
		case <-g.Context.Done():
		}
	})
	return g.Context.Err()
}

func TestDriverAnswerIsolatedAcrossSessions(t *testing.T) {
	d := startQuestionDriver(t, "claude")
	a, refA := d.openQuestion(t, "a")
	b, refB := d.openQuestion(t, "b")
	require.NotEqual(t, a, b)
	require.NotEqual(t, refA, refB)
	answer := questionAnswer("claude")
	_, err := d.AnswerInteraction(ctx(t), b, refA, answer)
	require.ErrorIs(t, err, runtime.ErrFailedPrecondition)

	deadline := ctx(t)
	gate := &answerWriteGate{Context: deadline, entered: make(chan struct{}), release: make(chan struct{})}
	var receiptA runtime.Acceptance
	var errA error
	done := make(chan struct{})
	go func() {
		defer close(done)
		receiptA, errA = d.AnswerInteraction(gate, a, refA, answer)
	}()
	release := sync.OnceFunc(func() { close(gate.release) })
	defer func() { release(); <-done }()
	select {
	case <-gate.entered:
	case <-done:
		t.Fatalf("session A returned before reaching its write: %v", errA)
	case <-deadline.Done():
		t.Fatal("session A did not enter SDK Answer")
	}

	receiptB, err := d.AnswerInteraction(deadline, b, refB, answer)
	require.NoError(t, err, "session B must finish while session A is answering")
	require.Equal(t, runtime.Bool(true), receiptB.Sent)
	select {
	case <-done:
		t.Fatalf("session A returned before release: %v", errA)
	default:
	}
	release()
	<-done
	require.NoError(t, errA)
	require.Equal(t, runtime.Bool(true), receiptA.Sent)
}
