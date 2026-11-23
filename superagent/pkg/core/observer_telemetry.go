package core

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"golang.org/x/time/rate"
)

const (
	observerOTLPContentType = "application/x-protobuf"
	observerOTLPMaxRequest  = 1 << 20
	observerOTLPMaxResponse = 32 << 10
	observerOTLPTimeout     = 3 * time.Second
)

type observerTelemetryGateway struct {
	origin   string
	client   *http.Client
	limit    *rate.Limiter
	inFlight chan struct{}
}

func newObserverTelemetryGateway(endpoint string) (*observerTelemetryGateway, error) {
	if endpoint == "" {
		return nil, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.Opaque != "" || u.User != nil ||
		u.ForceQuery || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" ||
		(u.Path != "" && u.Path != "/") || !net.ParseIP(u.Hostname()).IsLoopback() {
		return nil, problem("invalid_argument", "Web OTLP endpoint must be an HTTP loopback origin with a port")
	}
	if port, portErr := strconv.ParseUint(u.Port(), 10, 16); portErr != nil || port == 0 {
		return nil, problem("invalid_argument", "Web OTLP endpoint must be an HTTP loopback origin with a port")
	}
	return &observerTelemetryGateway{
		origin: "http://" + u.Host,
		client: &http.Client{
			// A dedicated transport avoids environment proxies and implicit compression.
			Transport: &http.Transport{
				DisableCompression:  true,
				MaxIdleConnsPerHost: 4,
				IdleConnTimeout:     30 * time.Second,
			},
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		limit:    rate.NewLimiter(16, 16),
		inFlight: make(chan struct{}, 8),
	}, nil
}

func (g *observerTelemetryGateway) serve(w http.ResponseWriter, r *http.Request) {
	var upstreamPath string
	switch r.URL.Path {
	case "/v1/telemetry/traces":
		upstreamPath = "/v1/traces"
	case "/v1/telemetry/metrics":
		upstreamPath = "/v1/metrics"
	default:
		http.NotFound(w, r)
		return
	}
	if g == nil || r.URL.RawPath != "" || r.URL.ForceQuery || r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Content-Type") != observerOTLPContentType || r.Header.Get("Content-Encoding") != "" {
		w.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}
	if r.ContentLength > observerOTLPMaxRequest {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	if !g.limit.Allow() {
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	select {
	case g.inFlight <- struct{}{}:
		defer func() { <-g.inFlight }()
	default:
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}

	deadline := time.Now().Add(observerOTLPTimeout)
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(deadline); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	defer func() { _ = controller.SetReadDeadline(time.Time{}) }()
	if err := controller.SetWriteDeadline(deadline); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
	ctx, cancel := context.WithDeadline(r.Context(), deadline)
	defer cancel()
	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, observerOTLPMaxRequest))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
		} else {
			w.WriteHeader(http.StatusBadRequest)
		}
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.origin+upstreamPath, bytes.NewReader(payload))
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	req.Header.Set("Content-Type", observerOTLPContentType)
	req.Header.Set("User-Agent", "superagent-web-otlp")
	res, err := g.client.Do(req)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	defer func() { _ = res.Body.Close() }()
	switch res.StatusCode {
	case http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusBadRequest:
		w.WriteHeader(res.StatusCode)
	case http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(res.Body, observerOTLPMaxResponse+1))
		if err != nil || len(body) > observerOTLPMaxResponse {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", observerOTLPContentType)
		_, _ = w.Write(body)
	default:
		w.WriteHeader(http.StatusBadGateway)
	}
}
