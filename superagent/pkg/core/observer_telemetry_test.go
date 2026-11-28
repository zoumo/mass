package core_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/zoumo/mass/superagent/pkg/core"
)

func startObserverTelemetryCore(t *testing.T, receiver string, options core.TelemetryOptions) string {
	t.Helper()
	f := fixtureAt(t)
	cfg, err := core.LoadConfig(f.config)
	require.NoError(t, err)
	cfg.WebOTLPEndpoint = receiver
	server, err := core.StartWithContext(t.Context(), cfg, options)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- server.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, server.Shutdown(ctx))
		require.NoError(t, <-done)
	})
	return server.Endpoint()
}

func TestObserverTelemetryGateway(t *testing.T) {
	const contentType = "application/x-protobuf"
	type batch struct {
		method, path, query, body string
		header                    http.Header
	}
	forwarded := make(chan batch, 10)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		forwarded <- batch{r.Method, r.URL.Path, r.URL.RawQuery, string(body), r.Header.Clone()}
		if string(body) == "large-response" {
			_, _ = io.WriteString(w, strings.Repeat("a", 32769))
			return
		}
		if string(body) == "try-redirect" {
			http.Redirect(w, r, "/unexpected", http.StatusTemporaryRedirect)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Set-Cookie", "receiver=secret")
		_, _ = io.WriteString(w, "receiver-response")
	}))
	t.Cleanup(receiver.Close)
	recorder := tracetest.NewSpanRecorder()
	traces := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { require.NoError(t, traces.Shutdown(context.Background())) })
	_, port, err := net.SplitHostPort(strings.TrimPrefix(receiver.URL, "http://"))
	require.NoError(t, err)
	endpoint := startObserverTelemetryCore(t, "http://localhost:"+port+"/collector/", core.TelemetryOptions{Traces: traces})
	client := &http.Client{Timeout: 2 * time.Second}
	type response struct {
		status int
		header http.Header
		body   string
	}
	request := func(endpoint, method, path, mediaType string, body io.Reader, headers ...[2]string) response {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, endpoint+path, body)
		require.NoError(t, err)
		req.Header.Set("Content-Type", mediaType)
		for _, header := range headers {
			req.Header.Set(header[0], header[1])
			if header[0] == "Host" {
				req.Host = header[1]
			}
		}
		res, err := client.Do(req)
		require.NoError(t, err)
		defer func() { _ = res.Body.Close() }()
		data, err := io.ReadAll(res.Body)
		require.NoError(t, err)
		return response{res.StatusCode, res.Header, string(data)}
	}
	disabled := startObserverTelemetryCore(t, "", core.TelemetryOptions{})
	path := "/v1/telemetry/traces"
	require.Equal(t, http.StatusNotFound, request(disabled, http.MethodPost, path, contentType, nil).status)
	require.Equal(t, http.StatusNotFound, request(endpoint, http.MethodPost, "/v1/telemetry/logs", contentType, nil).status)
	require.Equal(t, http.StatusNotFound, request(endpoint, http.MethodPost, path+"?destination=override", contentType, nil).status)
	require.Equal(t, http.StatusMethodNotAllowed, request(endpoint, http.MethodGet, path, contentType, nil).status)
	require.Equal(t, http.StatusUnsupportedMediaType, request(endpoint, http.MethodPost, path, "application/json", nil).status)
	require.Equal(t, http.StatusUnsupportedMediaType, request(endpoint, http.MethodPost, path, contentType, nil, [2]string{"Content-Encoding", "gzip"}).status)
	for _, header := range [][2]string{{"Origin", "http://foreign.invalid"}, {"Sec-Fetch-Site", "cross-site"}, {"Host", "foreign.invalid"}} {
		require.Equal(t, http.StatusForbidden, request(endpoint, http.MethodPost, path, contentType, nil, header).status)
	}
	large := strings.Repeat("x", (1<<20)+1)
	require.Equal(t, http.StatusRequestEntityTooLarge,
		request(endpoint, http.MethodPost, path, contentType, strings.NewReader(large)).status)
	require.Equal(t, http.StatusRequestEntityTooLarge,
		request(endpoint, http.MethodPost, path, contentType, io.LimitReader(strings.NewReader(large), int64(len(large)))).status)
	require.Empty(t, forwarded)

	for _, signal := range []string{"traces", "metrics"} {
		res := request(endpoint, http.MethodPost, "/v1/telemetry/"+signal, contentType, strings.NewReader(signal),
			[2]string{"Authorization", "secret"}, [2]string{"Cookie", "session=private"}, [2]string{"Origin", endpoint},
			[2]string{"Traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"}, [2]string{"X-Private", "secret"})
		require.Equal(t, http.StatusOK, res.status)
		require.Empty(t, res.header.Get("Set-Cookie"))
		require.Equal(t, "receiver-response", res.body)
		got := <-forwarded
		require.Equal(t, http.MethodPost, got.method)
		require.Equal(t, "/collector/v1/"+signal, got.path)
		require.Equal(t, signal, got.body)
		require.Empty(t, got.query)
		require.Equal(t, contentType, got.header.Get("Content-Type"))
		for _, header := range []string{"Authorization", "Cookie", "Origin", "Traceparent", "X-Private"} {
			require.Empty(t, got.header.Get(header), header)
		}
	}
	res := request(endpoint, http.MethodPost, path, contentType, strings.NewReader("try-redirect"))
	require.Equal(t, http.StatusBadGateway, res.status)
	require.Empty(t, res.header.Get("Location"))
	res = request(endpoint, http.MethodPost, path, contentType, strings.NewReader("large-response"))
	require.Equal(t, http.StatusBadGateway, res.status)
	require.Empty(t, res.body)
	require.Len(t, forwarded, 2)
	require.Empty(t, recorder.Ended(), "the gateway bypasses ordinary Core telemetry")
	require.Equal(t, http.StatusMethodNotAllowed, request(endpoint, http.MethodPost, "/status", contentType, nil).status)
}

func TestObserverTelemetryRequiresHTTPBaseURL(t *testing.T) {
	f := fixtureAt(t)
	cfg, err := core.LoadConfig(f.config)
	require.NoError(t, err)
	for _, endpoint := range []string{
		"http:///collector", "ftp://collector.example.com", "https://collector.example.com#fragment",
		"http://user@collector.example.com", "http://collector.example.com?secret",
		"http://collector.example.com:65536/otlp", "https://[not-an-ip]/otlp",
	} {
		cfg.WebOTLPEndpoint = endpoint
		server, err := core.StartWithContext(t.Context(), cfg, core.TelemetryOptions{})
		require.Nil(t, server)
		require.EqualError(t, err, "invalid_argument: Web OTLP endpoint must be an HTTP or HTTPS base URL without credentials, a query, or a fragment")
	}
}

func TestTelemetryGatewayBoundsConcurrencyAndTime(t *testing.T) {
	entered := make(chan struct{}, 8)
	exited := make(chan struct{}, 8)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		entered <- struct{}{}
		<-r.Context().Done()
		exited <- struct{}{}
	}))
	t.Cleanup(upstream.Close)
	endpoint := startObserverTelemetryCore(t, upstream.URL, core.TelemetryOptions{})
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
	defer cancel()
	results := make(chan int, cap(entered))
	for range cap(entered) {
		go func() {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/v1/telemetry/traces", strings.NewReader("batch"))
			if err != nil {
				results <- -1
				return
			}
			req.Header.Set("Content-Type", "application/x-protobuf")
			res, err := http.DefaultClient.Do(req)
			// The response write deadline may close a timed-out connection.
			if err != nil {
				results <- 0
				return
			}
			_ = res.Body.Close()
			results <- res.StatusCode
		}()
	}
	for range cap(entered) {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("all eight exports did not reach the receiver")
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/v1/telemetry/traces", strings.NewReader("excess"))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-protobuf")
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.NoError(t, res.Body.Close())
	require.Equal(t, http.StatusTooManyRequests, res.StatusCode)
	for range cap(exited) {
		select {
		case <-exited:
		case <-ctx.Done():
			t.Fatal("the gateway did not cancel the upstream requests on deadline")
		}
	}
	for range cap(results) {
		select {
		case status := <-results:
			require.Contains(t, []int{0, http.StatusBadGateway}, status)
		case <-ctx.Done():
			t.Fatal("the gateway did not finish the client requests on deadline")
		}
	}
}
