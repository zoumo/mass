package core

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
{"metric":{"service_name":"massd","service_instance_id":"new","private_label":"secret"},"values":[[%d,"0.5"],[%d,"NaN"],[%d,"0.75"]]},
{"metric":{"service_name":"massd","service_instance_id":"old"},"values":[[%d,"0"]]},
{"metric":{"service_name":"other","service_instance_id":"unrelated"},"values":[[%d,"10"]]}
]}}`, start+15, start+30, start+45, start, start)
		}
	}))
	t.Cleanup(upstream.Close)
	credentials := filepath.Join(t.TempDir(), "login.txt")
	require.NoError(t, os.WriteFile(credentials, []byte("observer\nfixture\n"), 0o600))
	reader, err := newObserverTraceReader(&ObserverTraceConfig{
		Endpoint: upstream.URL, Organization: "review", CredentialFile: credentials,
	})
	require.NoError(t, err)
	t.Cleanup(reader.client.CloseIdleConnections)

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "observer.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.ExecContext(t.Context(), schema)
	require.NoError(t, err)
	server := &Server{observerTraces: reader, store: &Store{db: db}}
	response := httptest.NewRecorder()
	server.query(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, observerPerformancePath+"?window=15m", http.NoBody))
	require.Equal(t, http.StatusOK, response.Code)
	var result observerPerformance
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	require.NotContains(t, response.Body.String(), "secret")
	require.NotContains(t, response.Body.String(), "unrelated")
	require.EqualValues(t, 15, result.StepSeconds)
	require.Len(t, result.Metrics, 4)
	cpu := result.Metrics[0]
	require.Equal(t, "cpu", cpu.ID)
	require.False(t, cpu.Unavailable)
	require.Equal(t, []performanceSeries{
		{Service: "massd", Instance: "new", Points: [][2]float64{{float64(result.From + 15), 0.5}, {float64(result.From + 45), 0.75}}},
		{Service: "massd", Instance: "old", Points: [][2]float64{{float64(result.From), 0}}},
	}, cpu.Series)
	require.NotNil(t, result.Metrics[2].Series)
	require.Empty(t, result.Metrics[2].Series)
	require.False(t, result.Metrics[2].Unavailable)
	require.True(t, result.Metrics[3].Unavailable)
	_, err = reader.performance(t.Context(), "unbounded")
	require.ErrorContains(t, err, "invalid performance window")
	_, err = (*observerTraceReader)(nil).performance(t.Context(), "")
	require.ErrorContains(t, err, "Performance query source is not configured")
}
