package watchdog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zoumo/mass/internal/httpaddress"
	"github.com/zoumo/mass/superagent/pkg/core"
)

const (
	resourcePrefix = "/v1/core/"
	maxBodyBytes   = 8 << 20
	maxTextBytes   = 1500
)

type reportView struct {
	core.Report
	Current bool `json:"current"`
}

type eventView struct {
	ID        string    `json:"event_id"`
	Kind      string    `json:"kind"`
	Status    string    `json:"status"`
	SentAt    time.Time `json:"sentAt"`
	Recipient struct {
		Kind    string `json:"kind"`
		TopicID string `json:"topic_id"`
	} `json:"recipient"`
	Refs []struct {
		Kind         string `json:"kind"`
		ID           string `json:"id"`
		IssueID      string `json:"issue_id"`
		GoalRevision int    `json:"goal_revision"`
	} `json:"refs"`
}

type activityView struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	At       string `json:"at"`
	IssueID  string `json:"issueId,omitempty"`
	ReportID string `json:"reportId,omitempty"`
	EventID  string `json:"eventId,omitempty"`
	Status   string `json:"status,omitempty"`
	Title    string `json:"title,omitempty"`
	Summary  string `json:"summary,omitempty"`
}

type evidence struct {
	Delivery      Delivery          `json:"delivery"`
	ObservedAt    time.Time         `json:"observedAt"`
	Issue         core.IssueSummary `json:"issue"`
	Report        core.Report       `json:"report"`
	Notification  eventView         `json:"notification"`
	TopicActivity []activityView    `json:"recentTopicActivity"`
}

type page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"nextCursor"`
}

type reader struct {
	endpoint *url.URL
	client   *http.Client
}

func newReader(endpoint string) (reader, error) {
	u, err := httpaddress.ParseOrigin(endpoint)
	if err != nil {
		return reader{}, fmt.Errorf("endpoint %w", err)
	}
	return reader{endpoint: u, client: &http.Client{
		Transport: &http.Transport{},
		Timeout:   10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}, nil
}

func (r reader) read(ctx context.Context, path string, query url.Values, out any) error {
	target := r.endpoint.String() + resourcePrefix + path
	if len(query) != 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("resource read returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxBodyBytes || !bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		return errors.New("response exceeds limit or is not a JSON object")
	}
	return json.Unmarshal(data, out)
}

func (r reader) collect(ctx context.Context, observed time.Time) (*evidence, error) {
	query := url.Values{"limit": {"50"}, "preview": {"true"}}
	var candidates []candidate
	seen := make(map[string]bool)
	for {
		var reports page[reportView]
		if err := r.read(ctx, "reports", query, &reports); err != nil {
			return nil, err
		}
		if reports.Items == nil {
			return nil, errors.New("reports page is missing items")
		}
		for _, report := range reports.Items {
			if !report.eligible() {
				continue
			}
			notification, err := r.sentNotification(ctx, report, observed)
			if err != nil {
				return nil, err
			}
			if notification == nil {
				continue
			}
			delivery := Delivery{
				IssueID: report.IssueID, TopicID: report.TopicID, GoalRevision: report.Revision,
				ReportID: report.ID, EventID: notification.ID, SentAt: notification.SentAt.UTC(),
			}
			candidates = append(candidates, candidate{delivery: delivery, notification: *notification})
		}
		if reports.NextCursor == "" {
			break
		}
		if seen[reports.NextCursor] {
			return nil, errors.New("reports page repeated a cursor")
		}
		seen[reports.NextCursor] = true
		query.Set("cursor", reports.NextCursor)
	}
	sort.Slice(candidates, func(i, j int) bool { return earlier(candidates[i].delivery, candidates[j].delivery) })
	for _, candidate := range candidates {
		selected, err := r.collectCandidate(ctx, observed, candidate)
		if selected != nil || err != nil {
			return selected, err
		}
	}
	return nil, nil
}

type candidate struct {
	delivery     Delivery
	notification eventView
}

func (r reader) collectCandidate(ctx context.Context, observed time.Time, candidate candidate) (*evidence, error) {
	delivery := candidate.delivery
	var report reportView
	if err := r.read(ctx, "reports/"+url.PathEscape(delivery.ReportID), nil, &report); err != nil {
		return nil, err
	}
	if !report.eligible() || report.ID != delivery.ReportID || report.EventID != delivery.EventID ||
		report.IssueID != delivery.IssueID || report.TopicID != delivery.TopicID || report.Revision != delivery.GoalRevision {
		return nil, nil
	}
	var issue core.IssueSummary
	issuePath := "issues/" + url.PathEscape(delivery.IssueID)
	issueQuery := url.Values{"view": {"summary"}}
	if err := r.read(ctx, issuePath, issueQuery, &issue); err != nil {
		return nil, err
	}
	if !issueMatches(issue, report) {
		return nil, nil
	}
	var activity page[activityView]
	if err := r.read(ctx, "topics/"+url.PathEscape(delivery.TopicID)+"/activity", url.Values{"limit": {"10"}}, &activity); err != nil {
		return nil, err
	}
	if activity.Items == nil {
		return nil, errors.New("activity page is missing items")
	}
	// Read acceptance and goal facts again after the activity fetch.
	if err := r.read(ctx, issuePath, issueQuery, &issue); err != nil {
		return nil, err
	}
	if !issueMatches(issue, report) {
		return nil, nil
	}
	selected := &evidence{
		Delivery: delivery, ObservedAt: observed, Issue: issue, Report: report.Report,
		Notification: candidate.notification, TopicActivity: activity.Items[:min(10, len(activity.Items))],
	}
	selected.boundText()
	return selected, nil
}

func (r reader) sentNotification(ctx context.Context, report reportView, observed time.Time) (*eventView, error) {
	var event eventView
	if err := r.read(ctx, "internal-events/"+url.PathEscape(report.EventID), nil, &event); err != nil {
		return nil, err
	}
	if event.ID != report.EventID || event.Kind != "report_submitted" || event.Status != "sent" ||
		event.Recipient.Kind != "topic" || event.Recipient.TopicID != report.TopicID ||
		event.SentAt.IsZero() || event.SentAt.After(observed.Add(-reportAge)) {
		return nil, nil
	}
	for i, ref := range event.Refs {
		if ref.Kind == "report" && ref.ID == report.ID && ref.IssueID == report.IssueID && ref.GoalRevision == report.Revision {
			event.Refs = event.Refs[i : i+1]
			return &event, nil
		}
	}
	return nil, nil
}

func (r reportView) eligible() bool {
	return r.Current && !r.Historical && r.Kind == "ready" && r.Revision > 0 &&
		r.ID != "" && r.IssueID != "" && r.TopicID != "" && r.EventID != "" && r.SessionBindingID != ""
}

func issueMatches(i core.IssueSummary, r reportView) bool {
	return i.ID == r.IssueID && i.TopicID == r.TopicID && i.GoalRevision == r.Revision &&
		i.CurrentSessionBindingID == r.SessionBindingID && i.Status == "active" && !i.Completed && !i.Blocked
}

func earlier(a, b Delivery) bool {
	order := a.SentAt.Compare(b.SentAt)
	return order < 0 || order == 0 && a.ReportID < b.ReportID
}

func (e *evidence) boundText() {
	e.Issue.Title, e.Issue.Goal = excerpt(e.Issue.Title), excerpt(e.Issue.Goal)
	e.Report.Summary, e.Report.Details = excerpt(e.Report.Summary), excerpt(e.Report.Details)
	e.Report.Evidence = boundItems(e.Report.Evidence)
	e.Report.Questions = boundItems(e.Report.Questions)
	for i := range e.TopicActivity {
		entry := &e.TopicActivity[i]
		entry.Title, entry.Summary = excerpt(entry.Title), excerpt(entry.Summary)
	}
}

func boundItems(items []string) []string {
	items = items[:min(5, len(items))]
	for i := range items {
		items[i] = excerpt(items[i])
	}
	return items
}

func excerpt(text string) string {
	if len(text) <= maxTextBytes {
		return text
	}
	end := maxTextBytes
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return strings.TrimSpace(text[:end]) + "…"
}
