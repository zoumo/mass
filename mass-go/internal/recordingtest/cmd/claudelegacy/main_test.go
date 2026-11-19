package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLegacyInputChecksOnlyTheSubmissionUUID(t *testing.T) {
	const control = `{"type":"control_request","request_id":"go_1","request":{"subtype":"initialize","hooks":null}}`
	const user = `{"type":"user","uuid":"a4b5135b-5da0-4ed5-95fc-d27ad5bf5b4d","message":{"role":"user","content":"hello"},"extra":1.0}`
	var forwarded bytes.Buffer
	require.NoError(t, forwardLegacyInput(strings.NewReader(control+"\n"+user+"\n"), &forwarded))
	lines := strings.Split(strings.TrimSuffix(forwarded.String(), "\n"), "\n")
	require.Len(t, lines, 2)
	require.Equal(t, control, lines[0])                                                                   //nolint:testifylint // Unmodified control bytes are required.
	require.Equal(t, `{"extra":1.0,"message":{"role":"user","content":"hello"},"type":"user"}`, lines[1]) //nolint:testifylint // Verify the number lexeme is preserved.

	for _, malformed := range []string{
		`{"type":"user"}`,
		strings.ReplaceAll(user, "a4b5135b", "A4B5135B"),
		strings.ReplaceAll(user, "-4ed5-", "-1ed5-"),
		user + "\n" + user,
		control,
	} {
		require.Error(t, forwardLegacyInput(strings.NewReader(malformed+"\n"), &forwarded))
	}
}
