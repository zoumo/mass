package coreclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/zoumo/mass/superagent/triggers/internal/trigger"
)

const TriggerMessagesPath = "/v1/core/trigger-messages"

type Client struct {
	endpoint, token string
	http            *http.Client
}
type Receipt struct {
	Status     string `json:"status"`
	Duplicate  bool   `json:"duplicate"`
	MessageID  string `json:"messageId"`
	ThreadID   string `json:"threadId"`
	AcceptedAt string `json:"acceptedAt"`
	EventID    string `json:"eventId"`
}
type SubmitResult struct {
	Receipt Receipt
	Code    string
	Retry   bool
}

func New(endpoint string, timeout time.Duration, token string) (*Client, error) {
	if len(token) < 32 || strings.ContainsAny(token, " \t\r\n") {
		return nil, errors.New("the Core credential reference must resolve to at least 32 non-whitespace bytes")
	}
	return &Client{strings.TrimRight(endpoint, "/"), token, trigger.HTTPClient(timeout)}, nil
}

func (c *Client) SubmitPayload(ctx context.Context, payload []byte) SubmitResult {
	data, code, retry := c.Request(ctx, http.MethodPost, TriggerMessagesPath, bytes.NewReader(payload), 65536)
	if code != "" {
		return SubmitResult{Code: code, Retry: retry}
	}
	var receipt Receipt
	if json.Unmarshal(data, &receipt) != nil || (receipt.Status != "accepted" && receipt.Status != "duplicate") || receipt.MessageID == "" || receipt.ThreadID == "" || receipt.AcceptedAt == "" {
		return SubmitResult{Code: "core_receipt_unknown", Retry: true}
	}
	return SubmitResult{Receipt: receipt}
}

func (c *Client) Patch(ctx context.Context, path string, payload any) (data []byte, code string, retry bool) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, "invalid_message", false
	}
	return c.Request(ctx, http.MethodPatch, path, bytes.NewReader(b), 65536)
}

func (c *Client) Request(ctx context.Context, method, path string, body io.Reader, limit int64) (data []byte, code string, retry bool) {
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, body)
	if err != nil {
		return nil, "invalid_request", false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, "core_unreachable_or_outcome_unknown", true
	}
	defer res.Body.Close()
	data, err = io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, "core_response_unknown", true
	}
	if res.StatusCode == http.StatusOK {
		return data, "", false
	}
	var failure struct {
		Error struct {
			Code      string `json:"code"`
			Retryable bool   `json:"retryable"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &failure) != nil || failure.Error.Code == "" {
		return nil, "core_response_unknown", true
	}
	// Only expose reviewed codes, never upstream text, request bodies or credentials.
	switch failure.Error.Code {
	case "invalid_argument", "unauthorized", "forbidden", "not_found", "conflict", "stale_binding", "failed_precondition", "unsupported", "unavailable", "outcome_unknown":
	default:
		return nil, "core_response_unknown", true
	}
	return nil, failure.Error.Code, failure.Error.Retryable || failure.Error.Code == "outcome_unknown"
}
