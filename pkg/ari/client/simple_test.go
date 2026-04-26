package client

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestNewClientSocketMissing tests that NewClient returns an error
// when the socket file does not exist.
func TestNewClientSocketMissing(t *testing.T) {
	// Use a non-existent socket path
	socketPath := filepath.Join(os.TempDir(), "nonexistent-ari-socket-"+t.Name()+".sock")
	// Ensure the file does not exist
	_ = os.Remove(socketPath)

	client, err := NewRawClient(socketPath)
	if err == nil {
		client.Close()
		t.Fatal("expected error for missing socket, got nil")
	}
	// Verify error contains socket path
	if !containsSubstring(err.Error(), socketPath) {
		t.Errorf("error message should contain socket path %q, got: %v", socketPath, err)
	}
}

// TestNewClientDaemonUnavailable tests that NewClient returns an error
// when no daemon is listening on the socket (connection refused).
func TestNewClientDaemonUnavailable(t *testing.T) {
	// Create a temporary socket file but don't listen on it
	socketPath := filepath.Join(os.TempDir(), "unavailable-ari-socket-"+t.Name()+".sock")
	// Remove any existing file
	_ = os.Remove(socketPath)

	// Create the file but don't start a server
	f, err := os.Create(socketPath)
	if err != nil {
		t.Fatalf("failed to create socket file: %v", err)
	}
	f.Close()
	defer os.Remove(socketPath)

	client, err := NewRawClient(socketPath)
	if err == nil {
		client.Close()
		t.Fatal("expected error for unavailable daemon, got nil")
	}
	if client != nil {
		client.Close()
		t.Fatal("expected nil client for unavailable daemon")
	}
}

// TestCallMalformedResponse tests that Call returns a parse error
// when the server returns malformed JSON.
func TestCallMalformedResponse(t *testing.T) {
	// Create a mock server that returns malformed JSON
	socketPath := filepath.Join(os.TempDir(), "malformed-ari-socket-"+t.Name()+".sock")
	_ = os.Remove(socketPath)

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("failed to listen on socket: %v", err)
	}
	defer listener.Close()
	defer os.Remove(socketPath)

	// Start mock server in background
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// Read request (discard it)
		buf := make([]byte, 1024)
		_, _ = conn.Read(buf)
		// Send malformed JSON response
		_, _ = conn.Write([]byte("not valid json!!!\n"))
	}()

	// Wait for server to be ready
	time.Sleep(50 * time.Millisecond)

	client, err := NewRawClient(socketPath)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer client.Close()

	var result map[string]interface{}
	err = client.Call("test/method", nil, &result)
	if err == nil {
		t.Fatal("expected error for malformed response, got nil")
	}
}

// TestCallRpcError tests that Call returns an error with code/message
// when the server returns an RPC error response.
func TestCallRpcError(t *testing.T) {
	socketPath := filepath.Join(os.TempDir(), "rpcerror-ari-socket-"+t.Name()+".sock")
	_ = os.Remove(socketPath)

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("failed to listen on socket: %v", err)
	}
	defer listener.Close()
	defer os.Remove(socketPath)

	// Start mock server that returns RPC error
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// Read request
		buf := make([]byte, 1024)
		_, _ = conn.Read(buf)
		// Send RPC error response
		errorResp := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      0,
			"error": map[string]interface{}{
				"code":    -32000,
				"message": "server error: session not found",
			},
		}
		data, _ := json.Marshal(errorResp)
		_, _ = conn.Write(data)
		_, _ = conn.Write([]byte("\n"))
	}()

	time.Sleep(50 * time.Millisecond)

	client, err := NewRawClient(socketPath)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer client.Close()

	var result map[string]interface{}
	err = client.Call("session/status", map[string]string{"sessionId": "nonexistent"}, &result)
	if err == nil {
		t.Fatal("expected RPC error, got nil")
	}
	// Verify error contains code and message
	if !containsSubstring(err.Error(), "-32000") {
		t.Errorf("error should contain code -32000, got: %v", err)
	}
	if !containsSubstring(err.Error(), "session not found") {
		t.Errorf("error should contain message 'session not found', got: %v", err)
	}
}

// TestCallResponseIdMismatch tests that Call returns an error
// when the response ID does not match the request ID.
func TestCallResponseIdMismatch(t *testing.T) {
	socketPath := filepath.Join(os.TempDir(), "idmismatch-ari-socket-"+t.Name()+".sock")
	_ = os.Remove(socketPath)

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("failed to listen on socket: %v", err)
	}
	defer listener.Close()
	defer os.Remove(socketPath)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 1024)
		_, _ = conn.Read(buf)
		// Send response with wrong ID
		resp := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      999, // Wrong ID
			"result":  map[string]string{"status": "ok"},
		}
		data, _ := json.Marshal(resp)
		_, _ = conn.Write(data)
		_, _ = conn.Write([]byte("\n"))
	}()

	time.Sleep(50 * time.Millisecond)

	client, err := NewRawClient(socketPath)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer client.Close()

	var result map[string]interface{}
	err = client.Call("test/method", nil, &result)
	if err == nil {
		t.Fatal("expected error for ID mismatch, got nil")
	}
}

// TestNewClientSuccess tests successful connection to a valid server.
func TestNewClientSuccess(t *testing.T) {
	socketPath := filepath.Join(os.TempDir(), "success-ari-socket-"+t.Name()+".sock")
	_ = os.Remove(socketPath)

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("failed to listen on socket: %v", err)
	}
	defer listener.Close()
	defer os.Remove(socketPath)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 1024)
		_, _ = conn.Read(buf)
		resp := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      0,
			"result":  map[string]string{"status": "ok"},
		}
		data, _ := json.Marshal(resp)
		_, _ = conn.Write(data)
		_, _ = conn.Write([]byte("\n"))
	}()

	time.Sleep(50 * time.Millisecond)

	client, err := NewRawClient(socketPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer client.Close()

	var result map[string]string
	err = client.Call("test/method", nil, &result)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if result["status"] != "ok" {
		t.Errorf("expected status 'ok', got %v", result)
	}
}

// containsSubstring checks if s contains substr.
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstringHelper(s, substr))
}

func containsSubstringHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
