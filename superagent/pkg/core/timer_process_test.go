package core_test

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/superagent/pkg/core"
)

const (
	timerEventType = "timer.followup"
	timerPast      = "2020-01-02T08:00:00.012+08:00"
	timerFuture    = "2090-01-02T08:00:00.012+08:00"
)

type timerRecord struct {
	core.Timer
	Followup *core.Event `json:"followup"`
}

func timerInput(op, dueAt string) core.SetTimerRequest {
	return core.SetTimerRequest{OperationID: op, DueAt: dueAt, Content: "Review the build evidence."}
}

func cancelTimerInput(op string, timer core.Timer) core.CancelTimerRequest {
	return core.CancelTimerRequest{OperationID: op, TimerID: timer.ID, ExpectedRevision: timer.Revision}
}

func waitTimer(t *testing.T, p *process, id, state string) timerRecord {
	t.Helper()
	var found timerRecord
	require.Eventually(t, func() bool {
		found = readRecord[timerRecord](t, p, "timers/"+id)
		return found.Status == state
	}, 6*time.Second, 20*time.Millisecond, "Timer %s: waiting for %s", id, state)
	return found
}

func assertTimerFollowup(t *testing.T, p *process, timer timerRecord) {
	t.Helper()
	require.NotEmpty(t, timer.FiredAt)
	require.NotNil(t, timer.Followup)
	event := readRecord[core.Event](t, p, "events/"+timer.FollowupEventID)
	require.Equal(t, event.ID, timer.Followup.ID)
	require.Equal(t, timerEventType, event.Type)
	require.Equal(t, "act", event.Intent)
	require.Equal(t, "topic", event.TargetRole)
	require.Equal(t, timer.TopicID, event.TargetID)
	require.Equal(t, timer.Revision, event.Revision)
	require.NotNil(t, event.TopicGeneration)
	require.Equal(t, timer.TopicGeneration, *event.TopicGeneration)
	require.Equal(t, timer.FiredAt, event.At)
	require.Empty(t, event.MessageID)
	require.Empty(t, event.ThreadKey)
}

func TestTimerNativeDeliveryAndAuthority(t *testing.T) {
	t.Parallel()
	peer := newNativeDeliveryPeer(t)
	endpoint, _ := startDeliveryMassd(t, peer)
	f, _ := deliveryFixture(t, endpoint)
	p := f.start()
	routerBinding := waitBinding(t, p, "router", "", "ready")
	router := startRoleMCP(t, f, routerBinding)
	binding, source := nativeWorkTopic(t, f, p, peer, router, "timer-main")
	foreignBinding, foreignSource := nativeWorkTopic(t, f, p, peer, router, "timer-foreign")
	topic := startRoleMCP(t, f, binding)
	foreign := startRoleMCP(t, f, foreignBinding)

	tools := topic.call("tools/list", struct{}{})
	for _, name := range []string{"timer_set", "timer_update", "timer_cancel"} {
		require.Contains(t, string(tools["result"]), name)
		require.NotContains(t, string(router.call("tools/list", struct{}{})["result"]), name)
	}
	r := timerInput("set-followup", timerFuture)
	for _, due := range []string{"2090-01-02T08:00:00", "2090-01-02T08:00:00+24:00", "2090-01-02T08:00:00+01:60"} {
		mcpWorkError(t, topic, "timer_set", "invalid_argument", timerInput("invalid-zone", due))
	}
	mcpWorkError(t, topic, "timer_set", "invalid_argument", map[string]any{
		"operationId": r.OperationID, "dueAt": r.DueAt, "content": r.Content, "topicId": foreignBinding.OwnerID,
	})
	status, body := request(t, http.MethodPost, p.endpoint, "/v1/role/timer_set", roleToken(t, f, routerBinding), r)
	errorCode(t, status, body, http.StatusForbidden, "forbidden")
	wrongMaterial := r
	wrongMaterial.Materials = []core.Material{{MessageID: foreignSource.MessageID}}
	mcpWorkError(t, topic, "timer_set", "not_found", wrongMaterial)

	r.Materials = []core.Material{{Label: "Original request", MessageID: source.MessageID}, {URI: "notes/build.txt"}}
	scheduled := mcpWork[core.Timer](t, topic, "timer_set", r)
	require.Equal(t, binding.OwnerID, scheduled.TopicID)
	require.Equal(t, "scheduled", scheduled.Status)
	require.Equal(t, 1, scheduled.Revision)
	require.Equal(t, "2090-01-02T00:00:00.012000000Z", scheduled.DueAt)
	require.Equal(t, r.Materials, scheduled.Materials)
	require.Equal(t, scheduled, mcpWork[core.Timer](t, topic, "timer_set", r))
	r.Content = "Different content under the same operation ID"
	mcpWorkError(t, topic, "timer_set", "conflict", r)
	query := core.QueryRequest{Collection: "timers", ID: scheduled.ID}
	mcpWorkError(t, foreign, "work_query", "not_found", query)
	mcpWorkError(t, foreign, "timer_cancel", "not_found", cancelTimerInput("foreign-cancel", scheduled))
	mcpWorkError(t, foreign, "timer_update", "not_found", core.UpdateTimerRequest{
		CancelTimerRequest: cancelTimerInput("foreign-update", scheduled), DueAt: timerPast, Content: r.Content,
	})
	require.Empty(t, mcpWork[core.Page](t, foreign, "work_query", core.QueryRequest{Collection: "timers"}).Items)

	// A real short future dueAt tests the ordinary scan, independently of the past-time cases.
	due := time.Now().Add(750 * time.Millisecond).In(time.FixedZone("UTC+8", 8*60*60))
	update := core.UpdateTimerRequest{
		CancelTimerRequest: cancelTimerInput("reschedule", scheduled),
		DueAt:              due.Format(time.RFC3339Nano), Content: "Review the revised build evidence.", Materials: r.Materials,
	}
	rescheduled := mcpWork[core.Timer](t, topic, "timer_update", update)
	require.Equal(t, 2, rescheduled.Revision)
	require.Equal(t, update.Content, rescheduled.Content)
	require.Equal(t, 2, mcpWorkError(t, topic, "timer_cancel", "conflict", cancelTimerInput("stale-cancel", scheduled)).CurrentRevision)
	fired := waitTimer(t, p, scheduled.ID, "fired")
	at, err := time.Parse(time.RFC3339Nano, fired.FiredAt)
	require.NoError(t, err)
	require.False(t, at.Before(due), "Timer fired early")
	assertTimerFollowup(t, p, fired)
	advance := peer.next(t, "turn/start")
	require.Equal(t, binding.SessionID, advance.session)
	nativeEvent := decodeBatch(t, advance, fired.FollowupEventID).Events[0]
	require.Equal(t, timerEventType, nativeEvent.Type)
	require.Equal(t, 2, nativeEvent.Revision)
	require.Empty(t, nativeEvent.Message.ID)
	var payload core.Timer
	require.NoError(t, json.Unmarshal(nativeEvent.Payload, &payload))
	require.Equal(t, fired.Timer, payload)
	advance.answer <- nativeDeliveryAnswer{complete: true}
	d := waitDelivery(t, p, fired.FollowupEventID, "delivered")
	require.Equal(t, "start_or_steer", readRecord[core.Batch](t, p, "batches/"+d.BatchID).Method)
	require.Equal(t, "delivered", mcpWork[timerRecord](t, topic, "work_query", query).Followup.Status)
	require.Equal(t, rescheduled, mcpWork[core.Timer](t, topic, "timer_update", update))
	mcpWorkError(t, topic, "timer_cancel", "failed_precondition", cancelTimerInput("too-late", fired.Timer))
	update.CancelTimerRequest = cancelTimerInput("update-fired", fired.Timer)
	mcpWorkError(t, topic, "timer_update", "failed_precondition", update)
	require.Len(t, issueEvents(t, p, timerEventType), 1)
	require.Len(t, page(t, p, "messages").Items, 2)
	require.Len(t, page(t, p, "threads").Items, 2)
	require.Empty(t, page(t, p, "issues").Items)
	require.Empty(t, page(t, p, "executions").Items)
	require.Equal(t, 1, readRecord[core.Topic](t, p, "topics/"+binding.OwnerID).Revision)
	p.stop(t, false)
	p = f.start()
	require.Equal(t, fired.Timer, readRecord[timerRecord](t, p, "timers/"+fired.ID).Timer)
	require.Equal(t, d.ID, waitDelivery(t, p, fired.FollowupEventID, "delivered").ID)
	peer.quiet(t, 600*time.Millisecond)
	require.Len(t, issueEvents(t, p, timerEventType), 1)
	t.Log("Topic MCP scheduled a timezone-aware revision; real massd StartOrSteer delivered it once through a durable batch, with no source or Issue creation.")
}

func TestTimerRevisionWinsBeforeFire(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"update", "cancel"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			boundary := newRouteBoundary(t)
			f := fixtureAt(t)
			f.execution(boundary)
			gate := t.TempDir()
			p := f.start("SUPERAGENT_TEST_GATE_STAGE=timer_before_fire", "SUPERAGENT_TEST_GATE_DIR="+gate)
			t.Cleanup(func() { write(t, filepath.Join(gate, "release"), "release") })
			_, token := workBoundaryTopic(t, f, p)
			r := timerInput("past-due", timerPast)
			selected := roleWork[core.Timer](t, p, token, "timer_set", r)
			waitWorkGate(t, gate, "timer_before_fire")
			change := cancelTimerInput("race-change", selected)
			var changed core.Timer
			if action == "update" {
				update := core.UpdateTimerRequest{CancelTimerRequest: change, DueAt: timerFuture, Content: "Postpone the followup."}
				changed = roleWork[core.Timer](t, p, token, "timer_update", update)
				require.Equal(t, "scheduled", changed.Status)
				require.Equal(t, changed, roleWork[core.Timer](t, p, token, "timer_update", update))
			} else {
				changed = roleWork[core.Timer](t, p, token, "timer_cancel", change)
				require.Equal(t, "canceled", changed.Status)
				require.Equal(t, changed, roleWork[core.Timer](t, p, token, "timer_cancel", change))
				change.ExpectedRevision++
				status, data := request(t, http.MethodPost, p.endpoint, "/v1/role/timer_cancel", token, change)
				errorCode(t, status, data, http.StatusConflict, "conflict")
			}
			require.Equal(t, 2, changed.Revision)
			write(t, filepath.Join(gate, "release"), "release")
			// A newly due Timer reaching delivery proves the held scan resumed.
			marker := roleWork[core.Timer](t, p, token, "timer_set", timerInput("next-scan", timerPast))
			fired := waitTimer(t, p, marker.ID, "fired")
			waitDelivery(t, p, fired.FollowupEventID, "delivered")
			require.Equal(t, changed, readRecord[timerRecord](t, p, "timers/"+changed.ID).Timer)
			require.Equal(t, selected, roleWork[core.Timer](t, p, token, "timer_set", r))
			require.Len(t, issueEvents(t, p, timerEventType), 1)
			t.Logf("%s committed after selecting the old due revision and prevented its followup", action)
		})
	}
}

func TestTimerFireCommitCrash(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"timer_before_commit", "timer_after_commit"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			boundary := newRouteBoundary(t)
			f := fixtureAt(t)
			f.execution(boundary)
			gate := t.TempDir()
			p := f.start("SUPERAGENT_TEST_GATE_STAGE="+stage, "SUPERAGENT_TEST_GATE_DIR="+gate)
			_, token := workBoundaryTopic(t, f, p)
			r := timerInput("crash-due", timerPast)
			timer := roleWork[core.Timer](t, p, token, "timer_set", r)
			waitWorkGate(t, gate, stage)
			p.stop(t, true)
			resume := t.TempDir()
			p = f.start("SUPERAGENT_TEST_GATE_STAGE=timer_before_fire", "SUPERAGENT_TEST_GATE_DIR="+resume)
			t.Cleanup(func() { write(t, filepath.Join(resume, "release"), "release") })
			if stage == "timer_before_commit" {
				waitWorkGate(t, resume, "timer_before_fire")
				require.Equal(t, "scheduled", readRecord[timerRecord](t, p, "timers/"+timer.ID).Status)
				require.Empty(t, issueEvents(t, p, timerEventType))
			} else {
				fired := readRecord[timerRecord](t, p, "timers/"+timer.ID)
				require.Equal(t, "fired", fired.Status)
				assertTimerFollowup(t, p, fired)
			}
			require.Equal(t, timer, roleWork[core.Timer](t, p, token, "timer_set", r))
			write(t, filepath.Join(resume, "release"), "release")
			fired := waitTimer(t, p, timer.ID, "fired")
			assertTimerFollowup(t, p, fired)
			waitDelivery(t, p, fired.FollowupEventID, "delivered")
			require.Len(t, issueEvents(t, p, timerEventType), 1)
			t.Logf("SIGKILL at %s recovered an all-or-none Timer and followup transaction", stage)
		})
	}
}

func TestTimerTopicLifecycleFence(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"complete", "cancel"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			boundary := newRouteBoundary(t)
			f := fixtureAt(t)
			f.execution(boundary)
			p := f.start()
			binding, token := workBoundaryTopic(t, f, p)
			future := roleWork[core.Timer](t, p, token, "timer_set", timerInput("long-lead", timerFuture))
			p.stop(t, false)
			gate := t.TempDir()
			p = f.start("SUPERAGENT_TEST_GATE_STAGE=topic_before_rpc", "SUPERAGENT_TEST_GATE_DIR="+gate)
			t.Cleanup(func() { write(t, filepath.Join(gate, "release"), "release") })
			past := roleWork[core.Timer](t, p, token, "timer_set", timerInput("claimed-before-terminal", timerPast))
			fired := waitTimer(t, p, past.ID, "fired")
			waitWorkGate(t, gate, "topic_before_rpc")
			require.Equal(t, "dispatching", readRecord[timerRecord](t, p, "timers/"+past.ID).Followup.Status)
			var terminal core.Topic
			if action == "complete" {
				terminal = roleWork[core.Topic](t, p, token, "topic_complete", acceptance("accept-topic", 1, "notes/verified.txt"))
				require.Equal(t, "completed", terminal.Status)
			} else {
				terminal = roleWork[core.Topic](t, p, token, "topic_cancel", lifecycle("cancel-topic", 1))
				require.Equal(t, "canceled", terminal.Status)
			}
			invalidated := readRecord[timerRecord](t, p, "timers/"+future.ID)
			require.Equal(t, "invalidated", invalidated.Status)
			require.Empty(t, invalidated.FollowupEventID)
			require.Equal(t, future.Revision, invalidated.Revision)
			// Reopen a completed Topic while the old dispatch is still paused.
			if action == "complete" {
				status, data := request(t, http.MethodPost, p.endpoint, "/v1/role/timer_set", token, timerInput("terminal", timerPast))
				errorCode(t, status, data, http.StatusConflict, "failed_precondition")
				reopened := roleWork[core.Topic](t, p, token, "topic_reopen", lifecycle("reopen-topic", terminal.Revision))
				require.Equal(t, "active", reopened.Status)
				require.Greater(t, reopened.LifecycleGeneration, fired.TopicGeneration)
			}
			write(t, filepath.Join(gate, "release"), "release")
			delivery := waitDelivery(t, p, fired.FollowupEventID, "superseded")
			require.Empty(t, delivery.Receipt)
			boundary.driver.mu.Lock()
			inputs := len(boundary.driver.inputs[binding.Namespace+"/"+binding.Name])
			boundary.driver.mu.Unlock()
			require.Equal(t, 1, inputs, "past Timer did not pass final admission")
			require.Equal(t, fired.Timer, readRecord[timerRecord](t, p, "timers/"+past.ID).Timer)
			p.stop(t, false)
			p = f.start()
			require.Equal(t, invalidated, readRecord[timerRecord](t, p, "timers/"+future.ID))
			require.Equal(t, delivery.ID, waitDelivery(t, p, fired.FollowupEventID, "superseded").ID)
			require.Len(t, issueEvents(t, p, timerEventType), 1)
			t.Logf("%s invalidated scheduled work and superseded a claimed Timer at final RPC admission", action)
		})
	}
}
