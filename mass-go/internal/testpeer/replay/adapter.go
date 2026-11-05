// Package replay implements strict native protocol consumption. It never starts
// an agent, interprets a story or performs a recorded tool's side effects.
package replay

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/zoumo/mass/mass-go/internal/testpeer/rawjson"
)

type adapter interface {
	client(expected, actual rawjson.Value) (reverseAnswer bool, err error)
	server(rawjson.Value) (rawjson.Value, error)
	complete() error
}

func newAdapter(provider string) (adapter, error) {
	switch provider {
	case "codex":
		return &codexAdapter{requests: newRequests(), threads: map[id]string{}}, nil
	case "claude":
		return &claudeAdapter{requests: newRequests()}, nil
	default:
		return nil, fmt.Errorf("unsupported native protocol adapter: %q", provider)
	}
}

type id struct {
	kind rawjson.Kind
	text string
}

func identity(value rawjson.Value, nonempty bool) (id, error) {
	key := id{kind: value.Kind()}
	switch key.kind {
	case rawjson.String:
		key.text, _ = value.String()
		if nonempty && key.text == "" {
			return id{}, errors.New("invalid empty protocol ID")
		}
	case rawjson.Number:
		key.text = string(bytes.TrimSpace(value.Raw()))
	default:
		return id{}, errors.New("invalid protocol ID")
	}
	return key, nil
}

func member(v rawjson.Value, name string) rawjson.Value { x, _ := v.Member(name); return x }
func stringMember(v rawjson.Value, name string) string  { s, _ := member(v, name).String(); return s }

// Client and server IDs occupy independent namespaces. Bindings and seen IDs
// survive settlement: reuse is not an unrecorded new request.
type requests struct {
	bindings    map[id]rawjson.Value
	live        map[id]bool
	active      map[id]bool
	pending     map[id]bool
	reverseSeen map[id]bool
}

func newRequests() requests {
	return requests{map[id]rawjson.Value{}, map[id]bool{}, map[id]bool{}, map[id]bool{}, map[id]bool{}}
}

func (s *requests) bind(key, live id, value rawjson.Value) error {
	if _, exists := s.bindings[key]; exists || s.live[live] {
		return errors.New("duplicate client ID binding")
	}
	s.bindings[key], s.live[live], s.active[key] = value, true, true
	return nil
}

func (s *requests) reverse(key id) error {
	if s.reverseSeen[key] {
		return errors.New("duplicate reverse request")
	}
	s.reverseSeen[key], s.pending[key] = true, true
	return nil
}

func (s *requests) answer(key id) error {
	if !s.pending[key] {
		return errors.New("unknown or duplicate reverse answer")
	}
	delete(s.pending, key)
	return nil
}

func (s *requests) response(key id) (rawjson.Value, error) {
	if !s.active[key] {
		return rawjson.Value{}, errors.New("response without pending request")
	}
	delete(s.active, key)
	return s.bindings[key], nil
}

func (s *requests) complete() error {
	if len(s.active) != 0 || len(s.pending) != 0 {
		return fmt.Errorf("unfinished protocol requests (client=%d server=%d)", len(s.active), len(s.pending))
	}
	return nil
}

func match(expected, actual rawjson.Value) error {
	if !rawjson.Equal(expected, actual) {
		return errors.New("client mismatch")
	}
	return nil
}
