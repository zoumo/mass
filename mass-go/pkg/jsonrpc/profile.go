package jsonrpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxSafeInteger = 9007199254740991

// Role selects the allowed message kinds for a connection endpoint.
type Role uint8

const (
	// RoleServer accepts requests and notifications.
	RoleServer Role = iota
	// RoleClient accepts responses and notifications.
	RoleClient
)

// Kind classifies a validated JSON-RPC message.
type Kind uint8

const (
	// Request has a method and an ID.
	Request Kind = iota
	// Notification has a method and no ID.
	Notification
	// Response has an ID and exactly one of result or error.
	Response
)

// Message is the validated envelope metadata needed before dispatch.
type Message struct {
	Kind   Kind
	ID     json.RawMessage
	Method string
	Params json.RawMessage
}

// ProfileError is a JSON-RPC error produced while validating a message envelope.
type ProfileError struct {
	Code     int64
	Message  string
	Reason   string
	ID       json.RawMessage
	Suppress bool
}

func (e *ProfileError) Error() string { return e.Message }

// Parse validates one text message against the MASS profile.
func Parse(data []byte, role Role) (Message, *ProfileError) {
	if !utf8.Valid(data) {
		return Message{}, &ProfileError{Code: -32700, Message: "Parse error"}
	}
	if err := validateJSON(data); err != nil {
		code := int64(-32700)
		message := "Parse error"
		if errors.Is(err, errDuplicateKey) {
			code, message = -32600, "Invalid Request"
		}
		return Message{}, &ProfileError{Code: code, Message: message}
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return Message{}, &ProfileError{Code: -32600, Message: "Invalid Request", Suppress: role == RoleClient}
	}

	version, ok := object["jsonrpc"]
	if !ok || decodeString(version) != EnvelopeVersion {
		return Message{}, invalidRequest(object, role)
	}

	_, hasMethod := object["method"]
	_, hasResult := object["result"]
	_, hasError := object["error"]
	switch {
	case hasMethod && !hasResult && !hasError:
		return parseRequest(object, role)
	case !hasMethod && (hasResult || hasError):
		return parseResponse(object, role)
	default:
		return Message{}, invalidRequest(object, role)
	}
}

func parseRequest(object map[string]json.RawMessage, role Role) (Message, *ProfileError) {
	for field := range object {
		switch field {
		case "jsonrpc", "id", "method", "params":
		default:
			return Message{}, invalidRequest(object, role)
		}
	}

	method, ok := decodeStringOK(object["method"])
	if !ok {
		return Message{}, invalidRequest(object, role)
	}
	rawID, hasID := object["id"]
	notification := !hasID
	if role == RoleClient && hasID {
		return Message{}, &ProfileError{Code: -32600, Message: "Invalid Request", Suppress: true}
	}

	var id json.RawMessage
	if hasID {
		var profileErr *ProfileError
		id, profileErr = parseID(rawID, false)
		if profileErr != nil {
			return Message{}, profileErr
		}
	}

	params := object["params"]
	if len(params) > 0 {
		trimmed := bytes.TrimSpace(params)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			return Message{}, &ProfileError{Code: -32602, Message: "Invalid params", ID: id, Suppress: notification}
		}
		if !validJSONStringEscapes(trimmed) {
			return Message{}, &ProfileError{Code: -32602, Message: "Invalid params", ID: id, Suppress: notification}
		}
	}

	kind := Request
	if notification {
		kind = Notification
	}
	return Message{Kind: kind, ID: id, Method: method, Params: params}, nil
}

// validJSONStringEscapes rejects isolated UTF-16 surrogate escapes. JSON
// grammar allows them, but the decoded value would silently become U+FFFD
// instead of the string the caller sent.
func validJSONStringEscapes(data []byte) bool {
	inString := false
	for i := 0; i < len(data); i++ {
		switch {
		case !inString:
			if data[i] == '"' {
				inString = true
			}
			continue
		case data[i] == '"':
			inString = false
			continue
		case data[i] != '\\':
			continue
		}
		i++
		if i >= len(data) {
			return false
		}
		if data[i] != 'u' {
			continue
		}
		if i+4 >= len(data) {
			return false
		}
		high, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if high >= 0xDC00 && high <= 0xDFFF {
			return false
		}
		if high < 0xD800 || high > 0xDBFF {
			continue
		}
		if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
			return false
		}
		low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
		if err != nil || low < 0xDC00 || low > 0xDFFF {
			return false
		}
		i += 6
	}
	return true
}

func parseResponse(object map[string]json.RawMessage, role Role) (Message, *ProfileError) {
	if role == RoleServer {
		return Message{}, &ProfileError{Code: -32600, Message: "Invalid Request"}
	}
	for field := range object {
		switch field {
		case "jsonrpc", "id", "result", "error":
		default:
			return Message{}, &ProfileError{Code: -32600, Message: "Invalid Request", Suppress: true}
		}
	}
	rawID, hasID := object["id"]
	if !hasID {
		return Message{}, &ProfileError{Code: -32600, Message: "Invalid Request", Suppress: true}
	}
	id, profileErr := parseID(rawID, true)
	if profileErr != nil {
		profileErr.Suppress = true
		return Message{}, profileErr
	}
	_, hasResult := object["result"]
	rawError, hasError := object["error"]
	if hasResult == hasError {
		return Message{}, &ProfileError{Code: -32600, Message: "Invalid Request", ID: id, Suppress: true}
	}
	if hasError && validateErrorObject(rawError) != nil {
		return Message{}, &ProfileError{Code: -32600, Message: "Invalid Request", ID: id, Suppress: true}
	}
	return Message{Kind: Response, ID: id}, nil
}

func validateErrorObject(raw json.RawMessage) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return err
	}
	for field := range object {
		switch field {
		case "code", "message", "data":
		default:
			return errors.New("unknown error field")
		}
	}
	code, ok := object["code"]
	if !ok {
		return errors.New("missing error code")
	}
	rawMessage, ok := object["message"]
	if _, valid := decodeStringOK(rawMessage); !ok || !valid {
		return errors.New("invalid error message")
	}
	text := string(bytes.TrimSpace(code))
	if text == "" || strings.ContainsAny(text, ".eE+") {
		return errors.New("invalid error code")
	}
	_, err := strconv.ParseInt(text, 10, 64)
	return err
}

func invalidRequest(object map[string]json.RawMessage, role Role) *ProfileError {
	profileErr := &ProfileError{Code: -32600, Message: "Invalid Request", Suppress: role == RoleClient}
	if rawID, ok := object["id"]; ok {
		if id, err := parseID(rawID, true); err == nil {
			profileErr.ID = id
		}
	}
	return profileErr
}

func parseID(raw json.RawMessage, allowNull bool) (json.RawMessage, *ProfileError) {
	text := string(bytes.TrimSpace(raw))
	switch {
	case text == "null":
		if allowNull {
			return raw, nil
		}
	case strings.HasPrefix(text, `"`):
		var value string
		if json.Unmarshal(raw, &value) == nil {
			return raw, nil
		}
	case text != "" && !strings.HasPrefix(text, "-") && !strings.ContainsAny(text, ".eE+"):
		value, err := strconv.ParseUint(text, 10, 64)
		if err == nil && value <= maxSafeInteger {
			return raw, nil
		}
	}
	return nil, &ProfileError{Code: -32600, Message: "Invalid Request"}
}

func decodeString(raw json.RawMessage) string {
	value, _ := decodeStringOK(raw)
	return value
}

func decodeStringOK(raw json.RawMessage) (string, bool) {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

// DecodeStrict decodes one fixed-structure JSON value with unknown fields rejected.
func DecodeStrict(data []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("multiple JSON values")
	}
	return nil
}

var errDuplicateKey = errors.New("duplicate object key")

func validateJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := scanValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	return nil
}

func scanValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		fields := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, duplicate := fields[key]; duplicate {
				return errDuplicateKey
			}
			fields[key] = struct{}{}
			if err := scanValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := scanValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return errors.New("unexpected closing delimiter")
	}
}
