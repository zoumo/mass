package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	liveUUID     = "a4b5135b-5da0-4ed5-95fc-d27ad5bf5b4d"
	capturedUUID = "315f8b4d-081c-4dd6-ae01-db405e0f0d66"
	clientUser   = `{"type":"user","uuid":"` + liveUUID + `","message":{"role":"user","content":"hello"},"extra":1.0}`
)

func TestLegacyInputChecksOnlyTheSubmissionUUID(t *testing.T) {
	const control = `{"type":"control_request","request_id":"go_1","request":{"subtype":"initialize","hooks":null}}`
	var binding replayBinding
	var forwarded bytes.Buffer
	require.NoError(t, binding.forwardInput(strings.NewReader(control+"\n"+clientUser+"\n"), &forwarded))
	lines := strings.Split(strings.TrimSuffix(forwarded.String(), "\n"), "\n")
	require.Len(t, lines, 2)
	require.Equal(t, control, lines[0])                                                                   //nolint:testifylint // Unmodified control bytes are required.
	require.Equal(t, `{"extra":1.0,"message":{"role":"user","content":"hello"},"type":"user"}`, lines[1]) //nolint:testifylint // Verify the number lexeme is preserved.

	for _, malformed := range []string{
		`{"type":"user"}`,
		strings.ReplaceAll(clientUser, "a4b5135b", "A4B5135B"),
		strings.ReplaceAll(clientUser, "-4ed5-", "-1ed5-"),
		clientUser + "\n" + clientUser,
		control,
	} {
		var invalid replayBinding
		require.Error(t, invalid.forwardInput(strings.NewReader(malformed+"\n"), io.Discard))
	}
}

func TestFreshReplayBindsOnlyProvenUserReferences(t *testing.T) {
	binding := replayBinding{captured: capturedUUID}
	var input bytes.Buffer
	require.NoError(t, binding.forwardInput(strings.NewReader(clientUser+"\n"), &input))
	require.Equal(t, strings.ReplaceAll(clientUser, liveUUID, capturedUUID)+"\n", input.String())

	const assistant = `{"type":"assistant","uuid":"` + capturedUUID + `","user_message_uuid":"` + capturedUUID + `","content":{"uuid":"` + capturedUUID + `"}}`
	const echo = `{"type":"user","uuid":"` + capturedUUID + `","user_message_uuid":"` + capturedUUID + `"}`
	const unrelated = `{"type":"result","user_message_uuid":"ba7a10d7-97d5-4f1e-b70a-3a192ae66764"}`
	const unlinked = `{"type":"result","uuid":"` + capturedUUID + `"}`
	var output bytes.Buffer
	require.NoError(t, binding.forwardOutput(strings.NewReader(assistant+"\n"+echo+"\n"+unrelated+"\n"+unlinked+"\n"), &output))
	want := strings.Replace(assistant, `"user_message_uuid":"`+capturedUUID, `"user_message_uuid":"`+liveUUID, 1) + "\n" +
		strings.ReplaceAll(echo, capturedUUID, liveUUID) + "\n" + unrelated + "\n" + unlinked + "\n"
	require.Equal(t, want, output.String())

	unbound := replayBinding{captured: capturedUUID}
	require.ErrorContains(t, unbound.forwardOutput(strings.NewReader(echo+"\n"), io.Discard), "before user input")
}

func TestWorkspaceBindingOnlyMapsClaimAndMatchingInit(t *testing.T) {
	const workspace = "/test/work space"
	const recordedRequest = "/var/tmp/captured/project"
	const recordedResponse = "/private/var/tmp/captured/project"
	const claim = `{"type":"control_request","request_id":"go_2","request":{"subtype":"claim_session","cwd":"` + workspace + `","append_system_prompt":"` + workspace + `"}}`
	const otherRequest = `{"type":"control_request","request_id":"go_1","request":{"subtype":"initialize","cwd":"` + workspace + `"}}`
	const response = `{"type":"control_response","response":{"request_id":"go_2","subtype":"success","response":{"status":"ok","session_id":"root","cwd":"` + recordedResponse + `","extra":{"cwd":"` + recordedResponse + `"}}}}`
	const nativeInit = `{"type":"system","subtype":"init","session_id":"root","cwd":"` + recordedResponse + `","extra":{"cwd":"` + recordedResponse + `"}}`
	otherResponse := strings.Replace(response, `"go_2"`, `"go_1"`, 1)
	failedResponse := strings.Replace(response, `"success"`, `"error"`, 1)
	otherInit := strings.Replace(nativeInit, `"root"`, `"unrelated"`, 1)
	binding := replayBinding{cwd: workspaceBinding{live: workspace, requestCWD: recordedRequest, responseCWD: recordedResponse, initCWD: recordedResponse}}

	var input bytes.Buffer
	require.NoError(t, binding.forwardInput(strings.NewReader(otherRequest+"\n"+claim+"\n"+clientUser+"\n"), &input))
	lines := strings.Split(strings.TrimSuffix(input.String(), "\n"), "\n")
	require.Len(t, lines, 3)
	require.Equal(t, otherRequest, lines[0])                                          //nolint:testifylint // Preserve the exact bytes of unrelated frames.
	require.Equal(t, strings.Replace(claim, workspace, recordedRequest, 1), lines[1]) //nolint:testifylint // Preserve every byte outside the claim cwd.

	var output bytes.Buffer
	require.NoError(t, binding.forwardOutput(strings.NewReader(otherResponse+"\n"+response+"\n"+nativeInit+"\n"+failedResponse+"\n"+otherInit+"\n"), &output))
	wantResponse := strings.Replace(response, recordedResponse, workspace, 1)
	wantInit := strings.Replace(nativeInit, recordedResponse, workspace, 1)
	require.Equal(t, otherResponse+"\n"+wantResponse+"\n"+wantInit+"\n"+failedResponse+"\n"+otherInit+"\n", output.String())

	incorrectClaim := strings.Replace(claim, workspace, "/wrong", 1)
	require.ErrorContains(t, binding.forwardInput(strings.NewReader(incorrectClaim+"\n"), io.Discard), "must name the live workspace")
	require.ErrorContains(t, binding.forwardInput(strings.NewReader(claim+"\n"), io.Discard), "expects one Claude claim_session")
	incorrectResponse := strings.Replace(response, recordedResponse, "/wrong", 1)
	require.ErrorContains(t, binding.forwardOutput(strings.NewReader(incorrectResponse+"\n"), io.Discard), "must confirm the captured cwd")
	incorrectInit := strings.Replace(nativeInit, recordedResponse, "/wrong", 1)
	require.ErrorContains(t, binding.forwardOutput(strings.NewReader(incorrectInit+"\n"), io.Discard), "must confirm the captured cwd")
}

func TestAdapterRejectsInvalidTailEvenWhenChildAcceptsEOF(t *testing.T) {
	for _, test := range []struct {
		name string
		tail string
	}{
		{"complete input", ""},
		{"unterminated frame", `{"type":"extra"}`},
		{"malformed frame", "invalid\n"},
		{"duplicate user input", clientUser + "\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			receipt := filepath.Join(t.TempDir(), "done")
			args := []string{capturedUUID, receipt, "/bin/sh", "-c", "cat >/dev/null"}
			err := run(args, strings.NewReader(clientUser+"\n"+test.tail), io.Discard, io.Discard)
			if test.tail == "" {
				require.NoError(t, err)
				data, err := os.ReadFile(receipt)
				require.NoError(t, err)
				require.Equal(t, "complete\n", string(data))
			} else {
				require.Error(t, err)
				require.NoFileExists(t, receipt)
			}
		})
	}
}
