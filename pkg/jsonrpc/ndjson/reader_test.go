package ndjson_test

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/pkg/jsonrpc/ndjson"
)

type msg struct {
	Name string `json:"name"`
}

func TestDecode_ValidLines(t *testing.T) {
	input := `{"name":"alice"}
{"name":"bob"}
`
	r := ndjson.NewReader(strings.NewReader(input))

	var m msg
	require.NoError(t, r.Decode(&m))
	assert.Equal(t, "alice", m.Name)

	require.NoError(t, r.Decode(&m))
	assert.Equal(t, "bob", m.Name)

	assert.ErrorIs(t, r.Decode(&m), io.EOF)
}

func TestDecode_SkipsEmptyLines(t *testing.T) {
	input := "\n\n{\"name\":\"alice\"}\n\n\n{\"name\":\"bob\"}\n\n"
	r := ndjson.NewReader(strings.NewReader(input))

	var m msg
	require.NoError(t, r.Decode(&m))
	assert.Equal(t, "alice", m.Name)

	require.NoError(t, r.Decode(&m))
	assert.Equal(t, "bob", m.Name)

	assert.ErrorIs(t, r.Decode(&m), io.EOF)
}

func TestDecode_InvalidLineReturnsError(t *testing.T) {
	input := "{\"name\":\"alice\"}\nthis is not json\n{\"name\":\"bob\"}\n"
	r := ndjson.NewReader(strings.NewReader(input))

	var m msg
	// First line: valid
	require.NoError(t, r.Decode(&m))
	assert.Equal(t, "alice", m.Name)

	// Second line: invalid — returns error but stream is still usable
	err := r.Decode(&m)
	require.Error(t, err)
	require.ErrorIs(t, err, ndjson.ErrInvalidJSON)

	var lineErr *ndjson.InvalidLineError
	require.ErrorAs(t, err, &lineErr)
	assert.Equal(t, []byte("this is not json"), lineErr.Line)

	// Third line: valid — stream recovered
	require.NoError(t, r.Decode(&m))
	assert.Equal(t, "bob", m.Name)
}

func TestDecode_LastLineWithoutNewline(t *testing.T) {
	input := `{"name":"alice"}`
	r := ndjson.NewReader(strings.NewReader(input))

	var m msg
	require.NoError(t, r.Decode(&m))
	assert.Equal(t, "alice", m.Name)

	assert.ErrorIs(t, r.Decode(&m), io.EOF)
}

func TestDecode_InvalidLastLineWithoutNewline(t *testing.T) {
	input := `{"name":"alice"}
garbage`
	r := ndjson.NewReader(strings.NewReader(input))

	var m msg
	require.NoError(t, r.Decode(&m))
	assert.Equal(t, "alice", m.Name)

	err := r.Decode(&m)
	require.Error(t, err)
	require.ErrorIs(t, err, ndjson.ErrInvalidJSON)
}

func TestDecode_LargeLine(t *testing.T) {
	// Build a JSON object larger than the default 64KB buffer
	bigValue := strings.Repeat("x", 128*1024)
	input := `{"name":"` + bigValue + "\"}\n"
	r := ndjson.NewReader(strings.NewReader(input))

	var m msg
	require.NoError(t, r.Decode(&m))
	assert.Equal(t, bigValue, m.Name)
}

func TestDecode_EmptyInput(t *testing.T) {
	r := ndjson.NewReader(strings.NewReader(""))
	var m msg
	assert.ErrorIs(t, r.Decode(&m), io.EOF)
}

func TestDecode_WhitespaceOnlyInput(t *testing.T) {
	r := ndjson.NewReader(strings.NewReader("   \n  \n\n"))
	var m msg
	assert.ErrorIs(t, r.Decode(&m), io.EOF)
}

func TestInvalidLineError_Error(t *testing.T) {
	input := "not-json\n"
	r := ndjson.NewReader(strings.NewReader(input))
	var m msg
	err := r.Decode(&m)
	require.Error(t, err)

	var lineErr *ndjson.InvalidLineError
	require.ErrorAs(t, err, &lineErr)

	errMsg := lineErr.Error()
	assert.Contains(t, errMsg, "ndjson: invalid JSON line")
	assert.Contains(t, errMsg, "not-json")
}

func TestInvalidLineError_Error_Truncation(t *testing.T) {
	// Line longer than 200 chars gets truncated in Error() output.
	longLine := strings.Repeat("x", 300)
	input := longLine + "\n"
	r := ndjson.NewReader(strings.NewReader(input))
	var m msg
	err := r.Decode(&m)
	require.Error(t, err)

	var lineErr *ndjson.InvalidLineError
	require.ErrorAs(t, err, &lineErr)

	errMsg := lineErr.Error()
	assert.Contains(t, errMsg, "...(truncated)")
	assert.LessOrEqual(t, len(errMsg), 350)
}

func TestInvalidLineError_Unwrap(t *testing.T) {
	input := "not-json\n"
	r := ndjson.NewReader(strings.NewReader(input))
	var m msg
	err := r.Decode(&m)
	require.Error(t, err)

	var lineErr *ndjson.InvalidLineError
	require.ErrorAs(t, err, &lineErr)

	// Unwrap should return the underlying json parse error.
	unwrapped := lineErr.Unwrap()
	require.Error(t, unwrapped)
	assert.Contains(t, unwrapped.Error(), "invalid character")
}
