package jsonrpc

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateLosslessRejectsLossyValues(t *testing.T) {
	type nested struct {
		Value string `json:"value"`
	}
	tests := map[string]any{
		"string":         "bad-\xff",
		"pointer":        &nested{Value: "bad-\xff"},
		"slice":          []string{"ok", "bad-\xff"},
		"map value":      map[string]string{"ok": "bad-\xff"},
		"map key":        map[string]string{"bad-\xff": "ok"},
		"raw UTF-8":      json.RawMessage(`{"value":"` + string([]byte{0xff}) + `"}`),
		"raw surrogate":  json.RawMessage(`{"value":"\uD800"}`),
		"nested pointer": []any{map[string]any{"nested": &nested{Value: "bad-\xff"}}},
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			require.ErrorIs(t, ValidateLossless(value), ErrLossyJSON)
		})
	}
}

func TestValidateLosslessAcceptsValuesJSONPreserves(t *testing.T) {
	tests := map[string]any{
		"unicode":     "普通 \U0001F600",
		"byte slice":  []byte{0xff, 0x00, 0x01},
		"raw JSON":    json.RawMessage(`{"text":"普通\n\ud83d\ude00"}`),
		"map and key": map[string]string{"普通": "line\nnext"},
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, ValidateLossless(value))
		})
	}
}
