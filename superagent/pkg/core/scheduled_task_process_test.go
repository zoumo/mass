package core_test

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/superagent/pkg/core"
)

const (
	scheduledTaskPast  = "2020-01-02T08:00:00.012+08:00"
	scheduledTaskLater = "2090-01-02T08:00:00.012+08:00"
)

func scheduledTaskInput(op, runAt string) core.CreateScheduledTaskRequest {
	return core.CreateScheduledTaskRequest{
		OperationID: op,
		Schedule:    core.ScheduledTaskSchedule{Type: "once", RunAt: runAt},
		Content:     []events.ContentBlock{events.Text("Review the build evidence.")},
	}
}

func scheduledTaskRevision(op string, task core.ScheduledTask) core.ScheduledTaskRevision {
	return core.ScheduledTaskRevision{OperationID: op, ScheduledTaskID: task.ID, ExpectedRevision: task.Revision}
}

func scheduledTaskGetArgs(id string) map[string]string {
	return map[string]string{"scheduledTaskId": id}
}

func listTopicScheduledTasks(t *testing.T, topic *roleMCPClient) []core.ScheduledTask {
	t.Helper()
	return mcpWork[struct{ ScheduledTasks []core.ScheduledTask }](t, topic, "get_topic", map[string][]string{"include": {"scheduledTasks"}}).ScheduledTasks
}

func waitScheduledTask(t *testing.T, p *process, token, id, status string) core.ScheduledTask {
	t.Helper()
	var task core.ScheduledTask
	require.Eventually(t, func() bool {
		task = roleWork[core.ScheduledTask](t, p, token, "get_scheduled_task", scheduledTaskGetArgs(id))
		return task.Status == status
	}, 6*time.Second, 25*time.Millisecond, "ScheduledTask %s should be %s", id, status)
	return task
}

func scheduledTaskHistory(t *testing.T, p *process, operatorFile, id string) []core.Message {
	t.Helper()
	token, err := core.ReadOperatorToken(operatorFile)
	require.NoError(t, err)
	status, body := request(t, http.MethodGet, p.endpoint, "/apis/core/v1/scheduled-tasks/"+id+"/trigger-messages", token, nil)
	require.Equal(t, http.StatusOK, status, string(body))
	var history struct{ Items []core.Message }
	require.NoError(t, json.Unmarshal(body, &history))
	return history.Items
}

func waitScheduledTaskHistory(t *testing.T, p *process, operatorFile, id string, count int) []core.Message {
	t.Helper()
	var history []core.Message
	require.Eventually(t, func() bool {
		history = scheduledTaskHistory(t, p, operatorFile, id)
		return len(history) == count
	}, 6*time.Second, 25*time.Millisecond, "ScheduledTask %s should have %d Trigger Messages", id, count)
	return history
}

func TestScheduledTaskTopicMCPAndNativeDelivery(t *testing.T) {
	t.Parallel()
	peer := newNativeSubmitPeer(t)
	endpoint, _ := startEventTestMassd(t, peer)
	f, operatorFile := eventFixture(t, endpoint)
	clock, advanceClock := processClock(t, f, "SUPERAGENT_TEST_SCHEDULED_TASK_CLOCK")
	receiver, spans := coreOTLPReceiver(t, "")
	p := f.start(append(traceTestEnv(receiver.URL), clock)...)
	routerBinding := waitBinding(t, p, "router", "", "ready")
	router := startRoleMCP(t, f, routerBinding)
	binding, _ := nativeWorkTopic(t, f, p, peer, router, "scheduled-task-main")
	foreignBinding, _ := nativeWorkTopic(t, f, p, peer, router, "scheduled-task-foreign")
	topic := startRoleMCP(t, f, binding)
	foreign := startRoleMCP(t, f, foreignBinding)
	topicToken := roleToken(t, f, binding)

	topicTools := string(topic.call("tools/list", struct{}{})["result"])
	routerTools := string(router.call("tools/list", struct{}{})["result"])
	for _, name := range []string{"create_scheduled_task", "get_scheduled_task", "update_scheduled_task", "delete_scheduled_task"} {
		require.Contains(t, topicTools, name)
		require.NotContains(t, routerTools, name)
	}
	for _, name := range []string{"schedule_followup", "update_followup", "cancel_followup"} {
		require.NotContains(t, topicTools, name)
	}
	for _, due := range []string{"2090-01-02T08:00:00", "2090-01-02T08:00:00+24:00", "2090-01-02T08:00:00+01:60"} {
		mcpWorkError(t, topic, "create_scheduled_task", "invalid_argument", scheduledTaskInput("invalid-zone", due))
	}
	r := scheduledTaskInput("create", scheduledTaskLater)
	mcpWorkError(t, topic, "create_scheduled_task", "invalid_argument", map[string]any{
		"operationId": r.OperationID, "schedule": r.Schedule, "content": r.Content,
		"owner": map[string]string{"type": "topic", "id": foreignBinding.OwnerID},
	})
	mcpWorkError(t, topic, "create_scheduled_task", "invalid_argument", map[string]any{
		"operationId": r.OperationID, "schedule": r.Schedule, "content": []events.ContentBlock{},
	})
	mcpWorkError(t, router, "create_scheduled_task", "forbidden", r)

	task := mcpWork[core.ScheduledTask](t, topic, "create_scheduled_task", r)
	require.NotEmpty(t, task.ID)
	require.NotNil(t, task.Owner)
	require.Equal(t, "topic", task.Owner.Type)
	require.Equal(t, binding.OwnerID, task.Owner.ID)
	require.Equal(t, "active", task.Status)
	require.Equal(t, 1, task.Revision)
	require.Equal(t, "2090-01-02T00:00:00.012000000Z", task.NextRunAt)
	require.Equal(t, task, mcpWork[core.ScheduledTask](t, topic, "create_scheduled_task", r))
	require.Equal(t, task, mcpWork[core.ScheduledTask](t, topic, "get_scheduled_task", scheduledTaskGetArgs(task.ID)))
	require.Equal(t, []core.ScheduledTask{task}, listTopicScheduledTasks(t, topic))
	changedRequest := r
	changedRequest.Content = []events.ContentBlock{events.Text("Different content under the same operation ID")}
	mcpWorkError(t, topic, "create_scheduled_task", "conflict", changedRequest)
	mcpWorkError(t, foreign, "get_scheduled_task", "not_found", scheduledTaskGetArgs(task.ID))
	mcpWorkError(t, foreign, "delete_scheduled_task", "not_found", scheduledTaskRevision("foreign-delete", task))
	mcpWorkError(t, foreign, "update_scheduled_task", "not_found", map[string]any{
		"operationId": "foreign-update", "scheduledTaskId": task.ID, "expectedRevision": task.Revision, "status": "paused",
	})
	mcpWorkError(t, topic, "update_scheduled_task", "invalid_argument", map[string]any{
		"operationId": "null-update", "scheduledTaskId": task.ID, "expectedRevision": task.Revision,
		"schedule": nil, "status": "paused",
	})
	require.Empty(t, listTopicScheduledTasks(t, foreign))
	foreignTask := mcpWork[core.ScheduledTask](t, foreign, "create_scheduled_task", r)
	require.NotEqual(t, task.ID, foreignTask.ID)

	due := advanceClock(0).Add(time.Minute)
	content := []events.ContentBlock{events.Text("Review the revised build evidence."), events.Text("Read the latest report first.")}
	rescheduled := mcpWork[core.ScheduledTask](t, topic, "update_scheduled_task", map[string]any{
		"operationId": "reschedule", "scheduledTaskId": task.ID, "expectedRevision": task.Revision,
		"schedule": core.ScheduledTaskSchedule{Type: "once", RunAt: due.Format(time.RFC3339Nano)}, "content": content,
	})
	require.Equal(t, task.ID, rescheduled.ID)
	require.Equal(t, 2, rescheduled.Revision)
	require.Equal(t, content, rescheduled.Content)
	require.Equal(t, 2, mcpWorkError(t, topic, "delete_scheduled_task", "conflict", scheduledTaskRevision("stale-delete", task)).CurrentRevision)
	require.Never(t, func() bool {
		return len(scheduledTaskHistory(t, p, operatorFile, task.ID)) != 0
	}, 350*time.Millisecond, 25*time.Millisecond)
	advanceClock(time.Minute)
	completed := waitScheduledTask(t, p, topicToken, task.ID, "completed")
	require.Empty(t, completed.NextRunAt)
	message := waitScheduledTaskHistory(t, p, operatorFile, task.ID, 1)[0]
	require.Equal(t, "core:scheduled-task", message.Source)
	require.Equal(t, content, message.Content)
	require.Empty(t, message.ReplyTarget)

	advance := peer.next(t, "turn/start")
	require.Equal(t, binding.SessionID, advance.session)
	event := decodeInternalBatch(t, advance, message.EventID)[0]
	require.Equal(t, "routed_message", event.Kind)
	require.JSONEq(t, `{"kind":"trigger_message","id":"`+message.ID+`"}`, string(event.Refs[0]))
	require.Equal(t, []attachedMessage{{MessageID: message.ID, BlockCount: 2}}, event.MessageContent)
	require.Len(t, advance.blocks, 3)
	require.Equal(t, content[0].Text.Text, advance.blocks[1].Text)
	require.Equal(t, content[1].Text.Text, advance.blocks[2].Text)
	view := mcpWork[map[string]json.RawMessage](t, topic, "get_message", map[string]string{"messageId": message.ID})
	require.NotContains(t, view, "scheduledTaskId")
	require.NotContains(t, view, "followup")
	mcpWorkError(t, topic, "send_reply", "failed_precondition", core.ReplyRequest{OperationID: "scheduled-message-reply", MessageID: message.ID, Content: markdownReply("fixture")})
	advance.answer <- nativeSubmitAnswer{complete: true}
	waitWorkEvent(t, p, message.EventID, "sent")

	large := scheduledTaskInput("large", scheduledTaskPast)
	large.Content = []events.ContentBlock{events.Text(strings.Repeat("long content ", 300))}
	largeTask := mcpWork[core.ScheduledTask](t, topic, "create_scheduled_task", large)
	largeMessage := waitScheduledTaskHistory(t, p, operatorFile, largeTask.ID, 1)[0]
	byReference := peer.next(t, "turn/start")
	require.Equal(t, binding.SessionID, byReference.session)
	event = decodeInternalBatch(t, byReference, largeMessage.EventID)[0]
	require.Empty(t, event.MessageContent)
	require.JSONEq(t, `{"kind":"trigger_message","id":"`+largeMessage.ID+`"}`, string(event.Refs[0]))
	require.Equal(t, large.Content, mcpWork[core.Message](t, topic, "get_message", map[string]string{"messageId": largeMessage.ID}).Content)
	byReference.answer <- nativeSubmitAnswer{complete: true}
	waitWorkEvent(t, p, largeMessage.EventID, "sent")

	mcpWorkError(t, topic, "update_scheduled_task", "failed_precondition", map[string]any{
		"operationId": "reopen-task", "scheduledTaskId": completed.ID, "expectedRevision": completed.Revision, "status": "active",
	})
	deleted := mcpWork[map[string]string](t, topic, "delete_scheduled_task", scheduledTaskRevision("delete", completed))
	require.Equal(t, task.ID, deleted["scheduledTaskId"])
	mcpWorkError(t, topic, "get_scheduled_task", "not_found", scheduledTaskGetArgs(task.ID))
	require.Len(t, listTopicScheduledTasks(t, topic), 1)
	require.Equal(t, []core.Message{message}, scheduledTaskHistory(t, p, operatorFile, task.ID))
	require.Empty(t, issueBindings(t, p))

	taskTraces := observedTraces(t, p, "trigger-messages", message.ID)
	require.Len(t, taskTraces, 1)
	p.stop(t, false)
	var traced bool
	for _, span := range spans() {
		if span.GetName() == "superagent.scheduled_task.fire" && hex.EncodeToString(span.GetTraceId()) == taskTraces[0].TraceID {
			traced = true
			require.NotEmpty(t, span.GetLinks())
		}
	}
	require.True(t, traced, "trigger admission is traced from the scheduled task revision")
}

func TestScheduledTaskRevisionWinsBeforeFire(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"update", "pause", "delete"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			boundary := newRouteBoundary(t)
			f, operatorFile := eventFixture(t, boundary.url)
			gate := t.TempDir()
			p := f.start("SUPERAGENT_TEST_GATE_STAGE=scheduled_task_before_fire", "SUPERAGENT_TEST_GATE_DIR="+gate)
			t.Cleanup(func() { write(t, filepath.Join(gate, "release"), "release") })
			_, token := workBoundaryTopic(t, f, p)
			selected := roleWork[core.ScheduledTask](t, p, token, "create_scheduled_task", scheduledTaskInput("past-due", scheduledTaskPast))
			waitWorkGate(t, gate, "scheduled_task_before_fire")
			fields := map[string]any{"operationId": "race-change", "scheduledTaskId": selected.ID, "expectedRevision": selected.Revision}
			switch action {
			case "update":
				fields["schedule"] = core.ScheduledTaskSchedule{Type: "once", RunAt: scheduledTaskLater}
				changed := roleWork[core.ScheduledTask](t, p, token, "update_scheduled_task", fields)
				require.Equal(t, "active", changed.Status)
				require.Equal(t, selected.Revision+1, changed.Revision)
			case "pause":
				fields["status"] = "paused"
				changed := roleWork[core.ScheduledTask](t, p, token, "update_scheduled_task", fields)
				require.Equal(t, "paused", changed.Status)
			case "delete":
				roleWork[map[string]string](t, p, token, "delete_scheduled_task", fields)
			}
			write(t, filepath.Join(gate, "release"), "release")
			marker := roleWork[core.ScheduledTask](t, p, token, "create_scheduled_task", scheduledTaskInput("next-scan", scheduledTaskPast))
			waitScheduledTaskHistory(t, p, operatorFile, marker.ID, 1)
			require.Empty(t, scheduledTaskHistory(t, p, operatorFile, selected.ID))
		})
	}
}

func TestScheduledTaskFireCommitCrash(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"scheduled_task_before_commit", "scheduled_task_after_commit"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			boundary := newRouteBoundary(t)
			f, operatorFile := eventFixture(t, boundary.url)
			clock, advanceClock := processClock(t, f, "SUPERAGENT_TEST_SCHEDULED_TASK_CLOCK")
			gate := t.TempDir()
			p := f.start(clock, "SUPERAGENT_TEST_GATE_STAGE="+stage, "SUPERAGENT_TEST_GATE_DIR="+gate)
			t.Cleanup(func() { write(t, filepath.Join(gate, "release"), "release") })
			_, token := workBoundaryTopic(t, f, p)
			r := scheduledTaskInput("crash-due", advanceClock(0).Add(time.Minute).Format(time.RFC3339Nano))
			task := roleWork[core.ScheduledTask](t, p, token, "create_scheduled_task", r)
			advanceClock(time.Minute)
			waitWorkGate(t, gate, stage)
			p.stop(t, true)
			// Restart at the exact scheduled instant. An uncommitted trigger is
			// still due while an accepted trigger retains its Message and Event.
			resume := t.TempDir()
			p = f.start(clock, "SUPERAGENT_TEST_GATE_STAGE=scheduled_task_before_fire", "SUPERAGENT_TEST_GATE_DIR="+resume)
			t.Cleanup(func() { write(t, filepath.Join(resume, "release"), "release") })
			var accepted []core.Message
			if stage == "scheduled_task_before_commit" {
				waitWorkGate(t, resume, "scheduled_task_before_fire")
				require.Equal(t, "active", roleWork[core.ScheduledTask](t, p, token, "get_scheduled_task", scheduledTaskGetArgs(task.ID)).Status)
				require.Empty(t, scheduledTaskHistory(t, p, operatorFile, task.ID))
			} else {
				require.Equal(t, "completed", waitScheduledTask(t, p, token, task.ID, "completed").Status)
				accepted = scheduledTaskHistory(t, p, operatorFile, task.ID)
				require.Len(t, accepted, 1)
			}
			require.Equal(t, task.ID, roleWork[core.ScheduledTask](t, p, token, "create_scheduled_task", r).ID)
			write(t, filepath.Join(resume, "release"), "release")
			history := waitScheduledTaskHistory(t, p, operatorFile, task.ID, 1)
			if accepted != nil {
				require.Equal(t, accepted, history)
			}
			waitWorkEvent(t, p, history[0].EventID, "sent")
			require.Len(t, page(t, p, "internal-events?messageId="+history[0].ID).Items, 1)
		})
	}
}

func TestScheduledTaskSkipsRestartAndPauseWindows(t *testing.T) {
	t.Parallel()
	boundary := newRouteBoundary(t)
	f, operatorFile := eventFixture(t, boundary.url)
	clock, advanceClock := processClock(t, f, "SUPERAGENT_TEST_SCHEDULED_TASK_CLOCK")
	p := f.start(clock)
	_, token := workBoundaryTopic(t, f, p)
	start := advanceClock(0)
	first := start.Add(time.Minute).Format(time.RFC3339Nano)
	once := roleWork[core.ScheduledTask](t, p, token, "create_scheduled_task", scheduledTaskInput("missed-once", first))
	recurringInput := scheduledTaskInput("recurring", first)
	recurringInput.Schedule = core.ScheduledTaskSchedule{Type: "interval", Every: json.RawMessage(`"1m"`)}
	recurring := roleWork[core.ScheduledTask](t, p, token, "create_scheduled_task", recurringInput)
	p.stop(t, false)
	advanceClock(3*time.Minute + time.Second)
	p = f.start(clock)
	waitScheduledTask(t, p, token, once.ID, "completed")
	require.Empty(t, scheduledTaskHistory(t, p, operatorFile, once.ID))
	require.Empty(t, scheduledTaskHistory(t, p, operatorFile, recurring.ID))
	resumed := roleWork[core.ScheduledTask](t, p, token, "get_scheduled_task", scheduledTaskGetArgs(recurring.ID))
	require.Equal(t, start.Add(4*time.Minute).Format("2006-01-02T15:04:05.000000000Z"), resumed.NextRunAt)
	advanceClock(4 * time.Minute)
	firstMessage := waitScheduledTaskHistory(t, p, operatorFile, recurring.ID, 1)[0]
	waitWorkEvent(t, p, firstMessage.EventID, "sent")
	paused := roleWork[core.ScheduledTask](t, p, token, "update_scheduled_task", map[string]any{
		"operationId": "pause", "scheduledTaskId": recurring.ID, "expectedRevision": resumed.Revision, "status": "paused",
	})
	require.Empty(t, paused.NextRunAt)
	advanceClock(7*time.Minute + time.Second)
	resumed = roleWork[core.ScheduledTask](t, p, token, "update_scheduled_task", map[string]any{
		"operationId": "resume", "scheduledTaskId": recurring.ID, "expectedRevision": paused.Revision, "status": "active",
	})
	require.Equal(t, start.Add(8*time.Minute).Format("2006-01-02T15:04:05.000000000Z"), resumed.NextRunAt)
	require.Len(t, scheduledTaskHistory(t, p, operatorFile, recurring.ID), 1)
	advanceClock(8 * time.Minute)
	history := waitScheduledTaskHistory(t, p, operatorFile, recurring.ID, 2)
	require.Equal(t, history[0].ThreadID, history[1].ThreadID)
	require.NotEqual(t, history[0].ID, history[1].ID)
	require.NotEqual(t, history[0].EventID, history[1].EventID)
}

func TestScheduledTaskTopicLifecycleFence(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"complete", "cancel"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			boundary := newRouteBoundary(t)
			f, operatorFile := eventFixture(t, boundary.url)
			clock, advanceClock := processClock(t, f, "SUPERAGENT_TEST_SCHEDULED_TASK_CLOCK")
			p := f.start(clock)
			binding, token := workBoundaryTopic(t, f, p)
			due := advanceClock(0).Add(time.Minute).Format(time.RFC3339Nano)
			future := roleWork[core.ScheduledTask](t, p, token, "create_scheduled_task", scheduledTaskInput("long-lead", due))
			p.stop(t, false)
			gate := t.TempDir()
			p = f.start(clock, "SUPERAGENT_TEST_GATE_STAGE=topic_before_rpc", "SUPERAGENT_TEST_GATE_DIR="+gate)
			t.Cleanup(func() { write(t, filepath.Join(gate, "release"), "release") })
			past := roleWork[core.ScheduledTask](t, p, token, "create_scheduled_task", scheduledTaskInput("claimed-before-terminal", scheduledTaskPast))
			message := waitScheduledTaskHistory(t, p, operatorFile, past.ID, 1)[0]
			waitWorkGate(t, gate, "topic_before_rpc")
			var terminal core.Topic
			if action == "complete" {
				terminal = roleWork[core.Topic](t, p, token, "topic_complete", acceptance("accept-topic", 1, "notes/verified.txt"))
				require.Equal(t, "completed", terminal.Status)
				require.Equal(t, "completed", waitScheduledTask(t, p, token, future.ID, "completed").Status)
				status, data := requestMCPTool(t, p.endpoint, "create_scheduled_task", token, scheduledTaskInput("terminal", scheduledTaskPast))
				errorCode(t, status, data, http.StatusConflict, "failed_precondition")
				reopened := roleWork[core.Topic](t, p, token, "topic_reopen", lifecycle("reopen-topic", terminal.Revision))
				require.Equal(t, "active", reopened.Status)
				roleWork[map[string]string](t, p, token, "delete_scheduled_task", scheduledTaskRevision("delete-finished", future))
			} else {
				terminal = roleWork[core.Topic](t, p, token, "topic_cancel", lifecycle("cancel-topic", 1))
				require.Equal(t, "canceled", terminal.Status)
			}
			write(t, filepath.Join(gate, "release"), "release")
			internalEvent := waitWorkEvent(t, p, message.EventID, "superseded")
			require.Empty(t, internalEvent.SentAt)
			boundary.driver.mu.Lock()
			inputs := len(boundary.driver.inputs[binding.Namespace+"/"+binding.Name])
			boundary.driver.mu.Unlock()
			require.Equal(t, 1, inputs, "the event must be fenced before native acceptance")
			p.stop(t, false)
			advanceClock(time.Minute)
			p = f.start(clock)
			require.Empty(t, scheduledTaskHistory(t, p, operatorFile, future.ID))
			require.Equal(t, []core.Message{message}, scheduledTaskHistory(t, p, operatorFile, past.ID))
		})
	}
}

func TestScheduledTaskNativeReceiptSurvivesTopicCompletion(t *testing.T) {
	t.Parallel()
	boundary := newRouteBoundary(t)
	f, operatorFile := eventFixture(t, boundary.url)
	p := f.start()
	_, token := workBoundaryTopic(t, f, p)
	p.stop(t, false)

	stage := "internal_event_before_settlement"
	gate := t.TempDir()
	p = f.start("SUPERAGENT_TEST_GATE_STAGE="+stage, "SUPERAGENT_TEST_GATE_DIR="+gate)
	t.Cleanup(func() { write(t, filepath.Join(gate, "release"), "release") })
	task := roleWork[core.ScheduledTask](t, p, token, "create_scheduled_task", scheduledTaskInput("accepted-before-completion", scheduledTaskPast))
	message := waitScheduledTaskHistory(t, p, operatorFile, task.ID, 1)[0]
	waitWorkGate(t, gate, stage)
	require.Equal(t, "dispatching", readRecord[workEventRecord](t, p, "internal-events/"+message.EventID).Status)

	roleWork[core.Topic](t, p, token, "topic_complete", acceptance("complete-topic", 1, "notes/verified.txt"))
	waitWorkEvent(t, p, message.EventID, "superseded")
	write(t, filepath.Join(gate, "release"), "release")
	require.NotEmpty(t, waitWorkEvent(t, p, message.EventID, "sent").SentAt)
}
