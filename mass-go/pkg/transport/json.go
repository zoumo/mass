package transport

import (
	"bytes"
	"encoding/json"
	"io"
)

type createMetadata struct {
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name" minLength:"1" maxLength:"253" pattern:"^[a-z0-9]([-a-z0-9]*[a-z0-9])?([.][a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"`
}

// ValidateJSON rejects duplicate members at every depth, including open maps.
func ValidateJSON(b []byte) error { return validateJSON(b) }

func validateJSON(b []byte) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if jsonValue(d) != nil {
		return Invalid()
	}
	if _, err := d.Token(); err != io.EOF {
		return Invalid()
	}
	return nil
}

func jsonValue(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := key.(string)
			if !ok || seen[s] {
				return Invalid()
			}
			seen[s] = true
			if err := jsonValue(d); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return Invalid()
		}
	case '[':
		for d.More() {
			if err := jsonValue(d); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return Invalid()
		}
	default:
		return Invalid()
	}
	return nil
}
