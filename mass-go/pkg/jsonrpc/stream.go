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

const defaultWriteTimeout = 10 * time.Second

// Stream adapts a WebSocket to jsonrpc2.ObjectStream with MASS validation.
type Stream struct {
	conn         *websocket.Conn
	role         Role
	writeTimeout time.Duration
	writeMu      sync.Mutex

	closeOnce sync.Once
	errMu     sync.Mutex
	lastErr   *ProfileError
}

// NewStream creates a validated text-message stream.
func NewStream(conn *websocket.Conn, role Role, writeTimeout time.Duration) *Stream {
	if writeTimeout <= 0 {
		writeTimeout = defaultWriteTimeout
	}
	return &Stream{conn: conn, role: role, writeTimeout: writeTimeout}
}

// ReadObject reads and validates the next message before jsonrpc2 decodes it.
func (s *Stream) ReadObject(out any) error {
	for {
		messageType, data, err := s.conn.ReadMessage()
		if err != nil {
			return normalizeReadError(err)
		}
		if messageType != websocket.TextMessage {
			s.closeWith(websocket.CloseUnsupportedData, "text messages required")
			return errors.New("binary WebSocket message rejected")
		}
		if !utf8.Valid(data) {
			s.closeWith(websocket.CloseInvalidFramePayloadData, "invalid UTF-8")
			return errors.New("invalid UTF-8 WebSocket message")
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
	data, err := json.Marshal(object)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.conn.SetWriteDeadline(time.Now().Add(s.writeTimeout)); err != nil {
		return err
	}
	if err := s.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		s.closeWith(websocket.CloseInternalServerErr, "write failed")
		return err
	}
	return nil
}

// Close closes the WebSocket after a best-effort normal close frame.
func (s *Stream) Close() error {
	var err error
	s.closeOnce.Do(func() {
		_ = s.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		err = s.conn.Close()
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
		_ = s.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, text), time.Now().Add(time.Second))
		_ = s.conn.Close()
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

func normalizeReadError(err error) error {
	var closeErr *websocket.CloseError
	if errors.As(err, &closeErr) && closeErr.Code == websocket.CloseAbnormalClosure && closeErr.Text == io.ErrUnexpectedEOF.Error() {
		return io.ErrUnexpectedEOF
	}
	return err
}
