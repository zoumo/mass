package vertical_test

import (
	"errors"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

func TestTicket16CodexColdDirectInputHTTPHistoryAndContinuedInput(t *testing.T) {
	ticket12ColdLoadHTTP(t, "cold-materialized-direct")
}

func TestTicket16CodexUnchangedDirectInputCapability(t *testing.T) {
	for _, mode := range []string{"cold", "cold-direct-equal-null", "cold-direct-equal-false", "cold-direct-equal-true"} {
		t.Run(mode, func(t *testing.T) { ticket12ColdLoadHTTP(t, mode) })
	}
}

func TestTicket16CodexDirectInputMetadataBoundaries(t *testing.T) {
	cases := []string{
		"cold-materialized-direct:pre:directMissing", // Omission is not explicit null.
		"cold-materialized-direct:pre:directFalse",
		"cold-materialized-direct:pre:directString",
		"cold-materialized-direct:pre:directNumber",
		"cold-materialized-direct:resume:directFalse", // Null -> false is not the observed transition.
		"cold-materialized-direct:resume:directMissing",
		"cold-materialized-direct:pre:idle", // Already loaded: even null -> true is drift.
		"cold-direct-equal-true:resume:directFalse",
		"cold-direct-equal-true:resume:directNull",
		"cold-direct-equal-true:resume:directMissing",
		"cold-materialized-direct:after:directFalse",
		"cold-materialized-direct:after:directNull",
		"cold-materialized-direct:after:directMissing",
		"cold-materialized-direct:after:directString",
		"cold-materialized-direct:after:directNumber",
		"cold:pre:directTrue",      // True -> missing.
		"cold:resume:directFalse",  // Missing -> false.
		"loaded:resume:directTrue", // Loaded -> loaded remains strict.
		"cold-materialized-direct:after:metadata",
		"cold-materialized-direct:after:statusMetadata",
		"cold-materialized-direct:after:sessionId",
		"cold-materialized-direct:after:id",
	}
	for _, field := range []string{
		"active", "unknown", "systemError", "notLoaded", "sessionId", "cwd",
		"modelProvider", "createdAt", "ephemeral", "source", "path", "metadata", "statusMetadata",
		"updatedAtRegression", "environmentUnknown",
	} {
		cases = append(cases, "cold-materialized-direct:resume:"+field)
	}
	for _, mode := range cases {
		t.Run(mode, func(t *testing.T) {
			evidence := filepath.Join(t.TempDir(), "peer.jsonl")
			svc, cwd := ticket06Service(t, mode, map[string]any{"LOAD_EVIDENCE": evidence})
			ref := resource.SessionRef{SessionID: "native-thread"}
			_, err := svc.LoadSession(ctx(t), resource.SessionLoad{Metadata: resource.Metadata{Namespace: "one", Name: "rejected"}, RuntimeRef: "agent", Cwd: cwd, SessionID: ref.SessionID})
			require.NoError(t, err)
			require.EventuallyWithT(t, func(c *assert.CollectT) {
				loaded, err := svc.GetSession(ref)
				if assert.NoError(c, err) {
					assert.Equal(c, "failed", loaded.Status.HistoryLoad.Phase)
					assert.True(c, loaded.Status.HistoryLoad.CleanupCompleted, "last public status: %+v", loaded.Status.HistoryLoad)
				}
			}, 5*time.Second, time.Millisecond)
			loaded, err := svc.GetSession(ref)
			require.NoError(t, err)
			require.NotNil(t, loaded.Status.HistoryLoad.Error)
			require.False(t, loaded.Status.HistoryLoad.NativeCompleted)
			require.False(t, loaded.Status.HistoryLoad.DeliveryCompleted)
			_, err = svc.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("must reject")}})
			require.Error(t, err)
			page, err := svc.ListHistoryEvents(ref, store.HistoryQuery{})
			require.NoError(t, err)
			retained, resumes := 0, 1
			if strings.Contains(mode, ":pre:directString") || strings.Contains(mode, ":pre:directNumber") {
				resumes = 0
			}
			if strings.Contains(mode, ":after:") {
				retained = 3
				require.True(t, loaded.Status.HistoryLoad.NativeVerified)
				require.True(t, loaded.Status.HistoryLoad.NativeResumed)
			}
			require.Len(t, page.Events, retained, "only facts accepted before failure remain")
			_, err = svc.StopRuntime(ctx(t), "agent")
			require.NoError(t, err)
			calls := ticket12Calls(t, evidence)
			counts := map[string]int{}
			for _, call := range calls {
				require.Equal(t, calls[0].PID, call.PID)
				counts[call.Method]++
			}
			require.Equal(t, resumes, counts["thread/resume"], "no retry")
			require.Zero(t, counts["turn/start"], "no hidden or rejected input")
			require.Equal(t, resumes, counts["thread/unsubscribe"])
			require.Equal(t, 1, counts["released"])
			require.Eventually(t, func() bool { return errors.Is(syscall.Kill(calls[0].PID, 0), syscall.ESRCH) }, time.Second, time.Millisecond)
		})
	}
}
