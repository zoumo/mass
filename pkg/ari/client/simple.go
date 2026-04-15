// Package client implements the Agent Runtime Interface (ARI) JSON-RPC client.
package client

import (
	"context"
	"fmt"

	"github.com/zoumo/mass/pkg/jsonrpc"
)

// RawClient is a simplified JSON-RPC client for ARI socket communication.
// It provides single-shot RPC calls without event handling.
// Use this as an escape hatch when the typed Client interface is not sufficient.
type RawClient struct {
	c *jsonrpc.Client
}

// NewRawClient connects to the ARI server over a Unix domain socket.
// Returns an error if the socket file is missing or the connection fails.
func NewRawClient(socketPath string) (*RawClient, error) {
	c, err := jsonrpc.Dial(context.Background(), "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", socketPath, err)
	}
	return &RawClient{c: c}, nil
}

// Call sends a JSON-RPC request to the ARI server and unmarshals the response.
// This is a blocking call that waits for the server response.
func (c *RawClient) Call(method string, params, result any) error {
	return c.c.Call(context.Background(), method, params, result)
}

// Close closes the connection to the ARI server.
func (c *RawClient) Close() error {
	return c.c.Close()
}
