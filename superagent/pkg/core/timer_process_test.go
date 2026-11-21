package core_test

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
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
	Followup *timerRoute `json:"followup"`
}

type timerRoute struct {
	ID        string `json:"event_id"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	MessageID string `json:"messageId"`
	TopicID   string `json:"topicId"`
	SentAt    string `json:"sentAt"`
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
	require.NotEmpty(t, timer.TriggerMessageID)
	require.Equal(t, timerEventType, timer.Type)
	message := readRecord[core.Message](t, p, "messages/"+timer.TriggerMessageID)
	require.Equal(t, timer.ID, message.SourceMessageID)
	require.Equal(t, core.ThreadRef{Source: "core:timer", ID: timer.ID}, message.ThreadRef)
	require.Equal(t, message.ThreadRef.Source, message.Source)
	require.Equal(t, core.ThreadKey(message.ThreadRef), timer.ThreadKey)
	require.Equal(t, timer.ThreadKey, message.ThreadKey)
	require.Equal(t, timer.Content, message.Text)
	require.Nil(t, message.Channel)
	require.Equal(t, timerEventType, message.Meta["type"])
	require.Equal(t, timer.ID, message.Meta["taskId"])
	require.Equal(t, timer.TopicID, message.Meta["topicId"])
	require.Equal(t, timer.DueAt, message.Meta["dueAt"])
	require.EqualValues(t, timer.Revision, message.Meta["taskRevision"])
	if len(timer.Materials) > 0 {
		actual, err := json.Marshal(message.Meta["materials"])
		require.NoError(t, err)
		expected, err := json.Marshal(timer.Materials)
		require.NoError(t, err)
		require.JSONEq(t, string(expected), string(actual))
	}

	type subscribedThread struct {
		core.Thread
		Subscriptions []core.Subscription `json:"subscriptions"`
	}
	thread := readRecord[subscribedThread](t, p, "threads/"+url.PathEscape(timer.ThreadKey))
	require.Equal(t, message.ThreadRef, thread.Ref)
	require.Len(t, thread.Subscriptions, 1)
	require.Equal(t, timer.TopicID, thread.Subscriptions[0].TopicID)
	require.Len(t, page(t, p, "messages?threadKey="+url.QueryEscape(timer.ThreadKey)).Items, 1)
	require.Len(t, page(t, p, "routing-events?messageId="+message.ID).Items, 1)
	internalEvent := readRecord[timerRoute](t, p, "routing-events/"+timer.Followup.ID)
	require.Equal(t, message.EventID, internalEvent.ID)
	require.Equal(t, "routed_message", internalEvent.Kind)
	require.Equal(t, timer.TopicID, internalEvent.TopicID)
	require.Equal(t, message.ID, internalEvent.MessageID)
}

func waitTimerEvent(t *testing.T, p *process, id, status string) timerRoute {
	t.Helper()
	var found timerRoute
	require.Eventually(t, func() bool {
		found = readRecord[timerRoute](t, p, "routing-events/"+id)
		return found.Status == status
	}, 7*time.Second, 25*time.Millisecond, "Timer delivery %s: waiting for %s", id, status)
	return found
}

func TestTimerNativeEventAndAuthority(t *testing.T) {
	t.Parallel()
	peer := newNativeSubmitPeer(t)
	endpoint, _ := startEventTestMassd(t, peer)
	f, _ := eventFixture(t, endpoint)
	clock, advanceClock := processClock(t, f, "SUPERAGENT_TEST_TIMER_CLOCK")
	receiver, spans := coreOTLPReceiver(t, "")
	p := f.start(append(traceTestEnv(receiver.URL), clock)...)
	routerBinding := waitBinding(t, p, "router", "", "ready")
	router := startRoleMCP(t, f, routerBinding)
	binding, source := nativeWorkTopic(t, f, p, peer, router, "timer-main")
	foreignBinding, foreignSource := nativeWorkTopic(t, f, p, peer, router, "timer-foreign")
	topic := startRoleMCP(t, f, binding)
	foreign := startRoleMCP(t, f, foreignBinding)

	tools := topic.call("tools/list", struct{}{})
	for _, name := range []string{"schedule_followup", "update_followup", "cancel_followup"} {
		require.Contains(t, string(tools["result"]), name)
		require.NotContains(t, string(router.call("tools/list", struct{}{})["result"]), name)
	}
	r := timerInput("set-followup", timerFuture)
	for _, due := range []string{"2090-01-02T08:00:00", "2090-01-02T08:00:00+24:00", "2090-01-02T08:00:00+01:60"} {
		mcpWorkError(t, topic, "schedule_followup", "invalid_argument", timerInput("invalid-zone", due))
	}
	mcpWorkError(t, topic, "schedule_followup", "invalid_argument", map[string]any{
		"operationId": r.OperationID, "dueAt": r.DueAt, "content": r.Content, "topicId": foreignBinding.OwnerID,
	})
	status, body := requestMCPTool(t, p.endpoint, "schedule_followup", roleToken(t, f, routerBinding), r)
	errorCode(t, status, body, http.StatusForbidden, "forbidden")
	wrongMaterial := r
	wrongMaterial.Materials = []core.Material{{MessageID: foreignSource.MessageID}}
	mcpWorkError(t, topic, "schedule_followup", "not_found", wrongMaterial)

	r.Materials = []core.Material{{Label: "Original request", MessageID: source.MessageID}, {URI: "notes/build.txt"}}
	scheduled := mcpWork[core.Timer](t, topic, "schedule_followup", r)
	require.Equal(t, "mass-t1-tm1", scheduled.ID)
	require.Equal(t, binding.OwnerID, scheduled.TopicID)
	require.Equal(t, "scheduled", scheduled.Status)
	require.Equal(t, 1, scheduled.Revision)
	require.Equal(t, "2090-01-02T00:00:00.012000000Z", scheduled.DueAt)
	require.Equal(t, r.Materials, scheduled.Materials)
	require.Equal(t, scheduled, mcpWork[core.Timer](t, topic, "schedule_followup", r))
	createdTrace := observedTraces(t, p, "timers", scheduled.ID)
	require.Len(t, createdTrace, 1)
	changedRequest := r
	changedRequest.Content = "Different content under the same operation ID"
	mcpWorkError(t, topic, "schedule_followup", "conflict", changedRequest)
	mcpWorkError(t, foreign, "timer_cancel", "not_found", cancelTimerInput("foreign-cancel", scheduled))
	mcpWorkError(t, foreign, "timer_update", "not_found", core.UpdateTimerRequest{
		CancelTimerRequest: cancelTimerInput("foreign-update", scheduled), DueAt: timerPast, Content: r.Content,
	})
	require.Empty(t, mcpWork[struct{ Followups []timerRecord }](t, foreign, "get_topic", map[string][]string{"include": {"followups"}}).Followups)
	foreignTimer := mcpWork[core.Timer](t, foreign, "schedule_followup", timerInput(r.OperationID, timerFuture))
	require.Equal(t, "mass-t2-tm1", foreignTimer.ID)

	// Fire the revised task only when the controlled clock reaches its due time.
	due := advanceClock(0).Add(time.Minute).In(time.FixedZone("UTC+8", 8*60*60))
	update := core.UpdateTimerRequest{
		CancelTimerRequest: cancelTimerInput("reschedule", scheduled),
		DueAt:              due.Format(time.RFC3339Nano), Content: "Review the revised build evidence.", Materials: r.Materials,
	}
	rescheduled := mcpWork[core.Timer](t, topic, "timer_update", update)
	require.Equal(t, scheduled.ID, rescheduled.ID)
	require.Equal(t, 2, rescheduled.Revision)
	require.Equal(t, update.Content, rescheduled.Content)
	revisions := observedTraces(t, p, "timers", scheduled.ID)
	require.Len(t, revisions, 2)
	require.Equal(t, createdTrace[0], revisions[1])
	require.Equal(t, "revision", revisions[0].Phase)
	require.NotEqual(t, revisions[1].TraceID, revisions[0].TraceID)
	require.Equal(t, 2, mcpWorkError(t, topic, "timer_cancel", "conflict", cancelTimerInput("stale-cancel", scheduled)).CurrentRevision)
	require.Never(t, func() bool {
		return readRecord[timerRecord](t, p, "timers/"+scheduled.ID).Status == "fired"
	}, 350*time.Millisecond, 25*time.Millisecond)
	advanceClock(time.Minute)
	fired := waitTimer(t, p, scheduled.ID, "fired")
	at, err := time.Parse(time.RFC3339Nano, fired.FiredAt)
	require.NoError(t, err)
	require.True(t, at.Equal(due), "Timer must use the controlled due instant")
	assertTimerFollowup(t, p, fired)
	advance := peer.next(t, "turn/start")
	require.Equal(t, binding.SessionID, advance.session)
	nativeEvent := decodeInternalBatch(t, advance, fired.Followup.ID)[0]
	require.Equal(t, "routed_message", nativeEvent.Kind)
	require.NotNil(t, nativeEvent.Content)
	require.Len(t, nativeEvent.Refs, 1)
	require.JSONEq(t, `{"kind":"trigger_message","id":"`+fired.TriggerMessageID+`"}`, string(nativeEvent.Refs[0]))
	message := roleWork[core.Message](t, p, roleToken(t, f, binding), "get_message", map[string]string{"messageId": fired.TriggerMessageID})
	require.Equal(t, fired.Content, message.Text)
	require.Equal(t, "followup", message.Meta["type"])
	require.Equal(t, fired.ID, message.Meta["followupId"])
	materials, err := json.Marshal(message.Meta["materials"])
	require.NoError(t, err)
	var sourceMaterials []core.Material
	require.NoError(t, json.Unmarshal(materials, &sourceMaterials))
	require.Equal(t, fired.Materials, sourceMaterials)
	mcpWorkError(t, topic, "send_reply", "failed_precondition", core.ReplyRequest{OperationID: "timer-reply", MessageID: message.ID, Content: markdownReply("fixture")})
	advance.answer <- nativeSubmitAnswer{complete: true}
	d := waitTimerEvent(t, p, fired.Followup.ID, "sent")
	require.NotEmpty(t, d.SentAt)
	followups := mcpWork[struct{ Followups []timerRecord }](t, topic, "get_topic", map[string][]string{"include": {"followups"}}).Followups
	require.Len(t, followups, 1)
	require.Equal(t, d, *followups[0].Followup)
	mcpWorkError(t, topic, "timer_update", "conflict", update)
	mcpWorkError(t, topic, "timer_cancel", "failed_precondition", cancelTimerInput("too-late", fired.Timer))
	update.CancelTimerRequest = cancelTimerInput("update-fired", fired.Timer)
	mcpWorkError(t, topic, "timer_update", "failed_precondition", update)
	require.Len(t, page(t, p, "messages").Items, 3)
	require.Len(t, page(t, p, "threads").Items, 3)
	require.Empty(t, page(t, p, "issues").Items)
	require.Empty(t, issueBindings(t, p))
	require.Equal(t, 1, readRecord[core.Topic](t, p, "topics/"+binding.OwnerID).Revision)
	firedTrace := observedTraces(t, p, "messages", fired.TriggerMessageID)
	require.Len(t, firedTrace, 1)
	p.stop(t, false)
	var linked bool
	for _, span := range spans() {
		if span.GetName() == "superagent.timer.fire" && spanLinksTrace(span, revisions[0].TraceID) {
			linked = true
			require.Len(t, span.GetLinks(), 1)
			require.Equal(t, firedTrace[0].TraceID, hex.EncodeToString(span.GetTraceId()))
		}
	}
	require.True(t, linked, "timer firing links to the committed current revision")
	p = f.start(clock)
	restarted := readRecord[timerRecord](t, p, "timers/"+fired.ID)
	require.Equal(t, fired.Timer, restarted.Timer)
	require.Equal(t, d, *restarted.Followup)
	token := roleToken(t, f, binding)
	retried := roleWork[core.Timer](t, p, token, "schedule_followup", r)
	require.Equal(t, fired.ID, retried.ID)
	require.Equal(t, fired.Status, retried.Status)
	require.Equal(t, fired.Revision, retried.Revision)
	next := roleWork[core.Timer](t, p, token, "schedule_followup", timerInput("next-followup", timerFuture))
	require.Equal(t, "mass-t1-tm2", next.ID)
	peer.quiet(t, 600*time.Millisecond)
	assertTimerFollowup(t, p, restarted)
	t.Log("Controlled time fired exactly one normal ingress Message. Real massd StartOrSteer accepted its routed_message.")
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
			selected := roleWork[core.Timer](t, p, token, "schedule_followup", r)
			waitWorkGate(t, gate, "timer_before_fire")
			change := cancelTimerInput("race-change", selected)
			var changed core.Timer
			if action == "update" {
				update := core.UpdateTimerRequest{CancelTimerRequest: change, DueAt: timerFuture, Content: "Postpone the followup."}
				changed = roleWork[core.Timer](t, p, token, "timer_update", update)
				require.Equal(t, "scheduled", changed.Status)
				status, data := requestMCPTool(t, p.endpoint, "timer_update", token, update)
				errorCode(t, status, data, http.StatusConflict, "conflict")
			} else {
				changed = roleWork[core.Timer](t, p, token, "timer_cancel", change)
				require.Equal(t, "canceled", changed.Status)
				status, data := requestMCPTool(t, p.endpoint, "timer_cancel", token, change)
				errorCode(t, status, data, http.StatusConflict, "conflict")
				change.ExpectedRevision++
				change.OperationID = "race-change-again"
				status, data = requestMCPTool(t, p.endpoint, "timer_cancel", token, change)
				errorCode(t, status, data, http.StatusConflict, "failed_precondition")
			}
			require.Equal(t, 2, changed.Revision)
			write(t, filepath.Join(gate, "release"), "release")
			// Dispatching a newly due followup proves the held scan resumed.
			marker := roleWork[core.Timer](t, p, token, "schedule_followup", timerInput("next-scan", timerPast))
			fired := waitTimer(t, p, marker.ID, "fired")
			waitTimerEvent(t, p, fired.Followup.ID, "sent")
			require.Equal(t, changed, readRecord[timerRecord](t, p, "timers/"+changed.ID).Timer)
			require.Equal(t, changed, roleWork[core.Timer](t, p, token, "schedule_followup", r))
			require.Len(t, page(t, p, "messages").Items, 2)
			require.Len(t, page(t, p, "threads").Items, 2)
			assertTimerFollowup(t, p, fired)
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
			clock, advanceClock := processClock(t, f, "SUPERAGENT_TEST_TIMER_CLOCK")
			gate := t.TempDir()
			p := f.start(clock, "SUPERAGENT_TEST_GATE_STAGE="+stage, "SUPERAGENT_TEST_GATE_DIR="+gate)
			t.Cleanup(func() { write(t, filepath.Join(gate, "release"), "release") })
			_, token := workBoundaryTopic(t, f, p)
			r := timerInput("crash-due", advanceClock(0).Add(time.Minute).Format(time.RFC3339Nano))
			timer := roleWork[core.Timer](t, p, token, "schedule_followup", r)
			// Let the scheduling receipt and its usage record commit before
			// the crash gate holds the timer firing transaction open.
			advanceClock(time.Minute)
			waitWorkGate(t, gate, stage)
			p.stop(t, true)
			resume := t.TempDir()
			p = f.start(clock, "SUPERAGENT_TEST_GATE_STAGE=timer_before_fire", "SUPERAGENT_TEST_GATE_DIR="+resume)
			t.Cleanup(func() { write(t, filepath.Join(resume, "release"), "release") })
			if stage == "timer_before_commit" {
				waitWorkGate(t, resume, "timer_before_fire")
				require.Equal(t, "scheduled", readRecord[timerRecord](t, p, "timers/"+timer.ID).Status)
				require.Len(t, page(t, p, "messages").Items, 1)
				require.Len(t, page(t, p, "threads").Items, 1)
			} else {
				fired := readRecord[timerRecord](t, p, "timers/"+timer.ID)
				require.Equal(t, "fired", fired.Status)
				assertTimerFollowup(t, p, fired)
			}
			require.Equal(t, timer.ID, roleWork[core.Timer](t, p, token, "schedule_followup", r).ID)
			write(t, filepath.Join(resume, "release"), "release")
			fired := waitTimer(t, p, timer.ID, "fired")
			waitTimerEvent(t, p, fired.Followup.ID, "sent")
			assertTimerFollowup(t, p, fired)
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
			clock, advanceClock := processClock(t, f, "SUPERAGENT_TEST_TIMER_CLOCK")
			p := f.start(clock)
			binding, token := workBoundaryTopic(t, f, p)
			due := advanceClock(0).Add(time.Minute).Format(time.RFC3339Nano)
			future := roleWork[core.Timer](t, p, token, "schedule_followup", timerInput("long-lead", due))
			p.stop(t, false)
			gate := t.TempDir()
			p = f.start(clock, "SUPERAGENT_TEST_GATE_STAGE=topic_before_rpc", "SUPERAGENT_TEST_GATE_DIR="+gate)
			t.Cleanup(func() { write(t, filepath.Join(gate, "release"), "release") })
			past := roleWork[core.Timer](t, p, token, "schedule_followup", timerInput("claimed-before-terminal", timerPast))
			fired := waitTimer(t, p, past.ID, "fired")
			waitWorkGate(t, gate, "topic_before_rpc")
			require.Equal(t, "pending", readRecord[timerRecord](t, p, "timers/"+past.ID).Followup.Status)
			var terminal core.Topic
			if action == "complete" {
				terminal = roleWork[core.Topic](t, p, token, "topic_complete", acceptance("accept-topic", 1, "notes/verified.txt"))
				require.Equal(t, "completed", terminal.Status)
			} else {
				terminal = roleWork[core.Topic](t, p, token, "topic_cancel", lifecycle("cancel-topic", 1))
				require.Equal(t, "canceled", terminal.Status)
			}
			require.Equal(t, terminal.Status, readRecord[core.Topic](t, p, "topics/"+binding.OwnerID).Status)
			invalidated := readRecord[timerRecord](t, p, "timers/"+future.ID)
			require.Equal(t, "invalidated", invalidated.Status)
			require.Empty(t, invalidated.TriggerMessageID)
			require.Equal(t, future.Revision, invalidated.Revision)
			// Reopen a completed Topic while the old dispatch is still paused.
			if action == "complete" {
				status, data := requestMCPTool(t, p.endpoint, "schedule_followup", token, timerInput("terminal", timerPast))
				errorCode(t, status, data, http.StatusConflict, "failed_precondition")
				reopened := roleWork[core.Topic](t, p, token, "topic_reopen", lifecycle("reopen-topic", terminal.Revision))
				require.Equal(t, "active", reopened.Status)
				require.Greater(t, reopened.LifecycleGeneration, fired.TopicGeneration)
				next := roleWork[core.Timer](t, p, token, "schedule_followup", timerInput("reopened-timer", timerFuture))
				require.Equal(t, "mass-t1-tm3", next.ID)
			}
			write(t, filepath.Join(gate, "release"), "release")
			internalEvent := waitTimerEvent(t, p, fired.Followup.ID, "superseded")
			require.Empty(t, internalEvent.SentAt)
			boundary.driver.mu.Lock()
			inputs := len(boundary.driver.inputs[binding.Namespace+"/"+binding.Name])
			boundary.driver.mu.Unlock()
			require.Equal(t, 1, inputs, "past Timer did not pass final admission")
			require.Equal(t, fired.Timer, readRecord[timerRecord](t, p, "timers/"+past.ID).Timer)
			p.stop(t, false)
			advanceClock(time.Minute)
			p = f.start(clock)
			require.Len(t, page(t, p, "messages").Items, 2)
			require.Len(t, page(t, p, "threads").Items, 2)
			require.Equal(t, invalidated, readRecord[timerRecord](t, p, "timers/"+future.ID))
			require.Equal(t, internalEvent, waitTimerEvent(t, p, fired.Followup.ID, "superseded"))
			assertTimerFollowup(t, p, fired)
			t.Logf("%s invalidated scheduled work and superseded a claimed Timer at final RPC admission", action)
		})
	}
}
