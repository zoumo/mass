package recordingtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAuditConsumerClaudeCaptureWithoutArgv(t *testing.T) {
	const (
		model     = "synthetic-model"
		project   = "/synthetic-project"
		sessionID = "19391939-1939-4939-8939-193919391939"
		request   = `"request":{"subtype":"claim_session","cwd":"` + project + `"}`
	)
	const frames = `
{"direction":"c2s","wire":{"type":"control_request","request_id":"init","request":{"subtype":"initialize"}}}
{"direction":"s2c","wire":{"type":"control_response","response":{"subtype":"success","request_id":"init","response":{"current_permission_mode":"auto"}}}}
{"direction":"c2s","wire":{"type":"control_request","request_id":"claim",` + request + `}}
{"direction":"s2c","wire":{"type":"control_response","response":{"subtype":"success","request_id":"claim","response":{"status":"ok","session_id":"` + sessionID + `","cwd":"` + project + `"}}}}
{"direction":"c2s","wire":{"type":"control_request","request_id":"settings","request":{"subtype":"get_settings"}}}
{"direction":"s2c","wire":{"type":"control_response","response":{"subtype":"success","request_id":"settings","response":{"effective":{"sandbox":{"enabled":true,"failIfUnavailable":true,"allowUnsandboxedCommands":false}}}}}}
{"direction":"c2s","wire":{"type":"user","message":{"role":"user","content":"hello"}}}
{"direction":"s2c","wire":{"type":"system","subtype":"init","session_id":"` + sessionID + `","model":"` + model + `","cwd":"` + project + `"}}
{"direction":"s2c","wire":{"type":"assistant","session_id":"` + sessionID + `","message":{"content":[{"type":"text","text":"hello"}]}}}
{"direction":"s2c","wire":{"type":"result","subtype":"success","session_id":"` + sessionID + `"}}
`
	material := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(material, "raw"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(material, "manifest.json"),
		[]byte(`{"native":{"version":"2.1.293 (Claude Code)"}}`), 0o600))
	rawPath := filepath.Join(material, "raw", "raw.jsonl")
	require.NoError(t, os.WriteFile(rawPath, []byte(frames), 0o600))
	require.NoError(t, auditClaudeCapture(material, model, sessionID, project))
	var manifest captureManifest
	require.NoError(t, readJSONFile(filepath.Join(material, "manifest.json"), &manifest))
	require.ErrorContains(t, auditLegacyClaudeArgs(manifest.Native.Args, model), "missing the explicit claim or model argument")

	for _, tc := range []struct {
		name, old, replacement, want string
	}{
		{
			name: "caller selects identity",
			old:  request, replacement: strings.TrimSuffix(request, "}") + `,"session_id":""}`,
			want: "carries a session identity",
		},
		{
			name: "native claim identity differs",
			old:  `"session_id":"` + sessionID + `"`, replacement: `"session_id":"other-session"`,
			want: "did not confirm the captured session",
		},
		{
			name: "native init model differs",
			old:  `"model":"` + model + `"`, replacement: `"model":"other-model"`,
			want: "native Claude init identity differs",
		},
		{
			name: "native work fails",
			old:  `"type":"result","subtype":"success"`, replacement: `"type":"result","subtype":"error"`,
			want: "native Claude result did not succeed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := strings.Replace(frames, tc.old, tc.replacement, 1)
			require.NoError(t, os.WriteFile(rawPath, []byte(raw), 0o600))
			require.ErrorContains(t, auditClaudeCapture(material, model, sessionID, project), tc.want)
		})
	}
}
