package jsonrpc

import (
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
)

// Stream implements jsonrpc2.ObjectStream with MASS validation.
type Stream struct {
	messages     messageTransport
	role         Role
	writeTimeout time.Duration
	writeMu      sync.Mutex

	closeOnce sync.Once
	errMu     sync.Mutex
	lastErr   *ProfileError
}

// NewStream adapts a WebSocket to the shared validated JSON-RPC stream.
func NewStream(conn *websocket.Conn, role Role, writeTimeout time.Duration) (*Stream, error) {
	if conn == nil {
		return nil, errors.New("WebSocket connection is required")
	}
	return newStream(webSocketMessages{conn}, role, writeTimeout)
}

type messageTransport interface {
	read() ([]byte, error)
	write([]byte, time.Duration) error
	close(int, string) error
}

func newStream(messages messageTransport, role Role, writeTimeout time.Duration) (*Stream, error) {
	if writeTimeout <= 0 {
		return nil, errors.New("write timeout must be positive")
	}
	return &Stream{messages: messages, role: role, writeTimeout: writeTimeout}, nil
}

// ReadObject reads and validates the next message before jsonrpc2 decodes it.
func (s *Stream) ReadObject(out any) error {
	for {
		data, err := s.messages.read()
		if err != nil {
			if errors.Is(err, errBinaryMessage) {
				s.closeWith(websocket.CloseUnsupportedData, "text messages required")
			}
			return err
		}
		if !utf8.Valid(data) {
			s.closeWith(websocket.CloseInvalidFramePayloadData, "invalid UTF-8")
			return errors.New("invalid UTF-8 JSON-RPC message")
		}

		message, profileErr := Parse(data, s.role)
		if profileErr != nil {
			s.setProtocolError(profileErr)
			if s.role == RoleClient {
				s.closeWith(websocket.CloseProtocolError, profileErr.Message)
				return profileErr
			}
			if !profileErr.Suppress {
				if err := s.WriteObject(profileErr.response()); err != nil {
					return err
				}
			}
			continue
		}
		if err := json.Unmarshal(data, out); err != nil {
			profileErr = &ProfileError{Code: -32600, Message: "Invalid Request", ID: message.ID}
			s.setProtocolError(profileErr)
			if s.role == RoleClient {
				s.closeWith(websocket.CloseProtocolError, profileErr.Message)
				return profileErr
			}
			if err := s.WriteObject(profileErr.response()); err != nil {
				return err
			}
			continue
		}
		return nil
	}
}

// WriteObject marshals and writes one text message under the shared write lock.
func (s *Stream) WriteObject(object any) error {
	if err := ValidateLossless(object); err != nil {
		return err
	}
	data, err := json.Marshal(object)
	if err != nil {
		return err
	}
	if !utf8.Valid(data) || !validJSONStringEscapes(data) {
		return ErrLossyJSON
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.messages.write(data, s.writeTimeout); err != nil {
		s.closeWith(websocket.CloseInternalServerErr, "write failed")
		return err
	}
	return nil
}

// Close terminates the connection and releases blocked readers and writers.
func (s *Stream) Close() error {
	var err error
	s.closeOnce.Do(func() {
		err = s.messages.close(websocket.CloseNormalClosure, "")
	})
	return err
}

// ProtocolError returns the malformed-message cause recorded before closure.
func (s *Stream) ProtocolError() *ProfileError {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.lastErr
}

func (s *Stream) setProtocolError(err *ProfileError) {
	s.errMu.Lock()
	s.lastErr = err
	s.errMu.Unlock()
}

func (s *Stream) closeWith(code int, text string) {
	s.closeOnce.Do(func() {
		_ = s.messages.close(code, text)
	})
}

func (e *ProfileError) response() any {
	return struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   errorObject     `json:"error"`
	}{
		JSONRPC: EnvelopeVersion,
		ID:      e.ID,
		Error:   errorObject{Code: e.Code, Message: e.Message},
	}
}

type errorObject struct {
	Code    int64  `json:"code"`
	Message string `json:"message"`
}

var errBinaryMessage = errors.New("binary WebSocket message rejected")

type webSocketMessages struct{ conn *websocket.Conn }

func (s webSocketMessages) read() ([]byte, error) {
	kind, data, err := s.conn.ReadMessage()
	if err != nil {
		return nil, normalizeReadError(err)
	}
	if kind != websocket.TextMessage {
		return nil, errBinaryMessage
	}
	return data, nil
}

func (s webSocketMessages) write(data []byte, timeout time.Duration) error {
	if err := s.conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	return s.conn.WriteMessage(websocket.TextMessage, data)
}

func (s webSocketMessages) close(code int, text string) error {
	_ = s.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, text), time.Now().Add(time.Second))
	return s.conn.Close()
}

func normalizeReadError(err error) error {
	var closeErr *websocket.CloseError
	if errors.As(err, &closeErr) && closeErr.Code == websocket.CloseAbnormalClosure && closeErr.Text == io.ErrUnexpectedEOF.Error() {
		return io.ErrUnexpectedEOF
	}
	return err
}
