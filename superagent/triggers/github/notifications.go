package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	githubInboxRepository  = "@account"
	githubInboxScope       = "notifications"
	githubNoticePrefix     = "notification/"
	githubNoticeKey        = "notification"
	githubNotificationPage = 50
	githubOverlap          = 2 * time.Minute
)

type githubNotification struct {
	ID         string    `json:"id"`
	Reason     string    `json:"reason"`
	UpdatedAt  time.Time `json:"updated_at"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Subject struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"subject"`
}

func (a *githubAPI) notificationPage(ctx context.Context, since time.Time, page, perPage int) ([]githubNotification, time.Duration, int, error) {
	if time.Now().Before(a.pauseUntil) {
		return nil, 0, 0, errGitHubRateLimited
	}
	query := url.Values{"all": {"true"}, "per_page": {strconv.Itoa(perPage)}, "page": {strconv.Itoa(page)}}
	if !since.IsZero() {
		query.Set("since", since.UTC().Format(time.RFC3339))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.origin+"/notifications?"+query.Encode(), http.NoBody)
	if err != nil {
		return nil, 0, 0, errors.New("github_invalid_notifications_request")
	}
	res, err := a.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, 0, ctx.Err()
		}
		return nil, 0, 0, errors.New("github_notifications_request_failed")
	}
	defer res.Body.Close()
	seconds, _ := strconv.ParseUint(res.Header.Get("X-Poll-Interval"), 10, 32) // Invalid or missing values leave the configured interval in effect.
	interval := time.Duration(seconds) * time.Second
	if res.StatusCode != http.StatusOK {
		return nil, interval, 0, githubHTTPError{res.StatusCode}
	}
	var items []githubNotification
	if json.NewDecoder(res.Body).Decode(&items) != nil || items == nil {
		return nil, interval, 0, errors.New("github_invalid_notifications_response")
	}
	next, err := a.nextNotificationPage(res.Header.Values("Link"), page)
	return items, interval, next, err
}

func (a *githubAPI) nextNotificationPage(links []string, page int) (int, error) {
	for _, header := range links {
		for link := range strings.SplitSeq(header, ",") {
			target, attributes, ok := strings.Cut(strings.TrimSpace(link), ">")
			if !strings.Contains(attributes, `rel="next"`) {
				continue
			}
			// GitHub documents Link pagination. Rebuild the next request with
			// the same account filters, after checking its origin and page.
			base := a.origin + "/notifications?"
			if !ok || !strings.HasPrefix(target, "<"+base) {
				return 0, errors.New("github_invalid_notifications_pagination")
			}
			params, err := url.ParseQuery(strings.TrimPrefix(target, "<"+base))
			next, numberErr := strconv.Atoi(params.Get("page"))
			if err != nil || numberErr != nil || next != page+1 {
				return 0, errors.New("github_invalid_notifications_pagination")
			}
			return next, nil
		}
	}
	return 0, nil
}

type githubNotice struct {
	ID        string    `json:"id"`
	Reason    string    `json:"reason"`
	UpdatedAt time.Time `json:"updatedAt"`
	Number    int       `json:"number"`
	Pull      bool      `json:"pull,omitempty"`
}

func (n githubNotification) notice(repo string) (githubNotice, bool) {
	pull := n.Subject.Type == "PullRequest"
	if !pull && n.Subject.Type != "Issue" || n.UpdatedAt.IsZero() {
		return githubNotice{}, false
	}
	if id, err := strconv.ParseUint(n.ID, 10, 64); err != nil || id == 0 {
		return githubNotice{}, false
	}
	collection := "issues"
	if pull {
		collection = "pulls"
	}
	u, err := url.Parse(n.Subject.URL)
	if err != nil || u.Scheme != "https" || u.Host != "api.github.com" || u.User != nil {
		return githubNotice{}, false
	}
	number, ok := strings.CutPrefix(strings.ToLower(u.Path), "/repos/"+repo+"/"+collection+"/")
	native, err := strconv.Atoi(number)
	if !ok || err != nil || native <= 0 {
		return githubNotice{}, false
	}
	return githubNotice{ID: n.ID, Reason: n.Reason, UpdatedAt: n.UpdatedAt, Number: native, Pull: pull}, true
}

func (t *githubTrigger) pollNotifications(ctx context.Context) error {
	stream, err := t.store.stream(githubInboxRepository, githubInboxScope)
	if err != nil {
		return errors.New("cannot read GitHub Notifications state")
	}
	started := time.Now().UTC()
	if ctx.Err() != nil || started.Before(stream.NextAttemptAt) || started.Before(t.api.pauseUntil) {
		return nil //nolint:nilerr // Cancellation ends polling normally.
	}
	// Replay an overlapping window from before the last complete poll.
	// A failed page leaves this checkpoint unchanged.
	checkpoint := stream.LastSuccessAt
	if checkpoint.IsZero() {
		checkpoint = stream.EnabledAt
	}
	since := checkpoint.Add(-githubOverlap).Truncate(time.Second)
	interval := t.interval
	var pollErr error
	for page := 1; page != 0; {
		var items []githubNotification
		var providerInterval time.Duration
		items, providerInterval, page, pollErr = t.api.notificationPage(ctx, since, page, githubNotificationPage)
		interval = max(interval, providerInterval)
		if pollErr != nil {
			break
		}
		if err := t.store.acceptNotifications(t.config, stream.EnabledAt, items, t.api.pauseUntil); err != nil {
			return errors.New("cannot persist GitHub Notifications page")
		}
	}
	return t.finishPoll(ctx, githubInboxRepository, githubInboxScope, stream, started, interval, pollErr)
}
