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
		{name: "empty string id", data: `{"jsonrpc":"2.0","id":"","method":"initialize"}`, kind: Request, id: `""`},
		{name: "zero id", data: `{"jsonrpc":"2.0","id":0,"method":"initialize"}`, kind: Request, id: "0"},
		{name: "max id", data: `{"jsonrpc":"2.0","id":9007199254740991,"method":"initialize"}`, kind: Request, id: "9007199254740991"},
		{name: "notification", data: `{"jsonrpc":"2.0","method":"initialize"}`, kind: Notification},
		{name: "null id", data: `{"jsonrpc":"2.0","id":null,"method":"initialize"}`, code: -32600},
		{name: "negative id", data: `{"jsonrpc":"2.0","id":-1,"method":"initialize"}`, code: -32600},
		{name: "fraction id", data: `{"jsonrpc":"2.0","id":1.5,"method":"initialize"}`, code: -32600},
		{name: "exponent id", data: `{"jsonrpc":"2.0","id":1e3,"method":"initialize"}`, code: -32600},
		{name: "unsafe id", data: `{"jsonrpc":"2.0","id":9007199254740992,"method":"initialize"}`, code: -32600},
		{name: "boolean id", data: `{"jsonrpc":"2.0","id":true,"method":"initialize"}`, code: -32600},
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

func TestParseParamsPreservesValidJSONStringEscapes(t *testing.T) {
	for _, params := range []string{
		`{"text":"普通中文"}`,
		`{"text":"line\nnext"}`,
		`{"text":"\uD83D\uDE00"}`,
		`{"text":"say \"hello\""}`,
		`{"text":"C:\\work\\file"}`,
	} {
		message, err := Parse([]byte(`{"jsonrpc":"2.0","id":"1","method":"call","params":`+params+`}`), RoleServer)
		require.Nil(t, err, params)
		require.Equal(t, Request, message.Kind)
		require.JSONEq(t, params, string(message.Params))
	}
}

func TestParseParamsRejectsInvalidUnicodeEscapes(t *testing.T) {
	for name, params := range map[string]string{
		"isolated high surrogate": `{"text":"\uD83D"}`,
		"isolated low surrogate":  `{"text":"\uDE00"}`,
		"high then text":          `{"text":"\uD83Dx"}`,
		"high then non-low":       `{"text":"\uD83D\u0041"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(`{"jsonrpc":"2.0","id":"1","method":"call","params":`+params+`}`), RoleServer)
			require.NotNil(t, err)
			require.Equal(t, int64(-32602), err.Code)
			require.Equal(t, `"1"`, string(err.ID))
			require.False(t, err.Suppress)
		})
	}
}

func TestInvalidJSONEscapeSyntaxIsRejected(t *testing.T) {
	for name, params := range map[string]string{
		"invalid hex":      `{"text":"\u12G4"}`,
		"truncated hex":    `{"text":"\u123"}`,
		"truncated escape": `{"text":"\`,
	} {
		t.Run(name, func(t *testing.T) {
			require.False(t, validJSONStringEscapes([]byte(params)))
			_, err := Parse([]byte(`{"jsonrpc":"2.0","id":"1","method":"call","params":`+params+`}`), RoleServer)
			require.NotNil(t, err)
			require.Equal(t, int64(-32700), err.Code)
		})
	}
}

func TestParseNotificationSuppressesInvalidUnicodeEscapeError(t *testing.T) {
	_, err := Parse([]byte(`{"jsonrpc":"2.0","method":"call","params":{"text":"\uD83D"}}`), RoleServer)
	require.NotNil(t, err)
	require.Equal(t, int64(-32602), err.Code)
	require.True(t, err.Suppress)
}

func TestParseRejectsInvalidUnicodeEscapesAcrossMessage(t *testing.T) {
	tests := []struct {
		name     string
		data     string
		role     Role
		code     int64
		id       string
		suppress bool
	}{
		{
			name: "request method",
			data: `{"jsonrpc":"2.0","id":"1","method":"runtime.\uD800list"}`,
			role: RoleServer,
			code: -32600,
			id:   `"1"`,
		},
		{
			name: "request string id",
			data: `{"jsonrpc":"2.0","id":"id-\uD800","method":"initialize"}`,
			role: RoleServer,
			code: -32600,
		},
		{
			name: "request params",
			data: `{"jsonrpc":"2.0","id":"1","method":"call","params":{"text":"\uD800"}}`,
			role: RoleServer,
			code: -32602,
			id:   `"1"`,
		},
		{
			name:     "response result",
			data:     `{"jsonrpc":"2.0","id":"1","result":{"text":"\uD800"}}`,
			role:     RoleClient,
			code:     -32600,
			id:       `"1"`,
			suppress: true,
		},
		{
			name:     "response error message",
			data:     `{"jsonrpc":"2.0","id":"1","error":{"code":-32000,"message":"\uD800"}}`,
			role:     RoleClient,
			code:     -32600,
			id:       `"1"`,
			suppress: true,
		},
		{
			name:     "response error data",
			data:     `{"jsonrpc":"2.0","id":"1","error":{"code":-32000,"message":"bad","data":{"text":"\uD800"}}}`,
			role:     RoleClient,
			code:     -32600,
			id:       `"1"`,
			suppress: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(test.data), test.role)
			require.NotNil(t, err)
			require.Equal(t, test.code, err.Code)
			require.Equal(t, test.id, string(err.ID))
			require.Equal(t, test.suppress, err.Suppress)
		})
	}
}

func TestParsePreservesLosslessUnicodeAcrossMessage(t *testing.T) {
	tests := []struct {
		name string
		data string
		role Role
	}{
		{
			name: "request method and id",
			data: `{"jsonrpc":"2.0","id":"普通-\uD83D\uDE00","method":"调用-\uD83D\uDE00"}`,
			role: RoleServer,
		},
		{
			name: "response result",
			data: `{"jsonrpc":"2.0","id":"1","result":{"text":"普通\n\uD83D\uDE00\"C:\\work"}}`,
			role: RoleClient,
		},
		{
			name: "response error",
			data: `{"jsonrpc":"2.0","id":"1","error":{"code":-32000,"message":"普通\n\uD83D\uDE00","data":{"text":"C:\\work"}}}`,
			role: RoleClient,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(test.data), test.role)
			require.Nil(t, err)
		})
	}
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
	message, err := Parse([]byte(`{"jsonrpc":"2.0","method":"session.event","params":{"subscriptionId":"1"}}`), RoleClient)
	require.Nil(t, err)
	require.Equal(t, Notification, message.Kind)
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
	_, err = Parse([]byte(`{"jsonrpc":"2.0","id":"a","method":"initialize"}`), RoleClient)
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
