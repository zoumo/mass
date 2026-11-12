package jsonrpc

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseRequestProfile(t *testing.T) {
	tests := []struct {
		name string
		data string
		kind Kind
		code int64
		id   string
	}{
		{name: "string id", data: `{"jsonrpc":"2.0","id":"a","method":"initialize","params":{}}`, kind: Request, id: `"a"`},
		{name: "zero id", data: `{"jsonrpc":"2.0","id":0,"method":"initialize"}`, kind: Request, id: "0"},
		{name: "max id", data: `{"jsonrpc":"2.0","id":9007199254740991,"method":"initialize"}`, kind: Request, id: "9007199254740991"},
		{name: "notification", data: `{"jsonrpc":"2.0","method":"initialize"}`, kind: Notification},
		{name: "null id", data: `{"jsonrpc":"2.0","id":null,"method":"initialize"}`, code: -32600},
		{name: "negative id", data: `{"jsonrpc":"2.0","id":-1,"method":"initialize"}`, code: -32600},
		{name: "fraction id", data: `{"jsonrpc":"2.0","id":1.5,"method":"initialize"}`, code: -32600},
		{name: "exponent id", data: `{"jsonrpc":"2.0","id":1e3,"method":"initialize"}`, code: -32600},
		{name: "unsafe id", data: `{"jsonrpc":"2.0","id":9007199254740992,"method":"initialize"}`, code: -32600},
		{name: "boolean id", data: `{"jsonrpc":"2.0","id":true,"method":"initialize"}`, code: -32600},
		{name: "empty string id", data: `{"jsonrpc":"2.0","id":"","method":"initialize"}`, code: -32600},
		{name: "batch", data: `[{"jsonrpc":"2.0","id":"a","method":"initialize"}]`, code: -32600},
		{name: "wrong version", data: `{"jsonrpc":"1.0","id":"a","method":"initialize"}`, code: -32600, id: `"a"`},
		{name: "duplicate key", data: `{"jsonrpc":"2.0","id":"a","id":"b","method":"initialize"}`, code: -32600},
		{name: "positional params", data: `{"jsonrpc":"2.0","id":"a","method":"initialize","params":[]}`, code: -32602, id: `"a"`},
		{name: "null params", data: `{"jsonrpc":"2.0","id":"a","method":"initialize","params":null}`, code: -32602, id: `"a"`},
		{name: "extra field", data: `{"jsonrpc":"2.0","id":"a","method":"initialize","extra":true}`, code: -32600, id: `"a"`},
		{name: "malformed", data: `{"jsonrpc":`, code: -32700},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message, err := Parse([]byte(test.data), RoleServer)
			if test.code != 0 {
				require.NotNil(t, err)
				require.Equal(t, test.code, err.Code)
				require.Equal(t, test.id, string(err.ID))
				return
			}
			require.Nil(t, err)
			require.Equal(t, test.kind, message.Kind)
			require.Equal(t, test.id, string(message.ID))
		})
	}
}

func TestParseNotificationErrorsAreSuppressed(t *testing.T) {
	_, err := Parse([]byte(`{"jsonrpc":"2.0","method":"initialize","params":[]}`), RoleServer)
	require.NotNil(t, err)
	require.True(t, err.Suppress)
}

func TestParseResponseProfile(t *testing.T) {
	valid := []string{
		`{"jsonrpc":"2.0","id":"a","result":null}`,
		`{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":"1"}}`,
		`{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"Invalid Request"}}`,
	}
	for _, data := range valid {
		message, err := Parse([]byte(data), RoleClient)
		require.Nil(t, err, data)
		require.Equal(t, Response, message.Kind)
	}
	invalid := []string{
		`{"jsonrpc":"2.0","id":"a"}`,
		`{"jsonrpc":"2.0","id":"a","result":null,"error":{"code":-32600,"message":"wrong"}}`,
		`{"jsonrpc":"2.0","id":"a","result":null,"extra":true}`,
		`{"jsonrpc":"2.0","id":"a","error":{"code":"bad","message":"wrong"}}`,
		`{"jsonrpc":"2.0","id":"a","error":{"code":-32600}}`,
		`[{"jsonrpc":"2.0","id":"a","result":null}]`,
	}
	for _, data := range invalid {
		_, err := Parse([]byte(data), RoleClient)
		require.NotNil(t, err, data)
	}
	_, err := Parse([]byte(`{"jsonrpc":"2.0","id":"a","method":"initialize"}`), RoleClient)
	require.NotNil(t, err)
	require.True(t, err.Suppress)
}

func TestDecodeStrictPreservesNumbersAndRejectsUnknownFields(t *testing.T) {
	var value struct {
		Number any `json:"number"`
	}
	require.NoError(t, DecodeStrict([]byte(`{"number":9007199254740993}`), &value))
	number, ok := value.Number.(json.Number)
	require.True(t, ok)
	require.Equal(t, "9007199254740993", number.String())

	var unknown struct {
		Known bool `json:"known"`
	}
	require.Error(t, DecodeStrict([]byte(`{"known":true,"unknown":true}`), &unknown))
}
