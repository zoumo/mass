// Package rawjson validates JSON and edits selected object fields without
// serializing the surrounding message. Values are immutable and retain numbers
// as lexemes. Object comparison ignores key order; output never does.
package rawjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"unicode/utf8"
)

type Kind byte

const (
	Invalid Kind = iota
	Object
	Array
	String
	Number
	Boolean
	Null
)

type node struct {
	kind       Kind
	start, end int
	fields     map[string]*node
	keys       []string
	items      []*node
}

// Value owns its source bytes. The zero value is invalid, distinct from null.
type Value struct {
	data []byte
	n    *node
}

// Parse rejects malformed JSON, invalid Unicode and duplicate decoded member names
// at every depth. It never converts a JSON number to a floating point value.
func Parse(data []byte) (Value, error) {
	if !utf8.Valid(data) || !json.Valid(data) {
		return Value{}, errors.New("invalid JSON or UTF-8")
	}
	p := parser{data: bytes.Clone(data)}
	n, err := p.value()
	if err != nil {
		return Value{}, err
	}
	n.start, n.end = 0, len(p.data)
	return Value{p.data, n}, nil
}

func (v Value) Kind() Kind {
	if v.n == nil {
		return Invalid
	}
	return v.n.kind
}

func (v Value) Raw() []byte {
	if v.n == nil {
		return nil
	}
	return bytes.Clone(v.data[v.n.start:v.n.end])
}

func (v Value) Member(key string) (Value, bool) {
	if v.Kind() != Object {
		return Value{}, false
	}
	n, ok := v.n.fields[key]
	if !ok {
		return Value{}, false
	}
	return Value{v.data, n}, true
}

func (v Value) Keys() []string {
	if v.Kind() != Object {
		return nil
	}
	return append([]string(nil), v.n.keys...)
}

func (v Value) Elements() []Value {
	if v.Kind() != Array {
		return nil
	}
	out := make([]Value, len(v.n.items))
	for i, n := range v.n.items {
		out[i] = Value{v.data, n}
	}
	return out
}

func (v Value) String() (string, error) {
	if v.Kind() != String {
		return "", errors.New("JSON string required")
	}
	var s string
	err := json.Unmarshal(v.Raw(), &s)
	return s, err
}

func (v Value) Int64() (int64, error) {
	if v.Kind() != Number {
		return 0, errors.New("JSON integer required")
	}
	return strconv.ParseInt(string(bytes.TrimSpace(v.Raw())), 10, 64)
}

func (v Value) Bool() (bool, error) {
	if v.Kind() != Boolean {
		return false, errors.New("JSON boolean required")
	}
	return bytes.Equal(bytes.TrimSpace(v.Raw()), []byte("true")), nil
}

// Equal compares all fields and array positions. String escape spelling is not
// significant; number spelling and type are (1, 1.0, true and "1" differ).
func Equal(a, b Value) bool {
	if a.Kind() != b.Kind() || a.Kind() == Invalid {
		return false
	}
	switch a.Kind() {
	case Object:
		if len(a.n.fields) != len(b.n.fields) {
			return false
		}
		for k, n := range a.n.fields {
			m, ok := b.n.fields[k]
			if !ok || !Equal(Value{a.data, n}, Value{b.data, m}) {
				return false
			}
		}
		return true
	case Array:
		if len(a.n.items) != len(b.n.items) {
			return false
		}
		for i, n := range a.n.items {
			if !Equal(Value{a.data, n}, Value{b.data, b.n.items[i]}) {
				return false
			}
		}
		return true
	case String:
		x, _ := a.String()
		y, _ := b.String()
		return x == y
	default:
		return bytes.Equal(bytes.TrimSpace(a.Raw()), bytes.TrimSpace(b.Raw()))
	}
}

// Replace changes exactly one existing object member path. It neither adds a
// member nor matches other fields with the same name. The original is unchanged.
func (v Value) Replace(path []string, replacement Value) (Value, error) {
	if v.Kind() == Invalid || replacement.Kind() == Invalid || len(path) == 0 {
		return Value{}, errors.New("value, replacement and nonempty member path required")
	}
	target := v
	for _, key := range path {
		next, ok := target.Member(key)
		if !ok {
			return Value{}, fmt.Errorf("missing object member %q", key)
		}
		target = next
	}
	data := append([]byte(nil), v.data[v.n.start:target.n.start]...)
	data = append(data, replacement.Raw()...)
	data = append(data, v.data[target.n.end:v.n.end]...)
	return Parse(data)
}

// parser runs only after json.Valid. It indexes token spans and detects decoded
// duplicate keys; syntax checking remains the standard library's responsibility.
type parser struct {
	data []byte
	pos  int
}

func (p *parser) space() {
	for p.pos < len(p.data) && bytes.ContainsRune([]byte(" \t\r\n"), rune(p.data[p.pos])) {
		p.pos++
	}
}

func (p *parser) stringEnd() error {
	p.pos++
	for p.data[p.pos] != '"' {
		if p.data[p.pos] == '\\' {
			p.pos++
			if p.data[p.pos] == 'u' {
				code, _ := strconv.ParseUint(string(p.data[p.pos+1:p.pos+5]), 16, 16)
				p.pos += 5
				if code >= 0xd800 && code <= 0xdbff {
					if p.pos+6 > len(p.data) || string(p.data[p.pos:p.pos+2]) != `\u` {
						return errors.New("unpaired Unicode surrogate")
					}
					low, err := strconv.ParseUint(string(p.data[p.pos+2:p.pos+6]), 16, 16)
					if err != nil || low < 0xdc00 || low > 0xdfff {
						return errors.New("unpaired Unicode surrogate")
					}
					p.pos += 6
				} else if code >= 0xdc00 && code <= 0xdfff {
					return errors.New("unpaired Unicode surrogate")
				}
				continue
			}
		}
		p.pos++
	}
	p.pos++
	return nil
}

func (p *parser) value() (*node, error) {
	p.space()
	n := &node{start: p.pos}
	switch p.data[p.pos] {
	case '{':
		n.kind = Object
		n.fields = make(map[string]*node)
		p.pos++
		p.space()
		for p.data[p.pos] != '}' {
			start := p.pos
			if err := p.stringEnd(); err != nil {
				return nil, err
			}
			var key string
			_ = json.Unmarshal(p.data[start:p.pos], &key)
			if _, ok := n.fields[key]; ok {
				return nil, fmt.Errorf("duplicate JSON member %q", key)
			}
			p.space()
			p.pos++
			child, err := p.value()
			if err != nil {
				return nil, err
			}
			n.fields[key] = child
			n.keys = append(n.keys, key)
			p.space()
			if p.data[p.pos] != ',' {
				break
			}
			p.pos++
			p.space()
		}
		p.pos++
	case '[':
		n.kind = Array
		p.pos++
		p.space()
		for p.data[p.pos] != ']' {
			child, err := p.value()
			if err != nil {
				return nil, err
			}
			n.items = append(n.items, child)
			p.space()
			if p.data[p.pos] != ',' {
				break
			}
			p.pos++
			p.space()
		}
		p.pos++
	case '"':
		n.kind = String
		if err := p.stringEnd(); err != nil {
			return nil, err
		}
	default:
		n.kind = Number
		if p.data[p.pos] == 't' || p.data[p.pos] == 'f' {
			n.kind = Boolean
		}
		if p.data[p.pos] == 'n' {
			n.kind = Null
		}
		for p.pos < len(p.data) && !bytes.ContainsRune([]byte(" \r\n\t,]}"), rune(p.data[p.pos])) {
			p.pos++
		}
	}
	n.end = p.pos
	return n, nil
}
