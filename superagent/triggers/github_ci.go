package triggers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	githubCIPrefix      = "ci/"
	githubCISnapshotKey = "snapshot"
)

type githubCISnapshot struct {
	Head  githubCIRevision  `json:"head"`
	Merge *githubCIRevision `json:"testMerge,omitempty"`
}

type githubCIRevision struct {
	SHA                 string           `json:"sha"`
	Checks              []githubCISignal `json:"checks"`
	Statuses            []githubCISignal `json:"statuses"`
	Actions             []githubCISignal `json:"actions"`
	UnassociatedActions int              `json:"unassociatedActions,omitempty"`
}

type githubCISignal struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion,omitempty"`
	URL        string `json:"url,omitempty"`
	Provider   string `json:"provider,omitempty"`
	Attempt    int    `json:"attempt,omitempty"`
	Event      string `json:"event,omitempty"`
}

func (s githubCISnapshot) matches(pull githubPullRecord) bool {
	if s.Head.SHA != pull.HeadSHA {
		return false
	}
	if pull.MergeSHA == "" || pull.MergeSHA == pull.HeadSHA {
		return s.Merge == nil
	}
	return s.Merge != nil && s.Merge.SHA == pull.MergeSHA
}

func scheduleGitHubCI(tx *sql.Tx, repo, number string, enabled time.Time, active bool) error {
	if !active {
		_, err := tx.ExecContext(context.Background(), `UPDATE github_streams SET active = 0
			WHERE repository = ? AND scope = ? AND active = 1`, repo, githubCIPrefix+number)
		return err
	}
	_, err := tx.ExecContext(context.Background(), `INSERT INTO github_streams (repository, scope, kind, active, enabled_at)
		VALUES (?, ?, 'ci', 1, ?) ON CONFLICT (repository, scope) DO UPDATE SET active = 1 WHERE active = 0`,
		repo, githubCIPrefix+number, githubTimeValue(enabled))
	return err
}

func (t *githubTrigger) pollNextGitHubCI(ctx context.Context, repo githubRepository) error {
	now := time.Now()
	if ctx.Err() != nil || now.Before(t.api.pauseUntil) {
		return nil //nolint:nilerr // Cancellation ends polling normally.
	}
	next, err := t.store.nextScope(repo.Repository, "ci", false, now, t.matchKey)
	if err != nil {
		return errors.New("cannot read GitHub CI schedule")
	}
	if next == "" {
		return nil
	}
	var raw []byte
	if err := t.store.db.QueryRowContext(context.Background(), `SELECT data FROM github_snapshots
		WHERE repository = ? AND scope = ? AND object_key = ?`,
		repo.Repository, githubPullsScope, strings.TrimPrefix(next, githubCIPrefix)).Scan(&raw); err != nil {
		return errors.New("cannot read GitHub CI pull snapshot")
	}
	target, err := githubPullFromSnapshot(raw)
	if err != nil {
		return err
	}
	scope := githubScope{name: next, poll: func(ctx context.Context, api *githubAPI, repo string, stream *githubStreamState, _ time.Time) ([]githubObservation, error) {
		return pollGitHubCI(ctx, api, repo, target, stream)
	}}
	return t.poll(ctx, repo, scope)
}

func githubPullFromSnapshot(data []byte) (githubPullRecord, error) {
	var pull githubPullRecord
	if json.Unmarshal(data, &pull) != nil || pull.Number <= 0 {
		return pull, errors.New("invalid saved GitHub pull scope")
	}
	return pull, nil
}

func pollGitHubCI(ctx context.Context, api *githubAPI, repo string, pull githubPullRecord, scope *githubStreamState) ([]githubObservation, error) {
	previous, known := scope.Items[githubCISnapshotKey]
	var old githubCISnapshot
	if known && json.Unmarshal(previous.Data, &old) != nil {
		return nil, errors.New("invalid saved GitHub CI snapshot")
	}
	head, err := readGitHubCIRevision(ctx, api, repo, pull.HeadSHA, pull)
	if err != nil {
		return nil, err
	}
	snapshot := githubCISnapshot{Head: head}
	if pull.MergeSHA != "" && pull.MergeSHA != pull.HeadSHA {
		merge, err := readGitHubCIRevision(ctx, api, repo, pull.MergeSHA, pull)
		if err != nil {
			return nil, err
		}
		snapshot.Merge = &merge
	}
	data, _ := json.Marshal(snapshot) //nolint:errchkjson // Snapshot contains only typed CI fields.
	if known && string(data) == string(previous.Data) {
		return nil, nil
	}
	kind := ""
	if known && snapshot.hasChanges(old) {
		kind = "ci_changed"
	}
	at := time.Now().UTC()
	prURL := githubObjectURL(repo, "pull", pull.Number)
	return []githubObservation{{
		Key: githubCISnapshotKey, Data: data, Kind: kind, UpdatedAt: at, At: at,
		URL: prURL + "/checks", Thread: ThreadRef{githubPRThreadSource, prURL},
		Title: fmt.Sprintf("GitHub CI changed for %s#%d\n%s/checks", repo, pull.Number, prURL),
		Body:  snapshot.summary(pull.HeadRepo),
		Meta: map[string]any{
			"objectType": "pull_request_ci", "objectId": strconv.FormatInt(pull.ID, 10), "number": pull.Number,
			"headRepository": pull.HeadRepo, "ci": snapshot,
		},
	}}, nil
}

func (s githubCISnapshot) hasChanges(old githubCISnapshot) bool {
	if s.Head.hasChanges(old.Head) || (s.Merge == nil) != (old.Merge == nil) {
		return true
	}
	return s.Merge != nil && s.Merge.hasChanges(*old.Merge)
}

func (r githubCIRevision) hasChanges(old githubCIRevision) bool {
	return r.SHA != old.SHA || !slices.Equal(r.Checks, old.Checks) || !slices.Equal(r.Statuses, old.Statuses) ||
		!slices.Equal(r.Actions, old.Actions)
}

func (s githubCISnapshot) summary(headRepo string) string {
	var b strings.Builder
	s.Head.writeSummary(&b, "Head "+headRepo)
	if s.Merge != nil {
		b.WriteString("\n")
		s.Merge.writeSummary(&b, "Test merge")
	}
	return b.String()
}

func (r githubCIRevision) writeSummary(b *strings.Builder, title string) {
	fmt.Fprintf(b, "%s SHA %s\n", title, r.SHA)
	if len(r.Checks)+len(r.Statuses) == 0 {
		b.WriteString("No Check Runs or Commit Statuses observed.\n")
	}
	writeSignals := func(source string, signals []githubCISignal) {
		for _, s := range signals {
			fmt.Fprintf(b, "- %s %s [%s]: %s", source, s.Name, s.ID, s.Status)
			if s.Conclusion != "" {
				fmt.Fprintf(b, " / %s", s.Conclusion)
			}
			if s.Attempt > 0 {
				fmt.Fprintf(b, " (attempt %d)", s.Attempt)
			}
			if s.URL != "" {
				fmt.Fprintf(b, " %s", s.URL)
			}
			b.WriteByte('\n')
		}
	}
	writeSignals("Check Run", r.Checks)
	writeSignals("Commit Status", r.Statuses)
	if r.UnassociatedActions > 0 {
		fmt.Fprintf(b, "Workflow Run association unknown for %d result(s).\n", r.UnassociatedActions)
	}
	writeSignals("Workflow Run", r.Actions)
}

func githubCIStatusFor(stream githubStreamState, pullData, snapshotData []byte) githubStreamStatus {
	status := githubStreamStatusFor(stream)
	pull, err := githubPullFromSnapshot(pullData)
	if err != nil {
		status.State, status.LastError = "stale", err.Error()
		return status
	}
	status.HeadSHA = pull.HeadSHA
	status.TestMergeSHA = pull.MergeSHA
	if !stream.Active {
		status.State = "inactive"
	}
	var snapshot githubCISnapshot
	if len(snapshotData) != 0 && json.Unmarshal(snapshotData, &snapshot) == nil {
		if stream.Active && !snapshot.matches(pull) {
			status.State = "stale"
		}
		status.CI = &snapshot
	}
	return status
}
