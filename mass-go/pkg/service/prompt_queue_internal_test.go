package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestPromptDispatchSettlesAfterResumeReplacesQueue(t *testing.T) {
	item := &queuedPrompt{nativeStarted: true, done: make(chan promptOutcome, 1)}
	q := &promptQueue{dispatching: true, pending: []*queuedPrompt{item}}
	l := &lane{prompts: q}
	run := &runBinding{}

	stopPromptQueueLocked(q, runtime.FailedPrecondition("resume replaced the lane"))
	l.prompts = &promptQueue{}
	(&Service{}).finishPromptDispatch(run, l, q, item, runtime.InputReceipt{
		Submission: "original",
		Result:     agent.InputAppliedNewWork,
	}, nil)

	outcome := <-item.done
	require.NoError(t, outcome.err)
	require.Equal(t, "original", outcome.receipt.Submission)
	require.Empty(t, q.pending)
}

func TestPromptNotAppliedSettlesAfterQueueShutdown(t *testing.T) {
	item := &queuedPrompt{nativeStarted: true, done: make(chan promptOutcome, 1)}
	q := &promptQueue{dispatching: true, pending: []*queuedPrompt{item}}
	l := &lane{prompts: q}
	run := &runBinding{}

	stopPromptQueueLocked(q, runtime.FailedPrecondition("session close accepted"))
	select {
	case outcome := <-item.done:
		t.Fatalf("in-flight input settled before its native receipt: %#v", outcome)
	default:
	}

	(&Service{}).finishPromptDispatch(run, l, q, item, runtime.InputReceipt{
		Result: agent.InputNotApplied,
	}, nil)

	outcome := <-item.done
	require.NoError(t, outcome.err)
	require.Equal(t, agent.InputNotApplied, outcome.receipt.Result)
	require.Empty(t, q.pending)
}
