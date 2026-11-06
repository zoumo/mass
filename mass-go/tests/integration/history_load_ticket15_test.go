package integration_test

import (
	"errors"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

func TestTicket15CodexColdLoadMaterializesMetadata(t *testing.T) {
	ticket12ColdLoadHTTP(t, "cold-materialized")
}

func TestTicket15CodexColdLoadEmptyEnvironmentAndTimestamp(t *testing.T) {
	for _, mode := range []string{"cold-materialized-empty", "cold-timestamp"} {
		t.Run(mode, func(t *testing.T) { ticket12ColdLoadHTTP(t, mode) })
	}
}

func TestTicket15CodexColdLoadMetadataBoundaries(t *testing.T) {
	for _, mode := range []string{
		"cold-materialized:pre:updatedAtNegative",
		"cold-materialized:pre:updatedAtBeforeCreated",
		"cold-materialized:resume:updatedAtNegative",
		"cold-materialized:resume:updatedAtRegression",
		"cold-materialized:pre:environmentsMissing",
		"cold-materialized:pre:environmentsEmpty",
		"cold-materialized:resume:environmentsMissing",
		"cold-materialized:resume:environmentUnknown",
		"cold-materialized:resume:metadata",
		"cold-materialized:resume:statusMetadata",
		"cold-materialized:pre:idle",    // Already loaded: neither field may change.
		"cold:resume:environmentsEmpty", // Missing is not explicit null.
		"loaded:resume:updatedAt",
		"loaded:resume:environmentsEmpty",
		"cold-materialized:after:updatedAtAdvance",
		"cold-materialized:after:environmentsEmpty",
		"cold-materialized:after:environmentsNull",
		"cold-materialized:after:environmentUnknown",
	} {
		t.Run(mode, func(t *testing.T) {
			evidence := filepath.Join(t.TempDir(), "peer.jsonl")
			svc, cwd := ticket06Service(t, mode, map[string]any{"LOAD_EVIDENCE": evidence})
			ref := resource.SessionRef{SessionID: "native-thread"}
			_, err := svc.LoadSession(ctx(t), resource.SessionLoad{Metadata: resource.Metadata{Namespace: "one", Name: "rejected"}, RuntimeRef: "agent", Cwd: cwd, SessionID: ref.SessionID})
			require.NoError(t, err)
			var loaded resource.Session
			require.Eventually(t, func() bool {
				loaded, err = svc.GetSession(ref)
				return err == nil && loaded.Status.HistoryLoad.Phase == "failed" && loaded.Status.HistoryLoad.CleanupCompleted
			}, 5*time.Second, time.Millisecond)
			require.False(t, loaded.Status.HistoryLoad.NativeCompleted)
			require.False(t, loaded.Status.HistoryLoad.DeliveryCompleted)
			require.NotNil(t, loaded.Status.HistoryLoad.Error)
			_, err = svc.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("must reject")}})
			require.Error(t, err)
			page, err := svc.ListHistoryEvents(ref, store.HistoryQuery{})
			require.NoError(t, err)
			retained := 0
			if strings.Contains(mode, ":after:") {
				retained = 3
				require.True(t, loaded.Status.HistoryLoad.NativeVerified)
				require.True(t, loaded.Status.HistoryLoad.NativeResumed)
			}
			require.Len(t, page.Events, retained)
			_, err = svc.StopRuntime(ctx(t), "agent")
			require.NoError(t, err)
			calls := ticket12Calls(t, evidence)
			counts := map[string]int{}
			for _, call := range calls {
				require.Equal(t, calls[0].PID, call.PID, "original owned peer")
				counts[call.Method]++
			}
			require.Equal(t, 1, counts["thread/resume"], "no retry")
			require.Zero(t, counts["turn/start"], "no hidden input")
			require.Equal(t, 1, counts["thread/unsubscribe"])
			require.Equal(t, 1, counts["released"])
			require.Eventually(t, func() bool { return errors.Is(syscall.Kill(calls[0].PID, 0), syscall.ESRCH) }, time.Second, time.Millisecond)
		})
	}
}
