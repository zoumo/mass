package claude

import (
	"encoding/json"
	"fmt"
	"os"

	nativeclaude "github.com/zoumo/agent-sdk-go/claude"
)

// controlFrame is the subset of the native control protocol the synthetic
// peers in this package read.
type controlFrame struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Request   struct {
		Subtype string `json:"subtype"`
		CWD     string `json:"cwd"`
	} `json:"request"`
}

// answerVersionProbe satisfies the SDK's stable CLI preflight. Launchers
// forward native argv after "--".
func answerVersionProbe() {
	for _, arg := range os.Args[1:] {
		if arg == "--version" {
			fmt.Fprintln(os.Stdout, nativeclaude.MinimumCLIVersion+" (Claude Code)")
			os.Exit(0)
		}
	}
}

func controlSuccess(enc *json.Encoder, requestID string, response map[string]any) error {
	return enc.Encode(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response":   response,
		},
	})
}

// claimSuccess answers a claim_session request with the identity this
// synthetic CLI assigned, mirroring a real CLI without calling a model.
func claimSuccess(enc *json.Encoder, request controlFrame, sessionID string) error {
	return controlSuccess(enc, request.RequestID, map[string]any{
		"status":     "ok",
		"session_id": sessionID,
		"cwd":        request.Request.CWD,
	})
}
