package replay

import (
	"errors"

	"github.com/zoumo/mass/mass-go/internal/testpeer/rawjson"
)

type claudeAdapter struct{ requests }

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
