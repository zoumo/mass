package core_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/superagent/pkg/core"
)

func TestScheduledTaskOperatorHTTPAndCLI(t *testing.T) {
	f := fixtureAt(t)
	operatorFile := filepath.Join(f.dir, "operator.token")
	token := strings.Repeat("O", 48)
	write(t, operatorFile, token)
	config, err := os.ReadFile(f.config)
	require.NoError(t, err)
	write(t, f.config, string(config)+"operatorTokenFile: "+operatorFile+"\n")
	p := f.start()
	const tasks = "/v1/core/scheduled-tasks"
	const messages = "/v1/core/trigger-messages"
	sourceFilter := "source=" + url.QueryEscape("core:scheduled-task")
	call := func(method, path string, body any, want int) []byte {
		t.Helper()
		status, data := request(t, method, p.endpoint, path, token, body)
		require.Equal(t, want, status, string(data))
		return data
	}
	var task struct {
		ID        string          `json:"id"`
		Owner     json.RawMessage `json:"owner"`
		Status    string          `json:"status"`
		Revision  int             `json:"revision"`
		NextRunAt string          `json:"nextRunAt"`
	}
	create := json.RawMessage(`{"operationId":"create-independent","schedule":{"type":"interval","every":"24h"},"content":[{"type":"text","text":"Check the release."}]}`)
	for _, path := range []string{tasks, messages + "?" + sourceFilter, messages + "?scheduledTaskId=example"} {
		status, data := request(t, http.MethodGet, p.endpoint, path, f.token, nil)
		require.Equal(t, http.StatusUnauthorized, status, string(data))
	}
	status, data := request(t, http.MethodGet, p.endpoint, messages, "", nil)
	require.Equal(t, http.StatusOK, status, string(data))
	call(http.MethodGet, messages+"?view=router&"+sourceFilter, nil, http.StatusBadRequest)
	call(http.MethodGet, tasks+"?ownerType=topic", nil, http.StatusBadRequest)
	call(http.MethodGet, tasks+"/missing/trigger-messages", nil, http.StatusNotFound)
	require.NoError(t, json.Unmarshal(call(http.MethodPost, tasks, create, http.StatusOK), &task))
	require.Empty(t, task.Owner)
	require.Equal(t, "active", task.Status)
	require.Equal(t, 1, task.Revision)
	require.NotEmpty(t, task.NextRunAt)
	taskPath := tasks + "/" + task.ID
	call(http.MethodHead, tasks, nil, http.StatusOK)
	call(http.MethodHead, taskPath, nil, http.StatusOK)
	call(http.MethodHead, taskPath+"/trigger-messages", nil, http.StatusOK)
	require.JSONEq(t, `{"items":[]}`, string(call(http.MethodGet, tasks+"?ownerType=topic&ownerId=missing", nil, http.StatusOK)))

	trigger := func(operationID string) core.Receipt {
		t.Helper()
		var receipt core.Receipt
		body := map[string]string{"operationId": operationID}
		require.NoError(t, json.Unmarshal(call(http.MethodPost, taskPath+"/trigger", body, http.StatusOK), &receipt))
		return receipt
	}
	first := trigger("first")
	require.NotEmpty(t, first.MessageID)
	require.NotEmpty(t, first.EventID)
	require.False(t, first.Duplicate)
	call(http.MethodGet, "/v1/core/internal-events/"+first.EventID, nil, http.StatusOK)
	var afterTrigger map[string]any
	require.NoError(t, json.Unmarshal(call(http.MethodGet, taskPath, nil, http.StatusOK), &afterTrigger))
	require.Equal(t, task.NextRunAt, afterTrigger["nextRunAt"])

	call(http.MethodPatch, taskPath, json.RawMessage(`{"operationId":"pause","expectedRevision":1,"status":"paused"}`), http.StatusOK)
	call(http.MethodPost, taskPath+"/trigger", map[string]string{"operationId": "new-while-paused"}, http.StatusConflict)
	call(http.MethodPatch, taskPath, json.RawMessage(`{"operationId":"stale","expectedRevision":1,"status":"active"}`), http.StatusConflict)
	call(http.MethodPatch, taskPath, json.RawMessage(`{"operationId":"resume","expectedRevision":2,"status":"active","content":[{"type":"text","text":"Check again."}]}`), http.StatusOK)
	replay := trigger("first")
	require.True(t, replay.Duplicate)
	require.Equal(t, first.MessageID, replay.MessageID)
	require.Equal(t, first.EventID, replay.EventID)

	invokeCLI := func(args ...string) []byte {
		t.Helper()
		args = append([]string{"scheduled-tasks", "--endpoint", p.endpoint, "--operator-token-file", operatorFile}, args...)
		output, err := exec.CommandContext(t.Context(), build(t), args...).CombinedOutput()
		require.NoError(t, err, string(output))
		return output
	}
	var cliTrigger core.Receipt
	require.NoError(t, json.Unmarshal(invokeCLI("trigger", task.ID, "--operation-id", "from-cli"), &cliTrigger))
	require.Equal(t, first.ThreadID, cliTrigger.ThreadID)
	require.NotEqual(t, first.MessageID, cliTrigger.MessageID)
	require.Contains(t, string(invokeCLI("get", task.ID)), task.ID)
	require.Contains(t, string(invokeCLI("list")), task.ID)

	nested := taskPath + "/trigger-messages"
	global := messages + "?scheduledTaskId=" + url.QueryEscape(task.ID)
	page := call(http.MethodGet, nested+"?limit=1", nil, http.StatusOK)
	require.JSONEq(t, string(page), string(call(http.MethodGet, global+"&limit=1", nil, http.StatusOK)))
	var history core.Page
	require.NoError(t, json.Unmarshal(page, &history))
	require.Len(t, history.Items, 1)
	require.Contains(t, string(history.Items[0]), "Check the release.")
	require.Contains(t, string(history.Items[0]), `"scheduledTaskId":"`+task.ID+`"`)
	require.NotEmpty(t, history.NextCursor)
	next := call(http.MethodGet, global+"&cursor="+url.QueryEscape(history.NextCursor), nil, http.StatusOK)
	require.Contains(t, string(next), "Check again.")
	require.Contains(t, string(invokeCLI("trigger-messages", task.ID)), "Check again.")
	all := call(http.MethodGet, nested, nil, http.StatusOK)
	require.JSONEq(t, string(all), string(call(http.MethodGet, global+"&"+sourceFilter, nil, http.StatusOK)))
	require.JSONEq(t, string(all), string(call(http.MethodGet, messages+"?"+sourceFilter, nil, http.StatusOK)))

	call(http.MethodDelete, taskPath+"?expectedRevision="+strconv.Itoa(task.Revision+2), nil, http.StatusOK)
	call(http.MethodGet, taskPath, nil, http.StatusNotFound)
	require.JSONEq(t, string(all), string(call(http.MethodGet, nested, nil, http.StatusOK)))
	require.Empty(t, checkStandaloneStackStatus(t, p, "ready", "disabled").ScheduledTaskStates)
}
