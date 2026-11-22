package github

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

	"github.com/zoumo/mass/superagent/triggers/internal/coreclient"
	"github.com/zoumo/mass/superagent/triggers/internal/trigger"
)

type githubTrigger struct {
	config   githubConfig
	api      *githubAPI
	core     *coreclient.Client
	store    githubStore
	interval time.Duration
	log      *json.Encoder
}

func runGitHub(ctx context.Context, c githubConfig, api *githubAPI, core *coreclient.Client, output io.Writer) error {
	store, closeDB, err := openGitHubStore(c)
	if err != nil {
		return err
	}
	defer closeDB()
	api.pauseUntil, err = readGitHubRateLimit(store.db)
	if err != nil {
		return errors.New("cannot read GitHub rate limit state")
	}
	t := &githubTrigger{config: c, api: api, core: core, store: store, interval: time.Duration(c.PollSeconds) * time.Second, log: json.NewEncoder(output)}
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
	names, err := githubRepositoryNames(t.store.db, true)
	if err != nil {
		return errors.New("cannot read GitHub delivery repositories")
	}
	for _, name := range names {
		delivery, err := readGitHubDelivery(t.store.db, name)
		if err != nil {
			return errors.New("cannot read GitHub delivery state")
		}
		if !time.Now().Before(delivery.NextDeliveryAt) {
			if err := t.deliver(ctx, name, delivery); err != nil {
				return err
			}
		}
	}
	if err := t.pollNotifications(ctx); err != nil {
		return err
	}
	if err := t.pollReadbacks(ctx, "discussion", githubNoticePrefix); err != nil {
		return err
	}
	return t.pollReadbacks(ctx, "ci", githubTrackedPrefix)
}

func (t *githubTrigger) pollReadbacks(ctx context.Context, kind, prefix string) error {
	if ctx.Err() != nil || time.Now().Before(t.api.pauseUntil) {
		return nil //nolint:nilerr // Cancellation ends polling normally.
	}
	due, err := t.store.dueScopes(t.config, kind, prefix, time.Now())
	if err != nil {
		return errors.New("cannot read GitHub readback schedule")
	}
	for _, next := range due {
		if ctx.Err() != nil || time.Now().Before(t.api.pauseUntil) {
			return nil //nolint:nilerr // Cancellation ends polling normally.
		}
		repo, _ := t.config.repository(next.repository)
		if err := t.readBack(ctx, repo, next.scope); err != nil {
			return err
		}
	}
	return nil
}

func (t *githubTrigger) readBack(ctx context.Context, repo githubRepository, scope string) error {
	stream, err := t.store.stream(repo.Repository, scope)
	if err != nil {
		return errors.New("cannot read GitHub readback state")
	}
	stream.Items, err = t.store.scopeItems(repo.Repository, scope)
	if err != nil {
		return errors.New("cannot read GitHub readback snapshots")
	}
	started := time.Now().UTC()
	var items []githubObservation
	var pull *githubPullRecord
	notification := strings.HasPrefix(scope, githubNoticePrefix)
	if notification {
		var notice githubNotice
		if json.Unmarshal(stream.Items[githubNoticeKey].Data, &notice) != nil || notice.ID == "" || notice.Number <= 0 {
			return errors.New("invalid saved GitHub notification")
		}
		items, pull, err = readGitHubNotification(ctx, t.api, repo.Repository, notice, &stream)
	} else {
		items, pull, err = readGitHubTrackedPR(ctx, t.api, repo.Repository, &stream)
	}
	if ctx.Err() != nil {
		if t.api.pauseUntil.After(started) {
			return t.finishPoll(ctx, repo.Repository, scope, stream, started, t.interval, ctx.Err())
		}
		return nil //nolint:nilerr // Cancellation ends polling normally.
	}
	if err != nil {
		return t.finishPoll(ctx, repo.Repository, scope, stream, started, t.interval, err)
	}
	stream.LastSuccessAt = started
	stream.LastError, stream.Failures = "", 0
	stream.Active = !notification && pull.State == "open"
	stream.NextAttemptAt = time.Time{}
	if stream.Active {
		stream.NextAttemptAt = time.Now().UTC().Add(t.interval)
	}
	queued, err := t.store.commitReadback(t.config, repo, scope, stream, items, t.api.pauseUntil, pull)
	if err != nil {
		return errors.New("cannot commit GitHub readback")
	}
	trigger.WriteDiagnostic(t.log, map[string]any{"event": "github_readback", "repository": repo.Repository, "scope": scope, "queued": queued})
	return nil
}

func (t *githubTrigger) finishPoll(ctx context.Context, repo, scope string, stream githubStreamState, started time.Time, interval time.Duration, pollErr error) error {
	// Persist a rate-limit response even when it interrupted a page or a readback.
	finished := time.Now().UTC()
	if pollErr == nil {
		stream.LastSuccessAt = started
		stream.LastError, stream.Failures = "", 0
		stream.NextAttemptAt = finished.Add(interval)
	} else if ctx.Err() == nil {
		stream.Failures++
		stream.LastError = pollErr.Error()
		backoff := githubBackoff(min(t.interval, time.Minute), stream.Failures, 15*time.Minute)
		if scope == githubInboxScope {
			backoff = max(backoff, interval)
		}
		stream.NextAttemptAt = finished.Add(backoff)
		trigger.WriteDiagnostic(t.log, map[string]string{"event": "github_poll_failed", "repository": repo, "scope": scope, "error": stream.LastError})
	}
	if t.api.pauseUntil.After(stream.NextAttemptAt) {
		stream.NextAttemptAt = t.api.pauseUntil
	}
	if err := t.store.savePoll(repo, scope, stream, t.api.pauseUntil); err != nil {
		return errors.New("cannot persist GitHub poll state")
	}
	return nil
}

func (t *githubTrigger) deliver(ctx context.Context, name string, current githubDeliveryState) error {
	for n := 0; n < min(16, current.Backlog) && ctx.Err() == nil; n++ {
		item, err := t.store.pendingHead(name)
		if err != nil {
			return errors.New("cannot read GitHub pending head")
		}
		result := t.core.SubmitPayload(ctx, item.payload)
		if ctx.Err() != nil {
			return nil //nolint:nilerr // Cancellation ends polling normally.
		}
		if result.Code != "" {
			current.LastDeliveryError = result.Code
			current.DeliveryFailures++
			base := time.Second
			if !result.Retry {
				base = time.Minute
			}
			current.NextDeliveryAt = time.Now().UTC().Add(githubBackoff(base, current.DeliveryFailures, 15*time.Minute))
			if err := t.store.saveDelivery(name, current, 0); err != nil {
				return errors.New("cannot persist GitHub delivery retry")
			}
			trigger.WriteDiagnostic(t.log, map[string]string{"event": "github_delivery_failed", "repository": name, "error": result.Code})
			return nil
		}
		current.Accepted++
		if result.Receipt.Duplicate || result.Receipt.Status == "duplicate" {
			current.Duplicates++
		}
		current.LastReceipt = result.Receipt
		current.LastDeliveryError = ""
		current.NextDeliveryAt = time.Time{}
		current.DeliveryFailures = 0
		if err := t.store.saveDelivery(name, current, item.sequence); err != nil {
			return errors.New("cannot acknowledge GitHub pending head")
		}
		trigger.WriteDiagnostic(t.log, map[string]string{"event": "github_message_accepted", "repository": name, "messageId": result.Receipt.MessageID, "status": result.Receipt.Status})
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
	Notifications    githubStreamStatus                `json:"notifications"`
	Repositories     map[string]githubRepositoryStatus `json:"repositories"`
}

type githubRepositoryStatus struct {
	Streams           map[string]githubStreamStatus `json:"streams"`
	PendingReadbacks  int                           `json:"pendingReadbacks"`
	Backlog           int                           `json:"backlog"`
	Accepted          uint64                        `json:"accepted"`
	Duplicates        uint64                        `json:"duplicates"`
	LastReceipt       coreclient.Receipt            `json:"lastReceipt,omitzero"`
	LastDeliveryError string                        `json:"lastDeliveryError,omitempty"`
	NextDeliveryAt    time.Time                     `json:"nextDeliveryAt,omitzero"`
}

type githubStreamStatus struct {
	State         string            `json:"state"`
	Items         int               `json:"items"`
	LastSuccessAt time.Time         `json:"lastSuccessAt,omitzero"`
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
	stream, err := readGitHubStream(tx.QueryRowContext(ctx, `SELECT `+githubStreamColumns+`
		FROM github_streams WHERE repository = ? AND scope = ?`, githubInboxRepository, githubInboxScope))
	if err != nil {
		return githubStatus{}, err
	}
	s := githubStatus{
		Source: githubSource, AccountID: t.config.AccountID, InstanceID: t.config.InstanceID, RateLimitedUntil: pause,
		Notifications: githubStreamStatusFor(stream), Repositories: make(map[string]githubRepositoryStatus),
	}
	names, err := githubRepositoryNames(tx, false)
	if err != nil {
		return githubStatus{}, err
	}
	for _, name := range names {
		out, err := readGitHubRepositoryStatus(tx, name)
		if err != nil {
			return githubStatus{}, err
		}
		s.Repositories[name] = out
	}
	return s, nil
}

func githubStreamStatusFor(v githubStreamState) githubStreamStatus {
	state := "ready"
	if !v.Active {
		state = "inactive"
	} else if v.LastError != "" {
		state = "stale"
	} else if v.LastSuccessAt.IsZero() {
		state = "starting"
	}
	return githubStreamStatus{State: state, Items: v.ItemCount, LastSuccessAt: v.LastSuccessAt, NextAttemptAt: v.NextAttemptAt, LastError: v.LastError}
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
	trigger.WriteDiagnostic(json.NewEncoder(w), status)
}

func Status(ctx context.Context, endpoint string, output io.Writer) error {
	if err := trigger.ValidateStatusEndpoint(endpoint); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/status", http.NoBody)
	if err != nil {
		return errors.New("invalid GitHub status request")
	}
	response, err := trigger.HTTPClient(3 * time.Second).Do(req)
	if err != nil {
		return errors.New("GitHub status unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub status HTTP %d", response.StatusCode)
	}
	var s githubStatus
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&s); err != nil || s.Source != githubSource || s.Repositories == nil || s.Notifications.State == "" {
		return errors.New("invalid GitHub status response")
	}
	return json.NewEncoder(output).Encode(s)
}
