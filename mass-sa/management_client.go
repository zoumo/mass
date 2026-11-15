package sa

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxManagementResponseBytes = 8 << 20

// ManagementClient is the CLI-facing authenticated management API client.
type ManagementClient struct {
	endpoint string
	token    string
	http     *http.Client
}

// NewManagementClient resolves the configured endpoint and token.
func NewManagementClient(config Config) (*ManagementClient, error) {
	token, err := config.ResolveManagementToken()
	if err != nil {
		return nil, err
	}
	return NewManagementClientForEndpoint(config.ManagementEndpoint(), token)
}

// NewManagementClientForEndpoint binds an explicit endpoint and token.
func NewManagementClientForEndpoint(endpoint, token string) (*ManagementClient, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || strings.TrimSpace(token) == "" {
		return nil, operationError("invalid_argument", "invalid_management_endpoint", "management endpoint and token are required")
	}
	return &ManagementClient{
		endpoint: strings.TrimRight(parsed.String(), "/"),
		token:    strings.TrimSpace(token),
		http:     &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// Health checks authenticated management readiness.
func (c *ManagementClient) Health(ctx context.Context) error {
	var result map[string]string
	return c.Request(ctx, http.MethodGet, "/health", nil, &result)
}

// SubmitEvent accepts one external event.
func (c *ManagementClient) SubmitEvent(ctx context.Context, req AcceptEventRequest) (AcceptedEvent, error) {
	var result AcceptedEvent
	err := c.Request(ctx, http.MethodPost, "/events", req, &result)
	return result, err
}

// InspectTicket reads the complete root delivery and worker state.
func (c *ManagementClient) InspectTicket(ctx context.Context, projectID, rootTicketID string) (TicketInspection, error) {
	var result TicketInspection
	path := "/tickets/" + url.PathEscape(rootTicketID) + "/inspect" + "?projectId=" + url.QueryEscape(projectID)
	err := c.Request(ctx, http.MethodGet, path, nil, &result)
	return result, err
}

// ListWorkers reads workers in one root Ticket scope.
func (c *ManagementClient) ListWorkers(ctx context.Context, projectID, rootTicketID string) (ListWorkersResult, error) {
	var result ListWorkersResult
	path := "/workers?projectId=" + url.QueryEscape(projectID) + "&rootTicketId=" + url.QueryEscape(rootTicketID)
	err := c.Request(ctx, http.MethodGet, path, nil, &result)
	return result, err
}

// BindRoute creates or replaces one route binding.
func (c *ManagementClient) BindRoute(ctx context.Context, req BindEventRouteRequest) (RouteBindingView, error) {
	var result RouteBindingView
	err := c.Request(ctx, http.MethodPost, "/routes", req, &result)
	return result, err
}

// RevokeRoute removes one route binding and reads back the removed identity.
func (c *ManagementClient) RevokeRoute(ctx context.Context, req BindEventRouteRequest) (RouteBindingView, error) {
	var result RouteBindingView
	body := map[string]string{
		"projectId":    req.ProjectID,
		"rootTicketId": req.RootTicketID,
		"routeKey":     req.RouteKey,
	}
	err := c.Request(ctx, http.MethodPost, "/routes/revoke", body, &result)
	return result, err
}

// ListRoutes reads every current route binding for one root Ticket.
func (c *ManagementClient) ListRoutes(ctx context.Context, projectID, rootTicketID string) ([]RouteBindingView, error) {
	var result struct {
		RouteBindings []RouteBindingView `json:"routeBindings"`
	}
	path := "/routes?projectId=" + url.QueryEscape(projectID) + "&rootTicketId=" + url.QueryEscape(rootTicketID)
	err := c.Request(ctx, http.MethodGet, path, nil, &result)
	return result.RouteBindings, err
}

func (c *ManagementClient) Request(ctx context.Context, method, path string, body, target any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return operationError("invalid_argument", "invalid_management_body", "management request cannot be encoded")
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, reader)
	if err != nil {
		return operationError("invalid_argument", "invalid_management_request", "management request cannot be created")
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return operationError("unavailable", "management_unavailable", "management API is unavailable")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxManagementResponseBytes+1))
	if err != nil || len(raw) > maxManagementResponseBytes {
		return operationError("unavailable", "management_response_invalid", "management API response is unavailable or too large")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope struct {
			Error *OperationError `json:"error"`
		}
		if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Error != nil {
			return envelope.Error
		}
		return operationError("unavailable", "management_request_failed", fmt.Sprintf("management API returned HTTP %d", response.StatusCode))
	}
	if target == nil {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return operationError("unavailable", "management_response_invalid", "management API response is invalid")
	}
	return nil
}
