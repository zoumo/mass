package triggers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/shurcooL/githubv4"
)

const githubGraphQLEndpoint = "https://api.github.com/graphql"

var errGitHubRateLimited = errors.New("github_rate_limited")

type githubAPI struct {
	client     *githubv4.Client
	pauseUntil time.Time
}

type githubHTTPError struct{ status int }

func (e githubHTTPError) Error() string { return fmt.Sprintf("github_http_%d", e.status) }

func newGitHubAPI(endpoint, token string, timeout time.Duration) *githubAPI {
	api := new(githubAPI)
	client := resty.New().SetTimeout(timeout).GetClient()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client.Transport = &githubTransport{base: client.Transport, token: token, api: api}
	api.client = githubv4.NewEnterpriseClient(endpoint, client)
	return api
}

type githubTransport struct {
	base  http.RoundTripper
	token string
	api   *githubAPI
}

// Capture status and rate limits before githubv4 reduces GraphQL errors to
// messages. Reject partial data and never return upstream response text.
func (t *githubTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer "+t.token)
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, errors.New("github_request_failed")
	}
	defer response.Body.Close()
	now := time.Now().UTC()
	t.api.applyRateLimit(response.StatusCode, response.Header, now)
	const maximumResponse = 32 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumResponse+1))
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusForbidden && bytes.Contains(bytes.ToLower(body), []byte("secondary rate limit")) && !t.api.pauseUntil.After(now) {
			t.api.pauseUntil = now.Add(time.Minute)
		}
		return nil, githubHTTPError{response.StatusCode}
	}
	if err != nil || len(body) > maximumResponse {
		return nil, errors.New("github_invalid_response")
	}
	var envelope struct {
		Data   *json.RawMessage `json:"data"`
		Errors []struct {
			Type       string `json:"type"`
			Message    string `json:"message"`
			Extensions struct {
				Code string `json:"code"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return nil, errors.New("github_invalid_response")
	}
	if len(envelope.Errors) > 0 {
		for _, failure := range envelope.Errors {
			message := strings.ToLower(failure.Message)
			if failure.Type == "RATE_LIMITED" || failure.Extensions.Code == "RATE_LIMITED" || strings.Contains(message, "rate limit") {
				if !t.api.pauseUntil.After(now) {
					t.api.pauseUntil = now.Add(time.Minute)
				}
				return nil, errGitHubRateLimited
			}
		}
		return nil, errors.New("github_graphql_error")
	}
	if envelope.Data == nil {
		return nil, errors.New("github_invalid_response")
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	return response, nil
}

func (a *githubAPI) applyRateLimit(status int, headers http.Header, now time.Time) {
	var until time.Time
	if retry, err := strconv.ParseInt(headers.Get("Retry-After"), 10, 32); err == nil && retry >= 0 {
		until = now.Add(time.Duration(max(1, retry)) * time.Second)
	} else if retry, err := http.ParseTime(headers.Get("Retry-After")); err == nil {
		until = retry
	}
	if headers.Get("X-RateLimit-Remaining") == "0" {
		reset := now.Add(time.Minute)
		if epoch, err := strconv.ParseInt(headers.Get("X-RateLimit-Reset"), 10, 64); err == nil && time.Unix(epoch, 0).After(now) {
			reset = time.Unix(epoch, 0)
		}
		if reset.After(until) {
			until = reset
		}
	}
	if status == http.StatusTooManyRequests && !until.After(now) {
		until = now.Add(time.Minute)
	}
	if until.After(a.pauseUntil) {
		a.pauseUntil = until.UTC()
	}
}

func (a *githubAPI) query(ctx context.Context, query any, variables map[string]any) error {
	if time.Now().Before(a.pauseUntil) {
		return errGitHubRateLimited
	}
	err := a.client.Query(ctx, query, variables)
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var status githubHTTPError
	var wrapped *url.Error
	if errors.As(err, &status) {
		return status
	}
	if errors.Is(err, errGitHubRateLimited) {
		return errGitHubRateLimited
	}
	if errors.As(err, &wrapped) {
		// This error originated in the transport and has been sanitized.
		switch wrapped.Err.Error() {
		case "github_request_failed", "github_invalid_response", "github_graphql_error":
			return wrapped.Err
		default:
			return errors.New("github_request_failed")
		}
	}
	return errors.New("github_invalid_response")
}

// GitHub encodes its fullDatabaseId BigInt scalar as a decimal JSON string.
type githubDatabaseID int64

func (id *githubDatabaseID) UnmarshalJSON(raw []byte) error {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if value == "" {
		*id = 0
		return nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	*id = githubDatabaseID(parsed)
	return err
}

func (id githubDatabaseID) MarshalJSON() ([]byte, error) {
	return json.Marshal(strconv.FormatInt(int64(id), 10))
}

type githubPageInfo struct {
	HasNextPage *bool
	EndCursor   *githubv4.String
}

type githubConnection[T any] struct {
	Nodes    []T
	PageInfo githubPageInfo
}

var errGitHubStopPagination = errors.New("reached GitHub polling window")

func githubPages[T any](read func(*githubv4.String) (githubConnection[T], error), consume func([]T) error) error {
	var cursor *githubv4.String
	seen := make(map[githubv4.String]bool)
	for {
		page, err := read(cursor)
		if err != nil {
			return err
		}
		info := page.PageInfo
		if page.Nodes == nil || info.HasNextPage == nil {
			return errors.New("github_invalid_collection")
		}
		if *info.HasNextPage && (info.EndCursor == nil || *info.EndCursor == "" || seen[*info.EndCursor]) {
			return errors.New("github_invalid_pagination_cursor")
		}
		if err := consume(page.Nodes); err != nil {
			if errors.Is(err, errGitHubStopPagination) {
				return nil
			}
			return err
		}
		if !*info.HasNextPage {
			return nil
		}
		cursor = info.EndCursor
		seen[*cursor] = true
	}
}

func githubReadConnection[T any](read func(*githubv4.String) (githubConnection[T], error)) ([]T, error) {
	var results []T
	err := githubPages(read, func(nodes []T) error {
		results = append(results, nodes...)
		return nil
	})
	return results, err
}

func githubRepositoryVariables(repo string) map[string]any {
	owner, name, _ := strings.Cut(repo, "/")
	return map[string]any{"owner": githubv4.String(owner), "name": githubv4.String(name)}
}
