package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestObserverPerformanceProjectsRangeSamples(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/review/prometheus/api/v1/query_range" {
			t.Error("unexpected performance query route")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if user, password, ok := r.BasicAuth(); !ok || user != "observer" || password != "fixture" {
			t.Error("performance query did not use configured credentials")
		}
		params := r.URL.Query()
		start, _ := strconv.ParseInt(params.Get("start"), 10, 64)
		end, _ := strconv.ParseInt(params.Get("end"), 10, 64)
		if start <= 0 || end-start != 900 || params.Get("step") != "15" {
			t.Error("unexpected range for 15m")
		}
		query := params.Get("query")
		if !strings.Contains(query, `service_name=~"massd|superagent-core"`) ||
			!strings.Contains(query, "by (service_name, service_instance_id)") {
			t.Error("query lost its service or instance scope")
		}
		switch {
		case strings.Contains(query, "process_unix_file_descriptor_count"):
			w.WriteHeader(http.StatusServiceUnavailable)
		case strings.Contains(query, "go_goroutine_count"):
			_, _ = fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
		default:
			if strings.Contains(query, "process_cpu_time") && !strings.Contains(query, "rate(process_cpu_time") {
				t.Error("CPU requires a counter rate")
			}
			_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[
{"metric":{"service_name":"superagent-core","service_instance_id":"first"},"values":[[%d,"1"]]},
{"metric":{"service_name":"massd","service_instance_id":"old"},"values":[[%d,"0"]]},
{"metric":{"service_name":"massd","service_instance_id":"new","private_label":"secret"},"values":[[%d,"0.5"],[%d,"NaN"],[%d,"0.75"]]},
{"metric":{"service_name":"other","service_instance_id":"unrelated"},"values":[[%d,"10"]]}
]}}`, start, start, start+15, start+30, start+45, start)
		}
	}))
	t.Cleanup(upstream.Close)
	credentials := filepath.Join(t.TempDir(), "login.txt")
	require.NoError(t, os.WriteFile(credentials, []byte("observer\nfixture\n"), 0o600))
	reader, err := newObserverTraceReader(&OpenObserveConfig{
		Endpoint: upstream.URL, Organization: "review", CredentialFile: credentials,
	})
	require.NoError(t, err)
	t.Cleanup(reader.client.CloseIdleConnections)

	store, err := OpenStore(Config{DataDir: t.TempDir()}, TelemetryOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	server := &Server{observerTraces: reader, store: store}
	response := httptest.NewRecorder()
	api := server.newHTTPAPI()
	api.router.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, telemetryPerformancePath+"?window=15m", http.NoBody))
	require.Equal(t, http.StatusOK, response.Code)
	var result telemetryPerformance
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	require.NotContains(t, response.Body.String(), "secret")
	require.NotContains(t, response.Body.String(), "unrelated")
	require.EqualValues(t, 15, result.StepSeconds)
	require.Len(t, result.Metrics, 4)
	cpu := result.Metrics[0]
	require.Equal(t, "cpu", cpu.ID)
	require.False(t, cpu.Unavailable)
	require.Equal(t, []performanceSeries{
		{Dimensions: map[string]string{"service": "massd", "instance": "new"}, Points: [][2]float64{{float64(result.From + 15), 0.5}, {float64(result.From + 45), 0.75}}},
		{Dimensions: map[string]string{"service": "massd", "instance": "old"}, Points: [][2]float64{{float64(result.From), 0}}},
		{Dimensions: map[string]string{"service": "superagent-core", "instance": "first"}, Points: [][2]float64{{float64(result.From), 1}}},
	}, cpu.Series)
	require.NotNil(t, result.Metrics[2].Series)
	require.Empty(t, result.Metrics[2].Series)
	require.False(t, result.Metrics[2].Unavailable)
	require.True(t, result.Metrics[3].Unavailable)
	_, err = reader.performance(t.Context(), url.Values{"window": {"unbounded"}})
	require.ErrorContains(t, err, "invalid performance window")
	_, err = (*observerTraceReader)(nil).performance(t.Context(), nil)
	require.ErrorContains(t, err, "Performance query source is not configured")
}

func TestObserverPerformanceScopesOpenTelemetryQueries(t *testing.T) {
	store, err := OpenStore(Config{DataDir: t.TempDir()}, TelemetryOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	const total = `sum by (superagent_role) (rate(superagent_mcp_tool_duration_count{service_name="superagent-core",superagent_tool="update_topic"}[5m]))`
	const failed = `sum by (superagent_role) (rate(superagent_mcp_tool_duration_count{service_name="superagent-core",superagent_tool="update_topic",superagent_outcome!="ok"}[5m]))`
	cases := []struct {
		name, query, expression, id string
		dimensions                  map[string]string
	}{
		{"cpu by service", "metric=cpu&groupBy=service&service=massd&instance=first", `sum by (service_name) (rate(process_cpu_time{service_name="massd",service_instance_id="first"}[5m]))`, "cpu", map[string]string{"service": "massd"}},
		{"action rate", "metric=mcp_rate&groupBy=action&role=topic&tool=update_topic&outcome=ok", `sum by (superagent_action) (rate(superagent_mcp_tool_duration_count{service_name="superagent-core",superagent_role="topic",superagent_tool="update_topic",superagent_outcome="ok"}[5m]))`, "mcp_rate", map[string]string{"action": "edit"}},
		{"failure denominator", "metric=mcp_failure_rate&groupBy=role&tool=update_topic", "(" + failed + " or (0 * " + total + ")) / (" + total + " > 0)", "mcp_failure_rate", map[string]string{"role": "topic"}},
		{"histogram P95", "metric=mcp_p95&groupBy=outcome&action=edit", `histogram_quantile(0.95, sum by (le, superagent_outcome) (rate(superagent_mcp_tool_duration_bucket{service_name="superagent-core",superagent_action="edit"}[5m])))`, "mcp_p95", map[string]string{"outcome": "ok"}},
		{"total", "metric=mcp_rate&groupBy=all", `sum(rate(superagent_mcp_tool_duration_count{service_name="superagent-core"}[5m]))`, "mcp_rate", map[string]string{}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if actual := r.URL.Query().Get("query"); actual != test.expression {
					t.Errorf("query = %q, want %q", actual, test.expression)
				}
				start := r.URL.Query().Get("start")
				_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[
{"metric":{"service_name":"massd","service_instance_id":"first","superagent_role":"topic","superagent_action":"edit","superagent_outcome":"ok","trace_id":"private"},"values":[[%s,"0.5"]]}
]}}`, start)
			}))
			t.Cleanup(upstream.Close)
			reader, err := newObserverTraceReader(&OpenObserveConfig{Endpoint: upstream.URL})
			require.NoError(t, err)
			t.Cleanup(reader.client.CloseIdleConnections)
			server := &Server{observerTraces: reader, store: store}
			response := httptest.NewRecorder()
			server.newHTTPAPI().router.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, telemetryPerformancePath+"?"+test.query, http.NoBody))
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			var result telemetryPerformance
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
			require.Equal(t, []performanceMetric{{ID: test.id, Series: []performanceSeries{{
				Dimensions: test.dimensions, Points: [][2]float64{{float64(result.From), 0.5}},
			}}}}, result.Metrics)
		})
	}
	for _, invalid := range []string{
		"groupBy=outcome", "metric=arbitrary", "metric=cpu&metric=rss", "metric=mcp_rate&service=massd", "metric=mcp_rate&tool=%22%7D",
		"metric=mcp_failure_rate&groupBy=outcome", "metric=mcp_failure_rate&outcome=ok", "query=arbitrary", "instance=", "service=other",
	} {
		t.Run(invalid, func(t *testing.T) {
			server := &Server{store: store}
			response := httptest.NewRecorder()
			server.newHTTPAPI().router.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, telemetryPerformancePath+"?"+invalid, http.NoBody))
			require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
		})
	}
}
