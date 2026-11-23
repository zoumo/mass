package core_test

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/superagent/pkg/core"
)

func TestScheduledTaskSystemRoutingFollowsActiveSubscriptions(t *testing.T) {
	boundary := newRouteBoundary(t)
	f, tokenFile := eventFixture(t, boundary.url)
	credential, err := os.ReadFile(tokenFile)
	require.NoError(t, err)
	operator := strings.TrimSpace(string(credential))
	p := f.start()
	router := waitBinding(t, p, "router", "", "ready")

	const tasks = "/v1/core/scheduled-tasks"
	status, raw := request(t, http.MethodPost, p.endpoint, tasks, operator, core.CreateScheduledTaskRequest{
		OperationID: "system-followup",
		Schedule: core.ScheduledTaskSchedule{
			Type: "once", RunAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		},
		Content: []events.ContentBlock{events.Text("Check the job status.")},
	})
	require.Contains(t, []int{http.StatusOK, http.StatusCreated}, status, string(raw))
	var task core.ScheduledTask
	require.NoError(t, json.Unmarshal(raw, &task))
	require.Nil(t, task.Owner)
	trigger := func(operationID string) core.Receipt {
		t.Helper()
		triggerStatus, triggerBody := request(t, http.MethodPost, p.endpoint, tasks+"/"+task.ID+"/trigger", operator, map[string]string{"operationId": operationID})
		require.Equal(t, http.StatusOK, triggerStatus, string(triggerBody))
		var receipt core.Receipt
		require.NoError(t, json.Unmarshal(triggerBody, &receipt))
		return receipt
	}

	first := trigger("first")
	require.Equal(t, first.EventID, waitDelivered(t, p, first.MessageID, "router", ""))
	route := decide(t, p, roleToken(t, f, router), newRoute("schedule-system-task", []string{first.MessageID}, f.dir))
	topicID := route.Targets[0].TopicID
	topic := waitBinding(t, p, "topic", topicID, "ready")
	waitDelivered(t, p, first.MessageID, "topic", topicID)

	second := trigger("second")
	require.Equal(t, first.ThreadID, second.ThreadID)
	require.Equal(t, second.EventID, waitDelivered(t, p, second.MessageID, "topic", topicID))
	require.NotEqual(t, first.MessageID, second.MessageID)

	completed := roleWork[core.Topic](t, p, roleToken(t, f, topic), "topic_complete", core.CompleteRequest{
		OperationID: "complete-subscribed-topic", ExpectedRevision: 1,
		Reason: "Completed", Evidence: []string{second.MessageID},
	})
	require.Equal(t, "completed", completed.Status)
	third := trigger("third")
	require.Equal(t, first.ThreadID, third.ThreadID)
	require.Equal(t, third.EventID, waitDelivered(t, p, third.MessageID, "router", ""))
	status, raw = request(t, http.MethodGet, p.endpoint, tasks+"/"+task.ID, operator, nil)
	require.Equal(t, http.StatusOK, status, string(raw))
	var latest core.ScheduledTask
	require.NoError(t, json.Unmarshal(raw, &latest))
	require.Equal(t, "active", latest.Status)
	require.Equal(t, task.NextRunAt, latest.NextRunAt)
}
