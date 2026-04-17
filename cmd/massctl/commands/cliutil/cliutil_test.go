package cliutil

import (
	"bytes"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOutputJSON_Struct(t *testing.T) {
	r, w, err := os.Pipe()
	require.NoError(t, err)

	origStdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	type sample struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	OutputJSON(sample{Name: "test", Count: 42})

	w.Close()
	var buf bytes.Buffer
	_, err = buf.ReadFrom(r)
	require.NoError(t, err)

	expected := "{\n  \"name\": \"test\",\n  \"count\": 42\n}\n"
	assert.Equal(t, expected, buf.String())
}

func TestOutputJSON_Nil(t *testing.T) {
	r, w, err := os.Pipe()
	require.NoError(t, err)

	origStdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	OutputJSON(nil)

	w.Close()
	var buf bytes.Buffer
	_, err = buf.ReadFrom(r)
	require.NoError(t, err)

	assert.Equal(t, "null\n", buf.String())
}

func TestOutputJSON_Map(t *testing.T) {
	r, w, err := os.Pipe()
	require.NoError(t, err)

	origStdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	OutputJSON(map[string]string{"key": "value"})

	w.Close()
	var buf bytes.Buffer
	_, err = buf.ReadFrom(r)
	require.NoError(t, err)

	expected := "{\n  \"key\": \"value\"\n}\n"
	assert.Equal(t, expected, buf.String())
}
