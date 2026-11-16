package sa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	rpc2 "github.com/sourcegraph/jsonrpc2"
)

const (
	massProtocolVersion  = "1"
	rpcWriteTimeout      = 5 * time.Second
	maxInitialInputBytes = 64 << 10
)

// InitialSessionConfig is the literal model and effort sent to massd.
type InitialSessionConfig struct {
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}

// MCPServer is the provider-neutral Session MCP configuration consumed by massd.
type MCPServer struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
}

// SessionSpec is the subset of a MASS Session spec used by mass-sa.
type SessionSpec struct {
	RuntimeRef   string                `json:"runtimeRef"`
	Cwd          string                `json:"cwd"`
	SystemPrompt string                `json:"systemPrompt,omitempty"`
	Config       *InitialSessionConfig `json:"config,omitempty"`
	MCPServers   []MCPServer           `json:"mcpServers,omitempty"`
}

// SessionMetadata identifies a named MASS Session.
type SessionMetadata struct {
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

// SessionStatus contains the durable and observed Session identity.
type SessionStatus struct {
	SessionID string `json:"sessionId"`
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"`
}

// SessionContentBlock is one create-only initial input content block.
type SessionContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// SessionInitialInput is operation data accepted only by session.create. It is
// never part of SessionSpec and is never replayed on resume or recovery.
type SessionInitialInput struct {
	Content []SessionContentBlock `json:"content"`
}

// Session is the MASS Session shape consumed by mass-sa.
type Session struct {
	Metadata            SessionMetadata `json:"metadata"`
	Spec                SessionSpec     `json:"spec"`
	Status              SessionStatus   `json:"status"`
	InitialInputReceipt json.RawMessage `json:"initialInputReceipt,omitempty"`
}

// SessionPage is the MASS session.list result shape used by mass-sa.
type SessionPage struct {
	Items []Session `json:"items"`
	Next  string    `json:"next,omitempty"`
}

// SessionQuery selects one namespace page without crossing a worker scope.
type SessionQuery struct {
	Namespace  string
	RuntimeRef string
	State      string
	After      string
	Limit      int
}

// CreateSessionInput is the explicit session.create request shape.
type CreateSessionInput struct {
	Metadata     SessionMetadata      `json:"metadata"`
	Spec         SessionSpec          `json:"spec"`
	InitialInput *SessionInitialInput `json:"initialInput,omitempty"`
}

type initializeResult struct {
	ProtocolVersion string `json:"protocolVersion"`
	ServerInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
}

type clientHandler struct{}

func (clientHandler) Handle(ctx context.Context, conn *rpc2.Conn, req *rpc2.Request) {
	if req.Notif {
		return
	}
	_ = conn.ReplyWithError(ctx, req.ID, &rpc2.Error{
		Code:    rpc2.CodeMethodNotFound,
		Message: "Method not found",
	})
}

type rpcLogger struct{}

func (rpcLogger) Printf(format string, args ...any) {
	slog.Default().Debug("mass-sa jsonrpc", "message", strings.TrimSuffix(fmt.Sprintf(format, args...), "\n"))
}

// MASSClient owns one initialized WebSocket JSON-RPC connection. Request
// correlation, notifications, and JSON-RPC errors are delegated to
// sourcegraph/jsonrpc2; this type owns only MASS wire and error mapping.
type MASSClient struct {
	config MASSConfig
	conn   *rpc2.Conn
	stream *websocketObjectStream
	cancel context.CancelFunc

	closeOnce sync.Once
	closeErr  error
}

// OpenMASS connects to the configured endpoint and completes initialize.
func OpenMASS(ctx context.Context, config MASSConfig) (*MASSClient, error) {
	endpoint, err := normalizeMASSEndpoint(config.Endpoint)
	if err != nil {
		return nil, err
	}
	token, err := resolveMASSToken(config)
	if err != nil {
		return nil, err
	}
	headers := http.Header{}
	if token != "" {
		headers.Set("Authorization", "Bearer "+token)
	}
	dialer := websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: rpcWriteTimeout,
	}
	socket, response, err := dialer.DialContext(ctx, endpoint, headers)
	if response != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		if response != nil {
			return nil, operationError("unavailable", "mass_unreachable", "MASS WebSocket handshake failed: "+response.Status)
		}
		return nil, operationError("unavailable", "mass_unreachable", "MASS WebSocket connection failed")
	}
	socket.SetReadLimit(4 << 20)

	life, cancel := context.WithCancel(context.Background())
	stream := &websocketObjectStream{conn: socket, writeTimeout: rpcWriteTimeout}
	client := &MASSClient{
		config: config,
		conn:   rpc2.NewConn(life, stream, clientHandler{}, rpc2.SetLogger(rpcLogger{})),
		stream: stream,
		cancel: cancel,
	}
	var result initializeResult
	err = client.call(ctx, "initialize", map[string]any{
		"protocolVersion": massProtocolVersion,
		"clientInfo": map[string]string{
			"name":    "mass-sa",
			"version": "0.1.0-demo",
		},
	}, &result, false)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	if result.ProtocolVersion != massProtocolVersion || result.ServerInfo.Name == "" || result.ServerInfo.Version == "" {
		_ = client.Close()
		return nil, operationError("unavailable", "mass_protocol_mismatch", "MASS initialize result is incompatible")
	}
	return client, nil
}

func resolveMASSToken(config MASSConfig) (string, error) {
	switch {
	case config.TokenEnv != "" && config.TokenFile != "":
		return "", invalidConfig("mass.tokenEnv and mass.tokenFile are mutually exclusive")
	case config.TokenEnv != "":
		token, exists := os.LookupEnv(config.TokenEnv)
		if !exists || strings.TrimSpace(token) == "" {
			return "", operationError("unavailable", "mass_token_missing", "MASS token environment variable is not set")
		}
		return strings.TrimSpace(token), nil
	case config.TokenFile != "":
		content, err := os.ReadFile(config.TokenFile)
		if err != nil {
			return "", operationError("unavailable", "mass_token_missing", "MASS token file is unavailable")
		}
		token := strings.TrimSpace(string(content))
		if token == "" {
			return "", operationError("unavailable", "mass_token_missing", "MASS token file is empty")
		}
		return token, nil
	default:
		return "", nil
	}
}

// GetSession resolves exactly one SessionRef.
func (c *MASSClient) GetSession(ctx context.Context, ref SessionRef) (Session, error) {
	if err := validateSessionRef(ref); err != nil {
		return Session{}, err
	}
	var result Session
	err := c.call(ctx, "session.get", map[string]any{"session": ref}, &result, false)
	return result, err
}

// ListSessions lists one namespace page.
func (c *MASSClient) ListSessions(ctx context.Context, query SessionQuery) (SessionPage, error) {
	params := map[string]any{}
	q := map[string]any{}
	if query.Namespace != "" {
		q["namespace"] = query.Namespace
	}
	if query.RuntimeRef != "" {
		q["runtimeRef"] = query.RuntimeRef
	}
	if query.State != "" {
		q["state"] = query.State
	}
	if query.After != "" || query.Limit != 0 {
		q["page"] = map[string]any{"after": query.After, "limit": query.Limit}
	}
	if len(q) != 0 {
		params["query"] = q
	}
	var result SessionPage
	err := c.call(ctx, "session.list", params, &result, false)
	return result, err
}

// CreateSession creates one Session without an initial input.
func (c *MASSClient) CreateSession(ctx context.Context, input CreateSessionInput) (Session, error) {
	if input.Metadata.Namespace == "" || input.Metadata.Name == "" || input.Spec.RuntimeRef == "" || input.Spec.Cwd == "" {
		return Session{}, operationError("invalid_argument", "invalid_session_create", "Session create input is incomplete")
	}
	if input.InitialInput != nil {
		if len(input.InitialInput.Content) == 0 {
			return Session{}, operationError("invalid_argument", "invalid_initial_input", "initial input must contain at least one content block")
		}
		total := 0
		for _, block := range input.InitialInput.Content {
			if block.Type != "text" || strings.TrimSpace(block.Text) == "" {
				return Session{}, operationError("invalid_argument", "invalid_initial_input", "initial input supports non-blank text content blocks only")
			}
			total += len(block.Text)
		}
		if total > maxInitialInputBytes {
			return Session{}, operationError("invalid_argument", "initial_input_too_large", "initial input exceeds the size limit")
		}
	}
	var result Session
	err := c.call(ctx, "session.create", map[string]any{"input": input}, &result, true)
	return result, err
}

// ResumeSession explicitly resumes the same persisted Session identity.
func (c *MASSClient) ResumeSession(ctx context.Context, ref SessionRef) (Session, error) {
	if err := validateSessionRef(ref); err != nil {
		return Session{}, err
	}
	var result Session
	err := c.call(ctx, "session.resume", map[string]any{"session": ref}, &result, true)
	return result, err
}

// CloseSession closes execution resources without deleting identity or history.
func (c *MASSClient) CloseSession(ctx context.Context, ref SessionRef) (Session, error) {
	if err := validateSessionRef(ref); err != nil {
		return Session{}, err
	}
	var result Session
	err := c.call(ctx, "session.close", map[string]any{"session": ref}, &result, true)
	return result, err
}

// PromptSession submits one text input and returns the unmodified acceptance receipt.
func (c *MASSClient) PromptSession(ctx context.Context, ref SessionRef, text string) (json.RawMessage, error) {
	if err := validateSessionRef(ref); err != nil {
		return nil, err
	}
	if strings.TrimSpace(text) == "" {
		return nil, operationError("invalid_argument", "invalid_prompt", "prompt text is required")
	}
	var result json.RawMessage
	err := c.call(ctx, "session.prompt", map[string]any{
		"session": ref,
		"input": map[string]any{
			"content": []map[string]string{{"type": "text", "text": text}},
		},
	}, &result, true)
	return result, err
}

// Close closes the WebSocket connection once.
func (c *MASSClient) Close() error {
	c.closeOnce.Do(func() {
		if c.conn != nil {
			c.closeErr = c.conn.Close()
		}
		if c.cancel != nil {
			c.cancel()
		}
		if c.stream != nil {
			c.closeErr = errors.Join(c.closeErr, c.stream.Close())
		}
		var rpcErr *rpc2.Error
		if errors.As(c.closeErr, &rpcErr) {
			c.closeErr = nil
		}
	})
	return c.closeErr
}

func (c *MASSClient) call(ctx context.Context, method string, params any, result any, mutating bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := c.conn.Call(ctx, method, params, result, rpc2.StringID())
	if err == nil {
		return nil
	}
	var rpcErr *rpc2.Error
	if errors.As(err, &rpcErr) {
		return mapRPCError(rpcErr)
	}
	if mutating {
		return outcomeUnknownError("mass_result_unconfirmed")
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, rpc2.ErrClosed) {
		return operationError("unavailable", "mass_connection_closed", "MASS connection is closed")
	}
	return operationError("unavailable", "mass_unreachable", "MASS response was not received")
}

func mapRPCError(err *rpc2.Error) error {
	if err.Code == -32000 && err.Data != nil && len(*err.Data) > 0 {
		var operation OperationError
		if json.Unmarshal(*err.Data, &operation) == nil && operation.Code != "" {
			return &operation
		}
	}
	if err.Code == -32001 && err.Data != nil {
		var data struct {
			Reason string `json:"reason"`
		}
		if json.Unmarshal(*err.Data, &data) == nil && data.Reason != "" {
			return operationError("failed_precondition", data.Reason, "MASS protocol state rejected the request")
		}
	}
	message := strings.TrimSpace(err.Message)
	if message == "" {
		message = "MASS JSON-RPC request failed"
	}
	switch err.Code {
	case -32600, -32602:
		return operationError("invalid_argument", "mass_invalid_params", "MASS rejected the request shape")
	case -32601:
		return operationError("not_found", "method_not_found", "MASS method is not available")
	case -32603, -32700:
		return operationError("unavailable", "mass_protocol_error", "MASS protocol request failed")
	default:
		return operationError("unavailable", fmt.Sprintf("jsonrpc_%d", err.Code), message)
	}
}

func outcomeUnknownError(reason string) error {
	return operationError("outcome_unknown", reason, "MASS operation outcome is unknown; inspect state before retrying")
}

func validateSessionRef(ref SessionRef) error {
	if ref.SessionID != "" {
		if ref.Namespace != "" || ref.Name != "" {
			return operationError("invalid_argument", "invalid_session_ref", "SessionRef must use exactly one addressing form")
		}
		return nil
	}
	if ref.Namespace == "" || ref.Name == "" {
		return operationError("invalid_argument", "invalid_session_ref", "SessionRef must include sessionId or namespace and name")
	}
	return nil
}

type websocketObjectStream struct {
	conn         *websocket.Conn
	writeTimeout time.Duration

	writeMu   sync.Mutex
	closeOnce sync.Once
	closeErr  error
}

func (s *websocketObjectStream) ReadObject(value any) error {
	messageType, raw, err := s.conn.ReadMessage()
	if err != nil {
		return err
	}
	if messageType != websocket.TextMessage {
		_ = s.conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseUnsupportedData, "JSON-RPC requires text messages"),
			time.Now().Add(time.Second),
		)
		return errors.New("MASS sent a non-text WebSocket message")
	}
	if !validRPCEnvelope(raw) {
		return errors.New("MASS sent an invalid JSON-RPC envelope")
	}
	return json.Unmarshal(raw, value)
}

func (s *websocketObjectStream) WriteObject(value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if !validRPCEnvelope(payload) {
		return errors.New("attempted to send an invalid JSON-RPC envelope")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.conn.SetWriteDeadline(time.Now().Add(s.writeTimeout)); err != nil {
		return err
	}
	return s.conn.WriteMessage(websocket.TextMessage, payload)
}

func (s *websocketObjectStream) Close() error {
	s.closeOnce.Do(func() {
		_ = s.conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
			time.Now().Add(time.Second),
		)
		s.closeErr = s.conn.Close()
	})
	return s.closeErr
}

func validRPCEnvelope(raw []byte) bool {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed[0] != '{' {
		return false
	}
	var envelope struct {
		JSONRPC string `json:"jsonrpc"`
	}
	return json.Unmarshal(raw, &envelope) == nil && envelope.JSONRPC == "2.0"
}
