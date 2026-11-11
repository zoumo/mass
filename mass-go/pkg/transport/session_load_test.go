package transport_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

// This public Service substitute proves HTTP mapping, not native recovery.
type loadAPI struct {
	transport.API
	input       resource.SessionLoad
	accepted    resource.SessionLoadAccepted
	err         error
	calls       int
	resolutions int
	ref         resource.SessionRef
	loadID      string
	status      resource.HistoryLoadStatus
}

func (a *loadAPI) LoadSession(_ context.Context, input resource.SessionLoad) (resource.SessionLoadAccepted, error) {
	a.input = input
	a.calls++
	return a.accepted, a.err
}

func (a *loadAPI) ResolveSession(namespace, name string) (resource.SessionRef, error) {
	a.resolutions++
	return resource.SessionRef{SessionID: "pinned/ID%2F"}, a.err
}

func (a *loadAPI) CancelHistoryLoad(_ context.Context, ref resource.SessionRef, loadID string) (resource.HistoryLoadStatus, error) {
	a.calls++
	a.ref, a.loadID = ref, loadID
	return a.status, a.err
}

func TestLoadReturnsAcceptedReceiptWithoutInferringCompletion(t *testing.T) {
	a := &loadAPI{accepted: resource.SessionLoadAccepted{
		LoadID:  "original-load",
		Session: resource.Session{Status: resource.SessionStatus{SessionID: "a/b%2F", State: "opening", HistoryLoad: &resource.HistoryLoadStatus{LoadID: "original-load", Phase: "accepted"}}},
	}}
	s, err := transport.New(a, transport.Options{Token: "test-token"})
	require.NoError(t, err)
	t.Cleanup(s.Shutdown)
	w := request(s, "POST", "/apis/v1/sessions/load", `{"metadata":{"namespace":"demo","name":"imported"},"runtimeRef":"agent","cwd":"/agent/does-not-exist-locally","sessionId":"a/b%2F"}`)
	require.Equal(t, 202, w.Code, w.Body.String())
	require.Equal(t, "/apis/v1/sessions/a%2Fb%252F", w.Header().Get("Location"))
	require.Equal(t, resource.SessionLoad{Metadata: resource.Metadata{Namespace: "demo", Name: "imported"}, RuntimeRef: "agent", Cwd: "/agent/does-not-exist-locally", SessionID: "a/b%2F"}, a.input)
	var got resource.SessionLoadAccepted
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Equal(t, a.accepted, got)
	for _, key := range []string{"nativeVerified", "nativeResumed", "nativeCompleted", "deliveryCompleted", "cleanupCompleted"} {
		require.Contains(t, w.Body.String(), `"`+key+`":false`)
	}
}

func TestLoadPreservesEmptyFeaturesObject(t *testing.T) {
	a := &loadAPI{accepted: resource.SessionLoadAccepted{
		LoadID:  "original-load",
		Session: resource.Session{Status: resource.SessionStatus{SessionID: "native", State: "opening"}},
	}}
	s, err := transport.New(a, transport.Options{Token: "test-token"})
	require.NoError(t, err)
	t.Cleanup(s.Shutdown)

	w := request(s, "POST", "/apis/v1/sessions/load", `{"metadata":{"namespace":"demo","name":"imported"},"runtimeRef":"agent","cwd":"/agent/work","sessionId":"native","features":{}}`)
	require.Equal(t, 202, w.Code, w.Body.String())
	require.NotNil(t, a.input.Features)
	require.Nil(t, a.input.Features.AgentQuestions)
}

func TestLoadRejectsInvalidWireInputBeforeService(t *testing.T) {
	valid := `{"metadata":{"namespace":"demo","name":"imported"},"runtimeRef":"agent","cwd":"/agent/work","sessionId":"native"}`
	cases := []string{"", "null", "[]", `{}`, valid + valid}
	for _, field := range []string{"spec", "status", "systemPrompt", "additionalDirs", "loadId", "unknown"} {
		cases = append(cases, strings.TrimSuffix(valid, "}")+fmt.Sprintf(`,%q:null}`, field))
	}
	for _, field := range []string{"runtimeRef", "cwd", "sessionId"} {
		var v map[string]any
		require.NoError(t, json.Unmarshal([]byte(valid), &v))
		for _, value := range []any{nil, ""} {
			v[field] = value
			b, err := json.Marshal(v)
			require.NoError(t, err)
			cases = append(cases, string(b))
		}
		delete(v, field)
		b, err := json.Marshal(v)
		require.NoError(t, err)
		cases = append(cases, string(b))
	}
	cases = append(cases,
		strings.Replace(valid, `"name":"imported"`, `"name":"imported","name":"other"`, 1),
		strings.Replace(valid, `"namespace":"demo"`, `"namespace":"demo","uid":"system"`, 1),
		strings.Replace(valid, `"metadata":{"namespace":"demo","name":"imported"}`, `"metadata":null`, 1),
		strings.Replace(valid, `"sessionId":"native"`, `"sessionId":"native","sessionId":"other"`, 1),
		strings.Replace(valid, `"sessionId":"native"`, `"sessionId":".."`, 1),
		strings.Replace(valid, `"sessionId":"native"`, `"sessionId":"."`, 1),
		strings.Replace(valid, `"sessionId":"native"`, "\"sessionId\":\"\xff\"", 1),
	)
	a := &loadAPI{}
	s, err := transport.New(a, transport.Options{Token: "test-token"})
	require.NoError(t, err)
	t.Cleanup(s.Shutdown)
	for _, body := range cases {
		w := request(s, "POST", "/apis/v1/sessions/load", body)
		require.Equal(t, 400, w.Code, body+": "+w.Body.String())
	}
	w := request(s, "POST", "/apis/v1/sessions/load?wait=true", valid)
	require.Equal(t, 400, w.Code, w.Body.String())
	require.Zero(t, a.calls)
}

func TestCancelLoadPinsTargetAndPreservesOriginalLoadID(t *testing.T) {
	for _, path := range []string{"/apis/v1/sessions/pinned%2FID%252F/history-load/cancel", "/apis/v1/namespaces/demo/sessions/task/history-load/cancel"} {
		t.Run(path, func(t *testing.T) {
			a := &loadAPI{status: resource.HistoryLoadStatus{LoadID: " original/% load ", Phase: "canceled", Error: &runtime.OperationError{Code: "outcome_unknown", Reason: "cleanup_pending", Message: "Cleanup is not confirmed"}}}
			s, err := transport.New(a, transport.Options{Token: "test-token"})
			require.NoError(t, err)
			t.Cleanup(s.Shutdown)
			w := request(s, "POST", path, `{"loadId":" original/% load "}`)
			require.Equal(t, 200, w.Code, w.Body.String())
			require.Equal(t, resource.SessionRef{SessionID: "pinned/ID%2F"}, a.ref)
			require.Equal(t, " original/% load ", a.loadID)
			require.Equal(t, 1, a.calls)
			if strings.Contains(path, "/namespaces/") {
				require.Equal(t, 1, a.resolutions)
			} else {
				require.Zero(t, a.resolutions)
			}
			var got resource.HistoryLoadStatus
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
			require.Equal(t, a.status, got)
			require.Contains(t, w.Body.String(), `"cleanupCompleted":false`)
			require.NotContains(t, w.Body.String(), `lastCommittedEventId`)
			for _, body := range []string{"", "null", "{}", `{"loadId":null}`, `{"loadId":""}`, `{"loadId":"a","loadId":"b"}`, `{"loadId":"a","sessionId":"other"}`} {
				w = request(s, "POST", path, body)
				require.Equal(t, 400, w.Code, body+": "+w.Body.String())
			}
			w = request(s, "POST", path+"?loadId=other", `{"loadId":"a"}`)
			require.Equal(t, 400, w.Code)
			require.Equal(t, 1, a.calls)
		})
	}
}

func TestLoadAndCancelUseNormalOperationErrors(t *testing.T) {
	for code, status := range map[string]int{"conflict": 409, "unsupported": 422, "resource_exhausted": 429, "outcome_unknown": 500} {
		t.Run(code, func(t *testing.T) {
			a := &loadAPI{err: &runtime.OperationError{Code: code, Reason: "load_not_confirmed", Message: "Inspect status"}}
			s, err := transport.New(a, transport.Options{Token: "test-token"})
			require.NoError(t, err)
			t.Cleanup(s.Shutdown)
			for path, body := range map[string]string{
				"/apis/v1/sessions/load":                       `{"metadata":{"namespace":"demo","name":"imported"},"runtimeRef":"agent","cwd":"/remote","sessionId":"native"}`,
				"/apis/v1/sessions/native/history-load/cancel": `{"loadId":"original"}`,
			} {
				w := request(s, "POST", path, body)
				require.Equal(t, status, w.Code, w.Body.String())
				require.Empty(t, w.Header().Get("Location"))
				require.JSONEq(t, fmt.Sprintf(`{"code":%q,"reason":"load_not_confirmed","message":"Inspect status"}`, code), w.Body.String())
			}
		})
	}
}

func TestLoadOpenAPIPublishesAcceptanceAndRequiredEvidence(t *testing.T) {
	b, err := transport.OpenAPI()
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(b, &doc))
	paths := doc["paths"].(map[string]any)
	load := paths["/apis/v1/sessions/load"].(map[string]any)["post"].(map[string]any)
	require.Equal(t, "loadSession", load["operationId"])
	responses := load["responses"].(map[string]any)
	require.Contains(t, responses, "202")
	require.NotContains(t, responses, "200")
	accepted := responses["202"].(map[string]any)
	require.Contains(t, accepted["headers"], "Location")
	for path, id := range map[string]string{
		"/apis/v1/sessions/{sessionId}/history-load/cancel":                       "cancelHistoryLoadByID",
		"/apis/v1/namespaces/{namespace}/sessions/{nameOrID}/history-load/cancel": "cancelHistoryLoadByNameOrID",
	} {
		op := paths[path].(map[string]any)["post"].(map[string]any)
		require.Equal(t, id, op["operationId"])
		require.Contains(t, op["responses"], "200")
	}
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	input := schemas["SessionLoad"].(map[string]any)
	require.Equal(t, false, input["additionalProperties"])
	require.ElementsMatch(t, []any{"metadata", "runtimeRef", "cwd", "sessionId"}, input["required"])
	require.Len(t, input["properties"], 7)
	require.Contains(t, input["properties"], "config")
	require.Contains(t, input["properties"], "features")
	require.Contains(t, input["properties"], "mcpServers")
	status := schemas["HistoryLoadStatus"].(map[string]any)
	required := status["required"]
	for _, key := range []string{"loadId", "phase", "nativeVerified", "nativeResumed", "nativeCompleted", "deliveryCompleted", "cleanupCompleted"} {
		require.Contains(t, required, key)
	}
	require.NotContains(t, required, "error")
	require.NotContains(t, required, "lastCommittedEventId")
	properties := status["properties"].(map[string]any)
	require.Equal(t, "#/components/schemas/OperationError", properties["error"].(map[string]any)["$ref"])
	sessionStatus := schemas["SessionStatus"].(map[string]any)
	require.NotContains(t, sessionStatus["required"], "historyLoad")
	require.Contains(t, sessionStatus["properties"], "effectiveFeatures")
	features := schemas["SessionFeatures"].(map[string]any)
	require.Equal(t, true, features["properties"].(map[string]any)["agentQuestions"].(map[string]any)["default"])
	mcpServer := schemas["MCPServer"].(map[string]any)
	require.InDelta(t, 1, mcpServer["properties"].(map[string]any)["name"].(map[string]any)["minLength"], 0)
}
