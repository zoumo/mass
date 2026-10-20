package events

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"time"
	"unicode/utf8"
)

// ContentBlock keeps the public union intact. Adapters may reject unsupported kinds.
//
//nolint:recvcheck // JSON decoding mutates; event encoding accepts immutable values.
type ContentBlock struct {
	Type   string `json:"type"`
	Text   string `json:"text"`
	fields map[string]any
}

func Text(text string) ContentBlock { return ContentBlock{Type: "text", Text: text} }
func (b ContentBlock) MarshalJSON() ([]byte, error) {
	if b.fields != nil {
		if err := CheckDynamicJSON(b.fields); err != nil {
			return nil, err
		}
		return json.Marshal(b.fields)
	}
	if b.Type != "text" {
		return nil, errors.New("content fields unavailable")
	}
	return json.Marshal(struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{b.Type, b.Text})
}

func (b *ContentBlock) UnmarshalJSON(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var fields map[string]any
	if d.Decode(&fields) != nil || fields == nil {
		return errors.New("invalid content block")
	}
	kind, _ := fields["type"].(string)
	allowed := map[string]bool{"type": true, "annotations": true}
	required := []string{}
	switch kind {
	case "text":
		allowed["text"] = true
		required = []string{"text"}
	case "image", "audio":
		allowed["data"], allowed["mimeType"] = true, true
		required = []string{"data", "mimeType"}
		if kind == "image" {
			allowed["uri"] = true
		}
	case "resource_link":
		for _, k := range []string{"uri", "name", "mimeType", "title", "description", "size"} {
			allowed[k] = true
		}
		required = []string{"uri", "name"}
	case "resource":
		allowed["resource"] = true
	default:
		return errors.New("invalid content type")
	}
	for k, v := range fields {
		if !allowed[k] {
			return errors.New("unknown content field")
		}
		if k != "annotations" && k != "resource" && k != "size" {
			if _, ok := v.(string); !ok {
				return errors.New("invalid content field")
			}
		}
	}
	for _, k := range required {
		if _, ok := fields[k]; !ok {
			return errors.New("missing content field")
		}
	}
	if data, ok := fields["data"].(string); ok {
		if _, err := base64.StdEncoding.Strict().DecodeString(data); err != nil {
			return errors.New("invalid media Base64")
		}
	}
	if size, ok := fields["size"]; ok {
		n, ok := size.(json.Number)
		if !ok {
			return errors.New("invalid size")
		}
		v, valid := new(big.Rat).SetString(string(n))
		if !valid || !v.IsInt() || v.Sign() < 0 {
			return errors.New("invalid size")
		}
	}
	if kind == "resource" {
		r, ok := fields["resource"].(map[string]any)
		if !ok {
			return errors.New("invalid embedded resource")
		}
		_, text := r["text"]
		_, blob := r["blob"]
		if text == blob {
			return errors.New("resource must contain text or blob")
		}
		if value, ok := r["blob"].(string); ok {
			if _, err := base64.StdEncoding.Strict().DecodeString(value); err != nil {
				return errors.New("invalid resource Base64")
			}
		}
		if _, ok := r["uri"].(string); !ok {
			return errors.New("missing resource URI")
		}
		for k, v := range r {
			if k != "uri" && k != "mimeType" && k != "text" && k != "blob" {
				return errors.New("unknown resource field")
			}
			if _, ok := v.(string); !ok {
				return errors.New("invalid resource field")
			}
		}
	}
	if value, ok := fields["annotations"]; ok {
		a, ok := value.(map[string]any)
		if !ok {
			return errors.New("invalid annotations")
		}
		for k, v := range a {
			switch k {
			case "audience":
				values, ok := v.([]any)
				if !ok {
					return errors.New("invalid audience")
				}
				for _, v := range values {
					if v != "user" && v != "assistant" {
						return errors.New("invalid audience")
					}
				}
			case "priority":
				n, ok := v.(json.Number)
				if !ok {
					return errors.New("invalid priority")
				}
				x, e := n.Float64()
				if e != nil || x < 0 || x > 1 {
					return errors.New("invalid priority")
				}
			case "lastModified":
				s, ok := v.(string)
				if !ok {
					return errors.New("invalid timestamp")
				}
				if _, e := time.Parse(time.RFC3339Nano, s); e != nil {
					return errors.New("invalid timestamp")
				}
			default:
				return errors.New("unknown annotation")
			}
		}
	}
	*b = ContentBlock{Type: kind, fields: fields}
	b.Text, _ = fields["text"].(string)
	if kind == "text" && len(fields) == 2 {
		b.fields = nil
	}
	return nil
}

// Validate checks the same public union used by the JSON decoder, including
// directly constructed Go values before encoding can replace invalid UTF-8.
func (b ContentBlock) Validate() error {
	if !utf8.ValidString(b.Type) || !utf8.ValidString(b.Text) {
		return errors.New("invalid content UTF-8")
	}
	raw, err := b.MarshalJSON()
	if err != nil {
		return err
	}
	var parsed ContentBlock
	return parsed.UnmarshalJSON(raw)
}
