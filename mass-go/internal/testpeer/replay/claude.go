package replay

import (
	"encoding/json"
	"errors"
	"strconv"

	"github.com/zoumo/mass/mass-go/internal/testpeer/rawjson"
)

const assignedSessionPlaceholder = "claude-session"

// syntheticCWD is the placeholder path those synthetic fixtures use for a
// working directory. A claiming peer reports the directory the CLI was
// actually bound to, so no fixture depends on a real path.
const syntheticCWD = "/synthetic"

func replaceSyntheticCWD(value rawjson.Value, cwd string) (rawjson.Value, error) {
	if stringMember(value, "cwd") != syntheticCWD {
		return value, nil
	}
	replacement, err := rawjson.Parse([]byte(strconv.Quote(cwd)))
	if err != nil {
		return rawjson.Value{}, err
	}
	return value.Replace([]string{"cwd"}, replacement)
}

// claimExchange answers a synthetic spare's claim_session request with the
// identity this peer assigned. It mirrors the native reply shape without
// inventing any model or account fact.
func claimExchange(value rawjson.Value, sessionID string) (rawjson.Value, bool, error) {
	if stringMember(value, "type") != "control_request" {
		return rawjson.Value{}, false, nil
	}
	request := member(value, "request")
	if stringMember(request, "subtype") != "claim_session" {
		return rawjson.Value{}, false, nil
	}
	cwd := stringMember(request, "cwd")
	if cwd == "" {
		return rawjson.Value{}, true, errors.New("claim_session has no working directory")
	}
	payload, err := json.Marshal(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": json.RawMessage(member(value, "request_id").Raw()),
			"response": map[string]any{
				"status":     "ok",
				"session_id": sessionID,
				"cwd":        cwd,
			},
		},
	})
	if err != nil {
		return rawjson.Value{}, true, err
	}
	reply, err := rawjson.Parse(payload)
	return reply, true, err
}

type claudeAdapter struct {
	requests
	sessionID string
}

func control(value rawjson.Value) (string, rawjson.Value, error) {
	kind := stringMember(value, "type")
	target := value
	if kind == "control_response" {
		var exists bool
		target, exists = value.Member("response")
		if exists && target.Kind() != rawjson.Object {
			return "", rawjson.Value{}, errors.New("control response must be an object")
		}
	}
	return kind, member(target, "request_id"), nil
}

func (a *claudeAdapter) client(expected, actual rawjson.Value) (bool, error) {
	kind, recordedID, err := control(expected)
	if err != nil {
		return false, err
	}
	if kind == "control_request" || kind == "control_response" {
		key, err := identity(recordedID, true)
		if err != nil {
			return false, err
		}
		actualKind, actualID, err := control(actual)
		if err != nil {
			return false, err
		}
		live, err := identity(actualID, true)
		if err != nil {
			return false, err
		}
		if actualKind != kind {
			return false, errors.New("control type mismatch")
		}
		if kind == "control_request" {
			if live.kind != key.kind {
				return false, errors.New("control ID type mismatch")
			}
			if err := a.bind(key, live, actualID); err != nil {
				return false, err
			}
			expected, err = expected.Replace([]string{"request_id"}, actualID)
			if err != nil {
				return false, err
			}
		} else if err := a.answer(key); err != nil {
			return false, err
		}
	}
	return false, match(expected, actual)
}

func (a *claudeAdapter) server(expected rawjson.Value) (rawjson.Value, error) {
	if a.sessionID != "" && stringMember(expected, "session_id") == assignedSessionPlaceholder {
		replacement, err := rawjson.Parse([]byte(strconv.Quote(a.sessionID)))
		if err != nil {
			return rawjson.Value{}, err
		}
		expected, err = expected.Replace([]string{"session_id"}, replacement)
		if err != nil {
			return rawjson.Value{}, err
		}
	}
	kind, recordedID, err := control(expected)
	if err != nil {
		return rawjson.Value{}, err
	}
	if kind != "control_request" && kind != "control_response" && kind != "control_cancel_request" {
		return expected, nil
	}
	key, err := identity(recordedID, true)
	if err != nil {
		return rawjson.Value{}, err
	}
	switch kind {
	case "control_request":
		err = a.reverse(key)
	case "control_response":
		var live rawjson.Value
		live, err = a.response(key)
		if err == nil {
			return expected.Replace([]string{"response", "request_id"}, live)
		}
	case "control_cancel_request":
		err = a.answer(key)
	}
	return expected, err
}
