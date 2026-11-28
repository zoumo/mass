package core_test

import (
	"bytes"
	"database/sql"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/massd/pkg/runtime"
	"github.com/zoumo/mass/superagent/pkg/core"
)

func TestCurrentSessionChatBoundary(t *testing.T) {
	boundary := newRouteBoundary(t)
	boundary.driver.mu.Lock()
	boundary.driver.answer = func(ref string, answer any) (runtime.Acceptance, error) {
		if value, ok := answer.(map[string]any); ref == "fixture-permission" && ok && value["optionId"] == "allow-once" {
			return runtime.Acceptance{Sent: runtime.Bool(true), NativeAccepted: runtime.Bool(true)}, nil
		}
		return runtime.Acceptance{}, errors.New("unexpected interaction answer")
	}
	boundary.driver.mu.Unlock()
	f, _ := eventFixture(t, boundary.url)
	p := f.start()
	router := waitBinding(t, p, "router", "", "ready")
	msg := submit(t, p, f, conversationInput("session-chat", "session-chat"))
	route := decide(t, p, roleToken(t, f, router), newRoute("session-chat", []string{msg.MessageID}, f.dir))
	topic := waitBinding(t, p, "topic", route.Targets[0].TopicID, "ready")
	topicToken := roleToken(t, f, topic)

	create := issueInput(f.dir, "session-chat")
	create.Start = true
	work := roleWork[core.WorkReceipt](t, p, topicToken, "create_issue", create)
	worker := waitBinding(t, p, "issue", work.SessionBinding.ID, "ready")
	waitWorkEvent(t, p, worker.StartEventID, "sent")

	path := func(binding core.SessionBinding) string {
		return "/apis/core/v1/session-bindings/" + binding.ID + "/chat"
	}
	answer := map[string]any{"action": "answer", "interaction": "fixture-permission", "answer": map[string]string{"optionId": "allow-once"}}
	for _, binding := range []core.SessionBinding{topic, worker} {
		status, data := request(t, http.MethodGet, p.endpoint, path(binding)+"/history", "", nil)
		require.Equal(t, http.StatusOK, status, string(data))
		status, data = request(t, http.MethodPost, p.endpoint, path(binding), "", answer)
		require.Equal(t, http.StatusOK, status, string(data))
		require.Contains(t, string(data), `"interaction":"fixture-permission"`)
	}
	status, data := request(t, http.MethodGet, p.endpoint, path(router)+"/history", "", nil)
	errorCode(t, status, data, http.StatusNotFound, "not_found")
	status, data = request(t, http.MethodPost, p.endpoint, path(topic), "", map[string]string{"action": "unknown"})
	errorCode(t, status, data, http.StatusBadRequest, "invalid_argument")

	for _, check := range []struct {
		origin, contentType string
		status              int
	}{
		{"https://foreign.example.test", "application/json", http.StatusForbidden},
		{"", "text/plain", http.StatusBadRequest},
	} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, p.endpoint+path(topic), bytes.NewBufferString(`{"action":"prompt","text":"ignore this"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", check.contentType)
		if check.origin != "" {
			req.Header.Set("Origin", check.origin)
		}
		resp, err := (&http.Client{Timeout: time.Second}).Do(req)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		require.Equal(t, check.status, resp.StatusCode)
	}

	db, err := sql.Open("sqlite", filepath.Join(f.data, "core.sqlite")+"?_pragma=busy_timeout(1000)")
	require.NoError(t, err)
	defer db.Close()
	_, err = db.ExecContext(t.Context(), `UPDATE session_bindings SET body=json_set(body,'$.sessionUid','other-uid') WHERE id=?`, topic.ID)
	require.NoError(t, err)
	status, data = request(t, http.MethodPost, p.endpoint, path(topic), "", answer)
	errorCode(t, status, data, http.StatusConflict, "conflict")

	issue := readRecord[core.Issue](t, p, "issues/"+worker.OwnerID)
	roleWork[core.WorkReceipt](t, p, topicToken, "issue_cancel", issueLifecycle("retire-session-chat", issue))
	status, data = request(t, http.MethodGet, p.endpoint, path(worker)+"/history", "", nil)
	errorCode(t, status, data, http.StatusNotFound, "not_found")
	status, data = request(t, http.MethodPost, p.endpoint, path(worker), "", answer)
	errorCode(t, status, data, http.StatusNotFound, "not_found")
}
