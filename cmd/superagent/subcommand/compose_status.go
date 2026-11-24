package subcommand

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/zoumo/mass/superagent/pkg/core"
	"github.com/zoumo/mass/superagent/triggers"
)

const (
	triggerProbeTimeout = time.Second
	maxTriggerStatus    = 2 << 20
	maxWatchdogSnapshot = 64 << 10
	composeWatchdogPath = "/watchdog"
)

type triggerStatusTarget struct {
	source   string
	endpoint string
	child    *childProcess
}

type composeStatusServer struct {
	mu            sync.RWMutex
	targets       map[string]triggerStatusTarget
	watchdog      core.WatchdogSnapshot
	watchdogChild *childProcess
	server        *http.Server
	client        *http.Client
	directory     string
	socket        string
	done          chan struct{}
}

func startComposeStatus(ctx context.Context, enabled []composeTrigger, watchdogEnabled bool) (*composeStatusServer, error) {
	// A short path works with the Unix socket limits on macOS and Linux.
	directory, err := os.MkdirTemp("/tmp", "mass-status-")
	if err != nil {
		return nil, errors.New("cannot create Compose status directory")
	}
	socket := filepath.Join(directory, "s")
	listener, err := new(net.ListenConfig).Listen(ctx, "unix", socket)
	if err != nil {
		_ = os.RemoveAll(directory)
		return nil, errors.New("cannot listen on Compose status socket")
	}
	s := &composeStatusServer{
		targets: make(map[string]triggerStatusTarget, len(enabled)), directory: directory, socket: socket, done: make(chan struct{}),
		watchdog: core.WatchdogSnapshot{Enabled: watchdogEnabled},
		client: &http.Client{
			Timeout: triggerProbeTimeout, Transport: &http.Transport{DisableKeepAlives: true},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
	for _, trigger := range enabled {
		s.targets[trigger.kind] = triggerStatusTarget{source: trigger.binding.Source}
	}
	s.server = &http.Server{Handler: http.HandlerFunc(s.serve), ReadHeaderTimeout: time.Second, WriteTimeout: 2 * time.Second}
	go func() {
		defer close(s.done)
		_ = s.server.Serve(listener)
	}()
	return s, nil
}

func (s *composeStatusServer) close() error {
	err := s.server.Close()
	<-s.done
	return errors.Join(err, os.RemoveAll(s.directory))
}

func (s *composeStatusServer) register(kind, endpoint string, child *childProcess) {
	s.mu.Lock()
	defer s.mu.Unlock()
	target := s.targets[kind]
	target.child = child
	target.endpoint = endpoint
	s.targets[kind] = target
}

func (s *composeStatusServer) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/status" && r.URL.Path != composeWatchdogPath {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	if r.URL.Path == composeWatchdogPath {
		snapshot := s.watchdog
		if s.watchdogChild != nil {
			select {
			case <-s.watchdogChild.done:
			default:
				snapshot.Running = true
			}
		}
		s.mu.RUnlock()
		writeComposeStatus(w, snapshot)
		return
	}
	github, githubEnabled := s.targets[triggers.GitHub]
	dws, dwsEnabled := s.targets[triggers.DWS]
	s.mu.RUnlock()
	var snapshot core.TriggerStatuses
	var probes sync.WaitGroup
	probes.Go(func() { snapshot.GitHub = s.probe(r.Context(), github, githubEnabled, githubState) })
	snapshot.DWS = s.probe(r.Context(), dws, dwsEnabled, dwsState)
	probes.Wait()
	writeComposeStatus(w, snapshot)
}

func writeComposeStatus(w http.ResponseWriter, snapshot any) {
	body, err := json.Marshal(snapshot)
	if err != nil {
		http.Error(w, "cannot encode Compose status", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func (s *composeStatusServer) registerWatchdog(child *childProcess) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.watchdogChild = child
}

func (s *composeStatusServer) recordWatchdog(check core.WatchdogCheck) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.watchdog.Latest = &check
}

func (s *composeStatusServer) probe(ctx context.Context, target triggerStatusTarget, enabled bool, normalize func(json.RawMessage, string) string) core.ComponentStatus {
	if !enabled {
		return core.ComponentStatus{Status: "disabled"}
	}
	if target.child == nil {
		return core.ComponentStatus{Status: "starting"}
	}
	unavailable := core.ComponentStatus{Status: "unavailable"}
	select {
	case <-target.child.done:
		return unavailable
	default:
	}
	if target.endpoint == "" {
		return core.ComponentStatus{Status: "starting"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.endpoint+"/status", http.NoBody)
	if err != nil {
		return unavailable
	}
	response, err := s.client.Do(req)
	if err != nil {
		return unavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return unavailable
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxTriggerStatus+1))
	if err != nil || len(body) > maxTriggerStatus {
		return unavailable
	}
	state := normalize(body, target.source)
	if state == "unavailable" {
		return unavailable
	}
	select {
	case <-target.child.done:
		return unavailable
	default:
	}
	observedAt := time.Now().UTC()
	return core.ComponentStatus{Status: state, ObservedAt: &observedAt}
}

// Core treats the supervisor response as observations only. If the socket
// closes or a snapshot cannot be decoded, configuration still identifies
// which Triggers were supposed to run.
func readComposeStatus(socket string, enabled []string) func(context.Context) core.TriggerStatuses {
	fallback := core.TriggerStatuses{
		GitHub: core.ComponentStatus{Status: "disabled"}, DWS: core.ComponentStatus{Status: "disabled"},
	}
	for _, kind := range enabled {
		switch kind {
		case triggers.GitHub:
			fallback.GitHub.Status = "unavailable"
		case triggers.DWS:
			fallback.DWS.Status = "unavailable"
		}
	}
	client := composeStatusClient(socket)
	return func(ctx context.Context) core.TriggerStatuses {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://compose/status", http.NoBody)
		if err != nil {
			return fallback
		}
		response, err := client.Do(req)
		if err != nil {
			return fallback
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return fallback
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 4097))
		if err != nil || len(body) > 4096 {
			return fallback
		}
		var statuses core.TriggerStatuses
		if json.Unmarshal(body, &statuses) != nil {
			return fallback
		}
		return core.TriggerStatuses{
			GitHub: supervisedStatus(statuses.GitHub, fallback.GitHub), DWS: supervisedStatus(statuses.DWS, fallback.DWS),
		}
	}
}

func readComposeWatchdog(socket string) func(context.Context) (core.WatchdogSnapshot, error) {
	client := composeStatusClient(socket)
	client.Timeout = time.Second
	return func(ctx context.Context) (core.WatchdogSnapshot, error) {
		var snapshot core.WatchdogSnapshot
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://compose"+composeWatchdogPath, http.NoBody)
		if err != nil {
			return snapshot, err
		}
		response, err := client.Do(req)
		if err != nil {
			return snapshot, err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return snapshot, errors.New("cannot read Compose Watchdog snapshot")
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, maxWatchdogSnapshot+1))
		if err != nil {
			return snapshot, err
		}
		if len(body) > maxWatchdogSnapshot {
			return snapshot, errors.New("compose Watchdog snapshot exceeds size limit")
		}
		err = json.Unmarshal(body, &snapshot)
		return snapshot, err
	}
}

func composeStatusClient(socket string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return new(net.Dialer).DialContext(ctx, "unix", socket)
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func supervisedStatus(observation, fallback core.ComponentStatus) core.ComponentStatus {
	if fallback.Status == "disabled" {
		return fallback
	}
	switch observation.Status {
	case "starting", "ready", "degraded", "unavailable":
		return observation
	default:
		return fallback
	}
}

func githubState(body json.RawMessage, source string) string {
	type stream struct {
		State string `json:"state"`
	}
	var report struct {
		Source           string    `json:"source"`
		RateLimitedUntil time.Time `json:"rateLimitedUntil"`
		Notifications    stream    `json:"notifications"`
		Repositories     map[string]struct {
			Streams           map[string]stream `json:"streams"`
			LastDeliveryError string            `json:"lastDeliveryError"`
		} `json:"repositories"`
	}
	if json.Unmarshal(body, &report) != nil || report.Source != source || report.Repositories == nil {
		return "unavailable"
	}
	state := "ready"
	inspect := func(current string) {
		switch current {
		case "ready", "inactive":
		case "starting":
			if state == "ready" {
				state = "starting"
			}
		case "stale":
			if state != "unavailable" {
				state = "degraded"
			}
		default:
			state = "unavailable"
		}
	}
	inspect(report.Notifications.State)
	for _, repository := range report.Repositories {
		if repository.LastDeliveryError != "" {
			inspect("stale")
		}
		for _, entry := range repository.Streams {
			inspect(entry.State)
		}
	}
	if time.Now().Before(report.RateLimitedUntil) {
		inspect("stale")
	}
	return state
}

func dwsState(body json.RawMessage, source string) string {
	var report struct {
		Source        string `json:"source"`
		Connection    string `json:"connection"`
		ConsumerReady *bool  `json:"consumerReady"`
		Admission     string `json:"admission"`
	}
	if json.Unmarshal(body, &report) != nil || report.Source != source || report.ConsumerReady == nil {
		return "unavailable"
	}
	switch report.Admission {
	case "waiting", "pending", "accepted", "failed":
	default:
		return "unavailable"
	}
	switch report.Connection {
	case "connecting", "recovering":
		if report.Admission != "failed" {
			return "starting"
		}
	case "ready":
		if *report.ConsumerReady && report.Admission != "failed" {
			return "ready"
		}
	case "disconnected":
	default:
		return "unavailable"
	}
	return "degraded"
}
