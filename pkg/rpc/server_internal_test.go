package rpc

import (
	"encoding/json"
	"testing"

	"github.com/sourcegraph/jsonrpc2"
	"github.com/stretchr/testify/require"
)

func TestUnmarshalParams_MissingParams(t *testing.T) {
	var dst SessionPromptParams
	err := unmarshalParams(&jsonrpc2.Request{}, &dst)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing params")
}

func TestUnmarshalParams_DecodesPrompt(t *testing.T) {
	raw := json.RawMessage(`{"prompt":"hello"}`)
	req := &jsonrpc2.Request{Params: &raw}

	var dst SessionPromptParams
	err := unmarshalParams(req, &dst)
	require.NoError(t, err)
	require.Equal(t, "hello", dst.Prompt)
}
