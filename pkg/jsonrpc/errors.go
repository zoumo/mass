// Package jsonrpc provides a transport-agnostic JSON-RPC 2.0 server and client
// framework built on top of sourcegraph/jsonrpc2.
package jsonrpc

import "fmt"

// RPCError is a JSON-RPC 2.0 error with code, message, and optional data.
// Method handlers return *RPCError to control the JSON-RPC error response;
// plain Go errors are mapped to InternalError (-32603).
type RPCError struct {
	Code    int64  `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("jsonrpc: code %d message: %s", e.Code, e.Message)
}

// Standard JSON-RPC 2.0 error constructors.

func ErrMethodNotFound(method string) *RPCError {
	return &RPCError{Code: -32601, Message: fmt.Sprintf("method not found: %s", method)}
}

func ErrInvalidParams(msg string) *RPCError {
	return &RPCError{Code: -32602, Message: msg}
}

func ErrInternal(msg string) *RPCError {
	return &RPCError{Code: -32603, Message: msg}
}
