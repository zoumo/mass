package triggers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	githubOverlap      = 2 * time.Minute
	githubFullInterval = 6 * time.Hour
)

type githubScope struct {
	name string
	poll func(context.Context, *githubAPI, string, *githubStreamState, time.Time) ([]githubObservation, error)
}

var githubScopes = []githubScope{
	{name: "issues", poll: pollGitHubIssues},
	{name: githubPullsScope, poll: pollGitHubPulls},
}

type githubTrigger struct {
	config   githubConfig
	api      *githubAPI
	core     *CoreClient
	store    githubStore
	interval time.Duration
	matchKey string
	log      *json.Encoder
}

func runGitHub(ctx context.Context, c githubConfig, api *githubAPI, core *CoreClient, output io.Writer) error {
	store, closeDB, err := openGitHubStore(c)
	if err != nil {
		return err
	}
	defer closeDB()
	api.pauseUntil, err = readGitHubRateLimit(store.db)
	if err != nil {
		return errors.New("cannot read GitHub rate limit state")
	}
	t := &githubTrigger{
		config: c, api: api, core: core, store: store, interval: time.Duration(c.PollSeconds) * time.Second,
		matchKey: c.Match.key(), log: json.NewEncoder(output),
	}
	listener, err := new(net.ListenConfig).Listen(ctx, "tcp", c.Listen)
	if err != nil {
		return errors.New("cannot bind GitHub status listener")
	}
	server := &http.Server{ReadHeaderTimeout: 3 * time.Second, Handler: http.HandlerFunc(t.serveStatus)}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	if err := t.log.Encode(map[string]string{"status": "starting", "statusEndpoint": "http://" + listener.Addr().String(), "source": githubSource}); err != nil {
		return err
	}
	for ctx.Err() == nil {
		if err := t.round(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
		case <-time.After(200 * time.Millisecond):
		}
	}
	return nil
}

func (t *githubTrigger) round(ctx context.Context) error {
	for _, repo := range t.config.Repositories {
		delivery, err := readGitHubDelivery(t.store.db, repo.Repository)
		if err != nil {
			return errors.New("cannot read GitHub delivery state")
		}
		if delivery.Backlog > 0 && !time.Now().Before(delivery.NextDeliveryAt) {
			if err := t.deliver(ctx, repo.Repository, delivery); err != nil {
				return err
			}
		}
		for _, scope := range githubScopes {
			if ctx.Err() != nil {
				return nil //nolint:nilerr // Cancellation ends polling normally.
			}
			if err := t.poll(ctx, repo, scope); err != nil {
				return err
			}
		}
		if err := t.pollNextGitHubDiscussion(ctx, repo); err != nil {
			return err
		}
		if err := t.pollNextGitHubCI(ctx, repo); err != nil {
			return err
		}
	}
	return nil
}

func (t *githubTrigger) poll(ctx context.Context, repo githubRepository, scope githubScope) error {
	now := time.Now().UTC()
	if ctx.Err() != nil || now.Before(t.api.pauseUntil) {
		return nil //nolint:nilerr // Cancellation ends polling normally.
	}
	stream, err := t.store.stream(repo.Repository, scope.name)
	if err != nil {
		return errors.New("cannot read GitHub stream state")
	}
	if !stream.Active || now.Before(stream.NextAttemptAt) {
		return nil
	}
	stream.Items, err = t.store.scopeItems(repo.Repository, scope.name)
	if err != nil {
		return errors.New("cannot read GitHub scope snapshots")
	}
	interval := t.interval
	if stream.DiscussionClosed {
		interval = max(interval, githubFullInterval)
	}
	var since time.Time
	parent := scope.name == "issues" || scope.name == githubPullsScope
	if parent {
		stream.Match = t.config.Match
	}
	if (!parent || stream.MatchKey == t.matchKey) && now.Before(stream.LastFullAt.Add(githubFullInterval)) {
		since = stream.LastSuccessAt.Add(-githubOverlap).Truncate(time.Second)
	}
	observations, err := scope.poll(ctx, t.api, repo.Repository, &stream, since)
	if ctx.Err() != nil {
		return nil //nolint:nilerr // Cancellation ends polling normally.
	}
	finished := time.Now().UTC()
	if err != nil {
		stream.Failures++
		stream.LastError = err.Error()
		stream.NextAttemptAt = finished.Add(githubBackoff(min(interval, time.Minute), stream.Failures, 15*time.Minute))
		if t.api.pauseUntil.After(stream.NextAttemptAt) {
			stream.NextAttemptAt = t.api.pauseUntil
		}
		if err := t.store.savePollFailure(repo.Repository, scope.name, stream, t.api.pauseUntil); err != nil {
			return errors.New("cannot persist GitHub poll failure")
		}
		writeDiagnostic(t.log, map[string]string{"event": "github_poll_failed", "repository": repo.Repository, "scope": scope.name, "error": stream.LastError})
		return nil
	}
	if parent {
		stream.MatchKey = t.matchKey
	}
	queued, err := t.store.commitScan(t.config, repo, scope.name, stream, observations, now, finished.Add(interval), t.api.pauseUntil, since.IsZero())
	if err != nil {
		return errors.New("cannot commit GitHub scan")
	}
	writeDiagnostic(t.log, map[string]any{"event": "github_polled", "repository": repo.Repository, "scope": scope.name, "queued": queued})
	return nil
}

func (t *githubTrigger) deliver(ctx context.Context, name string, current githubDeliveryState) error {
	for n := 0; n < min(16, current.Backlog) && ctx.Err() == nil; n++ {
		item, err := t.store.pendingHead(name)
		if err != nil {
			return errors.New("cannot read GitHub pending head")
		}
		result := t.core.submitPayload(ctx, item.payload)
		if ctx.Err() != nil {
			return nil //nolint:nilerr // Cancellation ends polling normally.
		}
		if result.code != "" {
			current.LastDeliveryError = result.code
			current.DeliveryFailures++
			base := time.Second
			if !result.retry {
				base = time.Minute
			}
			current.NextDeliveryAt = time.Now().UTC().Add(githubBackoff(base, current.DeliveryFailures, 15*time.Minute))
			if err := t.store.saveDelivery(name, current, 0); err != nil {
				return errors.New("cannot persist GitHub delivery retry")
			}
			writeDiagnostic(t.log, map[string]string{"event": "github_delivery_failed", "repository": name, "error": result.code})
			return nil
		}
		current.Accepted++
		if result.receipt.Duplicate || result.receipt.Status == "duplicate" {
			current.Duplicates++
		}
		current.LastReceipt = result.receipt
		current.LastDeliveryError = ""
		current.NextDeliveryAt = time.Time{}
		current.DeliveryFailures = 0
		if err := t.store.saveDelivery(name, current, item.sequence); err != nil {
			return errors.New("cannot acknowledge GitHub pending head")
		}
		writeDiagnostic(t.log, map[string]string{"event": "github_message_accepted", "repository": name, "messageId": result.receipt.MessageID, "status": result.receipt.Status})
	}
	return nil
}

func githubBackoff(base time.Duration, failures int, limit time.Duration) time.Duration {
	return max(base, min(limit, base*time.Duration(1<<min(failures-1, 8))))
}

type githubStatus struct {
	Source           string                            `json:"source"`
	AccountID        string                            `json:"accountId"`
	InstanceID       string                            `json:"instanceId"`
	RateLimitedUntil time.Time                         `json:"rateLimitedUntil,omitzero"`
	Repositories     map[string]githubRepositoryStatus `json:"repositories"`
}

type githubRepositoryStatus struct {
	Streams           map[string]githubStreamStatus `json:"streams"`
	Backlog           int                           `json:"backlog"`
	Accepted          uint64                        `json:"accepted"`
	Duplicates        uint64                        `json:"duplicates"`
	LastReceipt       Receipt                       `json:"lastReceipt,omitzero"`
	LastDeliveryError string                        `json:"lastDeliveryError,omitempty"`
	NextDeliveryAt    time.Time                     `json:"nextDeliveryAt,omitzero"`
}

type githubStreamStatus struct {
	State         string            `json:"state"`
	Items         int               `json:"items"`
	LastSuccessAt time.Time         `json:"lastSuccessAt,omitzero"`
	LastFullAt    time.Time         `json:"lastFullAt,omitzero"`
	NextAttemptAt time.Time         `json:"nextAttemptAt,omitzero"`
	CI            *githubCISnapshot `json:"ci,omitempty"`
	HeadSHA       string            `json:"headSha,omitempty"`
	TestMergeSHA  string            `json:"testMergeSha,omitempty"`
	LastError     string            `json:"lastError,omitempty"`
}

func (t *githubTrigger) readStatus(ctx context.Context) (githubStatus, error) {
	tx, err := t.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return githubStatus{}, err
	}
	defer func() { _ = tx.Rollback() }()
	pause, err := readGitHubRateLimit(tx)
	if err != nil {
		return githubStatus{}, err
	}
	s := githubStatus{githubSource, t.config.AccountID, t.config.InstanceID, pause, make(map[string]githubRepositoryStatus)}
	for _, repo := range t.config.Repositories {
		out, err := readGitHubRepositoryStatus(tx, repo.Repository)
		if err != nil {
			return githubStatus{}, err
		}
		s.Repositories[repo.Repository] = out
	}
	return s, nil
}

func githubStreamStatusFor(v githubStreamState) githubStreamStatus {
	state := "ready"
	if !v.Active {
		state = "inactive"
	} else if v.LastError != "" {
		state = "stale"
	} else if v.LastSuccessAt.IsZero() || v.SilentBaseline {
		state = "starting"
	}
	return githubStreamStatus{State: state, Items: v.ItemCount, LastSuccessAt: v.LastSuccessAt, LastFullAt: v.LastFullAt, NextAttemptAt: v.NextAttemptAt, LastError: v.LastError}
}

func (t *githubTrigger) serveStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path != "/status" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	status, err := t.readStatus(r.Context())
	if err != nil {
		http.Error(w, "GitHub status unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	writeDiagnostic(json.NewEncoder(w), status)
}

func githubStatusCommand(ctx context.Context, endpoint string, output io.Writer) error {
	if err := validateStatusEndpoint(endpoint); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/status", http.NoBody)
	if err != nil {
		return errors.New("invalid GitHub status request")
	}
	response, err := httpClient(3 * time.Second).Do(req)
	if err != nil {
		return errors.New("GitHub status unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub status HTTP %d", response.StatusCode)
	}
	var s githubStatus
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&s); err != nil || s.Source != githubSource || s.Repositories == nil {
		return errors.New("invalid GitHub status response")
	}
	return json.NewEncoder(output).Encode(s)
}
