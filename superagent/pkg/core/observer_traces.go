package core

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"go.opentelemetry.io/otel/trace"
)

const (
	observerTraceLimit  = 1000
	observerTraceWindow = 7 * 24 * time.Hour
)

// OpenObserveConfig configures trace and metric queries against OpenObserve.
type OpenObserveConfig struct {
	Endpoint       string `mapstructure:"endpoint"`
	Organization   string `mapstructure:"organization"`
	Stream         string `mapstructure:"stream"`
	CredentialFile string `mapstructure:"credentialFile"`
}

var traceStreamName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z_0-9]{0,127}$`)

type observerTraceReader struct {
	url, metricsURL, stream, user, password string
	client                                  *http.Client
	inFlight, metricsInFlight               chan struct{}
}

func newObserverTraceReader(config *OpenObserveConfig) (*observerTraceReader, error) {
	if config == nil {
		return nil, nil
	}
	u, err := url.Parse(config.Endpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" ||
		u.User != nil || (u.Path != "" && u.Path != "/") || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" {
		return nil, problem("invalid_argument", "openObserve endpoint must be an HTTP or HTTPS origin")
	}
	org, stream := config.Organization, config.Stream
	if org == "" {
		org = "default"
	}
	if stream == "" {
		stream = "default"
	}
	if !traceStreamName.MatchString(org) || !traceStreamName.MatchString(stream) {
		return nil, problem("invalid_argument", "invalid openObserve organization or stream")
	}
	base := u.Scheme + "://" + u.Host + "/api/" + org
	reader := &observerTraceReader{
		url: base + "/_search?type=traces", stream: stream,
		metricsURL: base + "/prometheus/api/v1/query_range",
		client: &http.Client{
			Timeout:       12 * time.Second,
			Transport:     &http.Transport{IdleConnTimeout: 30 * time.Second},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		inFlight: make(chan struct{}, 4), metricsInFlight: make(chan struct{}, 2),
	}
	if config.CredentialFile != "" {
		info, err := os.Lstat(config.CredentialFile)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 4096 {
			return nil, problem("invalid_argument", "openObserve credentialFile must be a private regular file")
		}
		data, err := os.ReadFile(config.CredentialFile)
		lines := strings.Split(strings.TrimSuffix(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n"), "\n")
		if err != nil || len(lines) != 2 || lines[0] == "" || strings.Contains(lines[0], ":") || lines[1] == "" {
			return nil, problem("invalid_argument", "openObserve credentialFile requires a username and password on separate lines")
		}
		reader.user, reader.password = lines[0], lines[1]
	}
	return reader, nil
}

type telemetrySpan struct {
	ID         string  `json:"id"`
	ParentID   string  `json:"parentId,omitempty"`
	Name       string  `json:"name"`
	Service    string  `json:"service"`
	OffsetUS   float64 `json:"offsetUs"`
	DurationUS int64   `json:"durationUs"`
	Status     int     `json:"status"`
	Role       string  `json:"role,omitempty"`
	Tool       string  `json:"tool,omitempty"`
	Action     string  `json:"action,omitempty"`
	Outcome    string  `json:"outcome,omitempty"`
	CallerID   string  `json:"callerId,omitempty"`
	EventID    string  `json:"eventId,omitempty"`
}

type telemetryTrace struct {
	TraceID   string          `json:"traceId"`
	StartedAt time.Time       `json:"startedAt"`
	Spans     []telemetrySpan `json:"spans"`
	Truncated bool            `json:"truncated"`
}

type traceSearchHit struct {
	TraceID    string `json:"trace_id"`
	ID         string `json:"span_id"`
	ParentID   string `json:"reference_parent_span_id"`
	Name       string `json:"operation_name"`
	Service    string `json:"service_name"`
	StartNS    int64  `json:"start_time"`
	DurationUS int64  `json:"duration"`
	Status     int    `json:"status_code"`
	Role       string `json:"superagent_role"`
	Tool       string `json:"superagent_tool"`
	Action     string `json:"superagent_action"`
	Outcome    string `json:"superagent_outcome"`
	CallerID   string `json:"superagent_caller_id"`
	EventID    string `json:"superagent_event_id"`
}

func (r *observerTraceReader) read(ctx context.Context, id string, query url.Values) (telemetryTrace, error) {
	result := telemetryTrace{TraceID: id, Spans: []telemetrySpan{}}
	if _, err := trace.TraceIDFromHex(id); err != nil {
		return result, problem("invalid_argument", "invalid Trace ID")
	}
	if r == nil {
		return result, problem("failed_precondition", "Trace query source is not configured")
	}
	end := time.Now()
	start := end.Add(-observerTraceWindow)
	if raw := query.Get("at"); raw != "" {
		anchor, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil || anchor.After(end.Add(time.Minute)) || anchor.Year() < 1970 {
			return result, problem("invalid_argument", "invalid Trace time anchor")
		}
		start = anchor.Add(-observerTraceWindow)
		end = end.Add(time.Minute)
		if latest := anchor.Add(observerTraceWindow); latest.Before(end) {
			end = latest
		}
	}
	select {
	case r.inFlight <- struct{}{}:
		defer func() { <-r.inFlight }()
	default:
		return result, problem("unavailable", "Trace query source is busy")
	}
	payload, err := json.Marshal(map[string]any{"query": map[string]any{
		"sql":        `SELECT * FROM "` + r.stream + `" WHERE trace_id = '` + id + `' ORDER BY start_time,span_id`,
		"start_time": start.UnixMicro(), "end_time": end.UnixMicro(), "from": 0, "size": observerTraceLimit + 1,
	}, "search_type": "ui", "timeout": 10})
	if err != nil {
		return result, problem("unavailable", "cannot encode Trace query")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(payload))
	if err != nil {
		return result, problem("unavailable", "cannot create Trace query")
	}
	request.Header.Set("Content-Type", "application/json")
	if r.user != "" {
		request.SetBasicAuth(r.user, r.password)
	}
	response, err := r.client.Do(request)
	if err != nil {
		return result, problem("unavailable", "cannot connect to Trace query source")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return result, problem("unavailable", "Trace query source rejected the search")
	}
	const maxBytes = 8 << 20
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	var page struct {
		Hits    []traceSearchHit `json:"hits"`
		Total   int              `json:"total"`
		Partial bool             `json:"is_partial"`
	}
	if err != nil || len(data) > maxBytes || json.Unmarshal(data, &page) != nil || page.Hits == nil {
		return result, problem("unavailable", "invalid or oversized Trace search response")
	}
	var first int64
	seen := make(map[string]bool)
	for _, hit := range page.Hits {
		if _, err := trace.SpanIDFromHex(hit.ID); err != nil || hit.TraceID != id || hit.StartNS <= 0 || hit.DurationUS < 0 || seen[hit.ID] {
			continue
		}
		seen[hit.ID] = true
		if first == 0 {
			first = hit.StartNS
			result.StartedAt = time.Unix(0, first).UTC()
		}
		result.Spans = append(result.Spans, telemetrySpan{
			ID: hit.ID, ParentID: hit.ParentID, Name: hit.Name, Service: hit.Service,
			OffsetUS: float64(hit.StartNS-first) / 1000, DurationUS: hit.DurationUS, Status: hit.Status,
			Role: hit.Role, Tool: hit.Tool, Action: hit.Action, Outcome: hit.Outcome, CallerID: hit.CallerID, EventID: hit.EventID,
		})
		if len(result.Spans) == observerTraceLimit {
			break
		}
	}
	result.Truncated = len(page.Hits) > observerTraceLimit || page.Total > observerTraceLimit || page.Partial
	if len(result.Spans) == 0 {
		return result, problem("not_found", "Trace has no spans in the search window")
	}
	return result, nil
}
