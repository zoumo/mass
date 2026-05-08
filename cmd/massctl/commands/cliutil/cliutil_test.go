package cliutil

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrintJSON(t *testing.T) {
	type sample struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}

	var buf bytes.Buffer
	require.NoError(t, PrintJSON(&buf, sample{Name: "test", Count: 42}))

	expected := "{\n  \"name\": \"test\",\n  \"count\": 42\n}\n"
	assert.Equal(t, expected, buf.String())
}

func TestPrintJSONNil(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, PrintJSON(&buf, nil))

	assert.Equal(t, "null\n", buf.String())
}

func TestToAnySlice(t *testing.T) {
	got := ToAnySlice([]int{1, 2, 3})

	assert.Equal(t, []any{1, 2, 3}, got)
}
