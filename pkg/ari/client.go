// Package ari implements the Agent Runtime Interface (ARI) JSON-RPC client.
// This client provides a simplified interface for single-shot RPC calls
// to the ARI server over a Unix domain socket.
package ari

import (
	"encoding/json"
	"fmt"
	"net"
	"sync"
)

// rpcRequest is the JSON-RPC 2.0 request structure.
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int   `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// rpcResponse is the JSON-RPC 2.0 response structure.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is the JSON-RPC 2.0 error structure.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Client is a simplified JSON-RPC client for ARI socket communication.
// It provides single-shot RPC calls without event handling.
type Client struct {
	conn    net.Conn
	encoder *json.Encoder
	decoder *json.Decoder
	mu      sync.Mutex
	nextID  int
}

// NewClient connects to the ARI server over a Unix domain socket.
// Returns an error if the socket file is missing or the connection fails.
func NewClient(socketPath string) (*Client, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", socketPath, err)
	}
	return &Client{
		conn:    conn,
		encoder: json.NewEncoder(conn),
		decoder: json.NewDecoder(conn),
	}, nil
}

// Call sends a JSON-RPC request to the ARI server and unmarshals the response.
// This is a blocking call that waits for the server response.
// Returns an error if the request fails, the response is malformed,
// or the server returns an RPC error.
func (c *Client) Call(method string, params, result any) error {
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	c.mu.Unlock()

	req := rpcRequest{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  method,
		Params:  params,
	}

	if err := c.encoder.Encode(req); err != nil {
		return fmt.Errorf("send request: %w", err)
	}

	var resp rpcResponse
	if err := c.decoder.Decode(&resp); err != nil {
		return fmt.Errorf("receive response: %w", err)
	}

	// Validate response ID matches request ID
	if resp.ID == nil || *resp.ID != id {
		return fmt.Errorf("response ID mismatch: expected %d, got %v", id, resp.ID)
	}

	// Check for RPC error
	if resp.Error != nil {
		return fmt.Errorf("rpc error %d: %s", resp.Error.Code, resp.Error.Message)
	}

	// Unmarshal result if provided
	if result != nil && resp.Result != nil {
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return fmt.Errorf("unmarshal result: %w", err)
		}
	}

	return nil
}

// Close closes the connection to the ARI server.
func (c *Client) Close() error {
	return c.conn.Close()
}
