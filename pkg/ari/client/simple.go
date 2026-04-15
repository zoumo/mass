// Package client implements the Agent Runtime Interface (ARI) JSON-RPC client.
// This client provides a simplified interface for single-shot RPC calls
// to the ARI server over a Unix domain socket.
package client

import (
	"context"
	"fmt"

	"github.com/zoumo/mass/pkg/jsonrpc"
)

// Client is a simplified JSON-RPC client for ARI socket communication.
// It provides single-shot RPC calls without event handling.
type Client struct {
	c *jsonrpc.Client
}

// NewClient connects to the ARI server over a Unix domain socket.
// Returns an error if the socket file is missing or the connection fails.
func NewClient(socketPath string) (*Client, error) {
	c, err := jsonrpc.Dial(context.Background(), "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", socketPath, err)
	}
	return &Client{c: c}, nil
}

// Call sends a JSON-RPC request to the ARI server and unmarshals the response.
// This is a blocking call that waits for the server response.
// Returns an error if the request fails, the response is malformed,
// or the server returns an RPC error.
func (c *Client) Call(method string, params, result any) error {
	return c.c.Call(context.Background(), method, params, result)
}

// Close closes the connection to the ARI server.
func (c *Client) Close() error {
	return c.c.Close()
}
