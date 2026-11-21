package core

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const observerPerformancePath = "/v1/observer/performance"

type observerPerformance struct {
	From        int64               `json:"from"`
	Until       int64               `json:"until"`
	StepSeconds int64               `json:"stepSeconds"`
	Metrics     []performanceMetric `json:"metrics"`
}

type performanceMetric struct {
	ID          string              `json:"id"`
	Unavailable bool                `json:"unavailable,omitempty"`
	Series      []performanceSeries `json:"series"`
}

type performanceSeries struct {
	Service  string       `json:"service"`
	Instance string       `json:"instance"`
	Points   [][2]float64 `json:"points"`
}

func (r *observerTraceReader) performance(ctx context.Context, window string) (observerPerformance, error) {
	var result observerPerformance
	var seconds, step int64
	switch window {
	case "15m":
		seconds, step = 900, 15
	case "", "1h":
		seconds, step = 3600, 30
	case "6h":
		seconds, step = 21600, 180
	case "24h":
		seconds, step = 86400, 300
	default:
		return result, problem("invalid_argument", "invalid performance window")
	}
	if r == nil {
		return result, problem("failed_precondition", "Performance query source is not configured")
	}
	select {
	case r.metricsInFlight <- struct{}{}:
		defer func() { <-r.metricsInFlight }()
	default:
		return result, problem("unavailable", "Performance query source is busy")
	}
	until := time.Now().Unix() / step * step
	result = observerPerformance{From: until - seconds, Until: until, StepSeconds: step}
	const services = `{service_name=~"massd|superagent-core",service_instance_id!=""}`
	const byInstance = ` by (service_name, service_instance_id) `
	// Cover the default minute-long export period at short resolutions.
	lookback := "[" + strconv.FormatInt(max(90, step), 10) + "s]"
	gauge := func(metric string) string {
		return "max" + byInstance + "(last_over_time(" + metric + services + lookback + "))"
	}
	queries := []struct{ id, expression string }{
		{"cpu", "sum" + byInstance + "(rate(process_cpu_time" + services + "[5m]))"},
		{"rss", gauge("process_memory_usage")},
		{"goroutines", gauge("go_goroutine_count")},
		{"file_descriptors", gauge("process_unix_file_descriptor_count")},
	}
	result.Metrics = make([]performanceMetric, len(queries))
	var pending sync.WaitGroup
	for i, query := range queries {
		pending.Go(func() {
			series, ok := r.performanceRange(ctx, until-seconds, until, step, query.expression)
			result.Metrics[i] = performanceMetric{ID: query.id, Series: series, Unavailable: !ok}
		})
	}
	pending.Wait()
	return result, nil
}

func (r *observerTraceReader) performanceRange(ctx context.Context, from, until, step int64, query string) ([]performanceSeries, bool) {
	series := []performanceSeries{}
	params := url.Values{
		"query": {query}, "start": {strconv.FormatInt(from, 10)},
		"end": {strconv.FormatInt(until, 10)}, "step": {strconv.FormatInt(step, 10)},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.metricsURL+"?"+params.Encode(), nil)
	if err != nil {
		return series, false
	}
	if r.user != "" {
		request.SetBasicAuth(r.user, r.password)
	}
	response, err := r.client.Do(request)
	if err != nil {
		return series, false
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return series, false
	}

	const maxBytes = 2 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	var page struct {
		Status string
		Data   struct {
			ResultType string
			Result     []struct {
				Metric struct {
					Service  string `json:"service_name"`
					Instance string `json:"service_instance_id"`
				}
				Values [][2]json.RawMessage
			}
		}
	}
	if err != nil || len(body) > maxBytes || json.Unmarshal(body, &page) != nil ||
		page.Status != "success" || page.Data.ResultType != "matrix" || page.Data.Result == nil {
		return series, false
	}
	for _, result := range page.Data.Result {
		service, instance := result.Metric.Service, result.Metric.Instance
		if (service != "superagent-core" && service != "massd") || instance == "" || len(instance) > 128 {
			continue
		}
		previous := from - step
		line := performanceSeries{Service: service, Instance: instance}
		for _, sample := range result.Values {
			var at int64
			var raw string
			if json.Unmarshal(sample[0], &at) != nil || json.Unmarshal(sample[1], &raw) != nil ||
				at < from || at > until || at <= previous || (at-from)%step != 0 {
				continue
			}
			value, err := strconv.ParseFloat(raw, 64)
			if err != nil || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
				continue
			}
			line.Points = append(line.Points, [2]float64{float64(at), value})
			previous = at
		}
		if len(line.Points) > 0 {
			series = append(series, line)
		}
	}
	slices.SortFunc(series, func(a, b performanceSeries) int {
		if a.Service != b.Service {
			return strings.Compare(a.Service, b.Service)
		}
		return strings.Compare(a.Instance, b.Instance)
	})
	return series, true
}
