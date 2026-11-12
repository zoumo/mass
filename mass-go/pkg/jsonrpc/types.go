// Package jsonrpc defines the MASS JSON-RPC 2.0 profile shared by client and server.
package jsonrpc

import "encoding/json"

const (
	// Version is the MASS method-set version.
	Version = "1"

	// EnvelopeVersion is the JSON-RPC protocol version.
	EnvelopeVersion = "2.0"

	// ServerName identifies this implementation in initialize results.
	ServerName = "mass"

	// CodeServiceError identifies Service OperationError responses.
	CodeServiceError int64 = -32000

	// CodeProtocolState identifies connection protocol-state failures.
	CodeProtocolState int64 = -32001
)

// PeerInfo identifies a client or server implementation.
type PeerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeParams is the initialize method parameters.
type InitializeParams struct {
	ProtocolVersion string    `json:"protocolVersion"`
	ClientInfo      *PeerInfo `json:"clientInfo,omitempty"`
}

// Initialized is the initialize method result.
type Initialized struct {
	ProtocolVersion string   `json:"protocolVersion"`
	ServerInfo      PeerInfo `json:"serverInfo"`
}

// ProtocolErrorData is the data attached to protocol-state errors.
type ProtocolErrorData struct {
	Reason string `json:"reason"`
}

// RPCError is a JSON-RPC error returned by a peer.
type RPCError struct {
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return e.Message }

// Reason returns the structured protocol reason when present.
func (e *RPCError) Reason() string {
	var data ProtocolErrorData
	if len(e.Data) == 0 || json.Unmarshal(e.Data, &data) != nil {
		return ""
	}
	return data.Reason
}
