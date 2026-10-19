// Package client implements the MASS HTTP JSON-RPC and live-only SSE contract.
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxResponse = 64 << 20

type Client struct {
	endpoint string
	http     *http.Client
}

// RPCError retains structured diagnostics such as operationId and acceptance evidence.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if len(e.Data) > 0 {
		return fmt.Sprintf("RPC %d: %s (data: %s)", e.Code, e.Message, e.Data)
	}
	return fmt.Sprintf("RPC %d: %s", e.Code, e.Message)
}

func New(endpoint string, timeout time.Duration) (*Client, error) {
	if !strings.Contains(endpoint, "://") {
		endpoint = "http://" + endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("endpoint must be an HTTP(S) base URL or JSON-RPC URL without credentials or fragment")
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/api/v1/agentd/jsonrpc"
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = timeout
	return &Client{endpoint: u.String(), http: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) Close() { c.http.CloseIdleConnections() }

func (c *Client) request(ctx context.Context, method string, params any) (*http.Response, error) {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "massctl", "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	return resp, nil
}

func decodeResponse(body io.Reader) (json.RawMessage, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxResponse+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxResponse {
		return nil, errors.New("RPC response exceeds 64 MiB")
	}
	var response struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      string          `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   *RPCError       `json:"error"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decode RPC response: %w", err)
	}
	if response.JSONRPC != "2.0" || response.ID != "massctl" || (response.Error == nil) == (response.Result == nil) {
		return nil, errors.New("invalid JSON-RPC response envelope")
	}
	if response.Error != nil {
		return nil, response.Error
	}
	return response.Result, nil
}

func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	resp, err := c.request(ctx, method, params)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return decodeResponse(resp.Body)
}

// Watch does not replay history, reconnect or retry accepted operations.
// The context, rather than the unary timeout, controls stream lifetime.
func (c *Client) Watch(ctx context.Context, name string, emit func(json.RawMessage) error) error {
	resp, err := c.request(ctx, "agentd/session/watch_event", map[string]string{"sessionName": name})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if media != "text/event-stream" {
		_, err := decodeResponse(resp.Body)
		if err != nil {
			return err
		}
		return errors.New("expected text/event-stream")
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), maxResponse)
	var data []byte
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if len(data) > 0 {
				if !json.Valid(data) {
					return errors.New("invalid JSON in SSE event")
				}
				if err := emit(json.RawMessage(data)); err != nil {
					return err
				}
				data = nil
			}
			continue
		}
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			value = strings.TrimPrefix(value, " ")
			if len(data)+len(value)+1 > maxResponse {
				return errors.New("SSE event exceeds 64 MiB")
			}
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, value...)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if len(data) > 0 {
		return errors.New("SSE stream ended with an incomplete event")
	}
	return nil
}
