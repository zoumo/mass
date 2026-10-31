package file

import (
	"fmt"
	"os"
	goruntime "runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

// Opt-in quantitative acceptance. Four Sessions, fixed payloads, bounded batches.
func TestReplicaScaleAcceptance(t *testing.T) {
	if os.Getenv("MASS_TEST_REPLICA_SCALE") != "1" {
		t.Skip("opt-in disk/memory acceptance")
	}
	for _, scale := range []struct{ count, payload int }{{1024, 1024}, {8192, 1024}, {32768, 1024}, {1024, 16384}, {1024, 65536}} {
		t.Run(fmt.Sprintf("%d-%d", scale.count, scale.payload), func(t *testing.T) {
			count := scale.count
			dir := t.TempDir()
			st, err := Open(dir, false)
			require.NoError(t, err)
			original := st
			t.Cleanup(func() { require.NoError(t, original.Close()) })
			sessions := make([]resource.Session, 4)
			for i := range sessions {
				sessions[i], err = st.CreateSession(resource.Session{Metadata: resource.Metadata{Namespace: "scale", Name: strconv.Itoa(i)}, Spec: resource.SessionSpec{RuntimeRef: "agent"}, Status: resource.SessionStatus{SessionID: strconv.Itoa(i), State: events.Unknown}})
				require.NoError(t, err)
			}
			var retry events.SessionEvent
			for n := 0; n < count; n += 64 {
				r := sessions[(n/64)%len(sessions)]
				batch := make([]events.SessionEvent, 64)
				for i := range batch {
					batch[i], err = events.NewEvent(events.EventBase{EventID: uuid.NewString(), Namespace: r.Metadata.Namespace, SessionName: r.Metadata.Name, SessionID: r.Status.SessionID, ItemID: uuid.NewString(), Time: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)}, events.AgentMessage{MessageID: "scale", Content: []events.ContentBlock{events.Text(strings.Repeat("x", scale.payload))}}, events.NewSource())
					require.NoError(t, err)
				}
				_, err = st.Append(r.Status.SessionID, batch)
				require.NoError(t, err)
				if n == 0 {
					retry = batch[0]
				}
			}
			require.NoError(t, st.Close())
			measure := func(name string, operation func()) {
				goruntime.GC()
				var before, after goruntime.MemStats
				goruntime.ReadMemStats(&before)
				samples := make(chan uint64, 1)
				stop := make(chan struct{})
				go func() {
					ticker := time.NewTicker(5 * time.Millisecond)
					defer ticker.Stop()
					peak := before.HeapAlloc
					for {
						select {
						case <-ticker.C:
							var sample goruntime.MemStats
							goruntime.ReadMemStats(&sample)
							peak = max(peak, sample.HeapAlloc)
						case <-stop:
							samples <- peak
							return
						}
					}
				}()
				start := time.Now()
				var peak uint64
				func() {
					defer func() { close(stop); peak = <-samples }()
					operation()
				}()
				elapsed := time.Since(start)
				goruntime.GC()
				goruntime.ReadMemStats(&after)
				var usage syscall.Rusage
				require.NoError(t, syscall.Getrusage(syscall.RUSAGE_SELF, &usage))
				delta := int64(after.HeapAlloc) - int64(before.HeapAlloc)
				require.Less(t, delta, int64(16<<20), "retained application heap must remain bounded")
				t.Logf("%s count=%d payload=%d elapsed=%s alloc_bytes=%d retained_heap=%d heap_delta=%d sampled_peak_heap=%d process_maxrss=%d", name, count, scale.payload, elapsed, after.TotalAlloc-before.TotalAlloc, after.HeapAlloc, delta, peak, usage.Maxrss)
			}
			measure("startup", func() { st, err = Open(dir, false); require.NoError(t, err) })
			reopened := st
			t.Cleanup(func() { require.NoError(t, reopened.Close()) })
			measure("query100", func() {
				for range 100 {
					page, e := st.ListHistory("0", store.HistoryQuery{Types: []string{"agent.message"}, Limit: 10})
					require.NoError(t, e)
					require.Len(t, page.Events, 10)
				}
			})
			measure("retry100", func() {
				for range 100 {
					_, e := st.Append("0", []events.SessionEvent{retry})
					require.NoError(t, e)
				}
			})
			measure("append20", func() {
				for range 20 {
					e := retry
					e.EventID = uuid.NewString()
					_, err = st.Append("0", []events.SessionEvent{e})
					require.NoError(t, err)
				}
			})
			measure("status20", func() {
				for range 20 {
					r, e := st.GetSession(resource.SessionRef{SessionID: "0"})
					require.NoError(t, e)
					if r.Status.State == events.Idle {
						r.Status.State = events.Unknown
					} else {
						r.Status.State = events.Idle
					}
					_, e = st.UpdateSession(r)
					require.NoError(t, e)
				}
			})
			measure("reconcile", func() { require.NoError(t, st.Reconcile()) })
			previousOpen := st.appendOpen
			st.appendOpen = func(path string) (appendFile, error) {
				f, e := openHistory(path, os.O_RDWR|os.O_APPEND)
				if e != nil {
					return nil, e
				}
				return failingAppendFile{File: f, stage: "before-write"}, nil
			}
			pending := retry
			pending.EventID = uuid.NewString()
			_, err = st.Append("0", []events.SessionEvent{pending})
			require.ErrorContains(t, err, "outcome_unknown")
			st.appendOpen = previousOpen
			measure("pending-recovery", func() { require.NoError(t, st.Reconcile()) })
		})
	}
}
