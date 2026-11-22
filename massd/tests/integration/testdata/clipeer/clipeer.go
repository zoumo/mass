// Package clipeer holds the synthetic Claude CLI protocol helpers shared by
// integration peers. Nothing here starts an Agent, calls a model, or reads an
// account.
package clipeer

import (
	"fmt"
	"os"

	"github.com/zoumo/agent-sdk-go/claude"

	"github.com/zoumo/mass/internal/testpeer/startup"
)

// VersionNumber is the stable native version these peers advertise.
const VersionNumber = claude.MinimumCLIVersion

// VersionOutput is the native --version line the SDK preflight parses.
const VersionOutput = VersionNumber + " (Claude Code)"

// Version answers the SDK's stable-version preflight and reports whether the
// process should exit.
func Version() bool {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(VersionOutput)
		return true
	}
	return false
}

// AwaitingClaim reports whether the SDK parked this CLI for claim_session.
func AwaitingClaim() bool {
	for _, arg := range os.Args[1:] {
		if arg == "--await-claim" {
			return true
		}
	}
	return false
}

// ClaimReply returns the control response for a claim_session request. The
// reply reports the identity and directory this synthetic CLI was bound to.
func ClaimReply(frame map[string]any, sessionID string) (map[string]any, bool) {
	if frame["type"] != "control_request" {
		return nil, false
	}
	request, ok := frame["request"].(map[string]any)
	if !ok || request["subtype"] != "claim_session" {
		return nil, false
	}
	return map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": frame["request_id"],
			"response": map[string]any{
				"status":     "ok",
				"session_id": sessionID,
				"cwd":        request["cwd"],
			},
		},
	}, true
}

func StartupReply(subtype string, id any) map[string]any {
	body := map[string]any{}
	switch subtype {
	case "initialize":
		body["current_permission_mode"] = startup.ClaudePermissionMode
	case "get_settings":
		body = startup.ClaudeSettings()
	}
	return map[string]any{
		"type": "control_response",
		"response": map[string]any{"subtype": "success", "request_id": id, "response": body},
	}
}

// StatusReply reports the peer's own bound identity for the SDK's read-only
// resume probe. Callers must pass the same identity their native frames use.
func StatusReply(frame map[string]any, sessionID, cwd string) (map[string]any, bool) {
	request, ok := frame["request"].(map[string]any)
	if frame["type"] != "control_request" || !ok || request["subtype"] != "get_status" {
		return nil, false
	}
	return map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype": "success", "request_id": frame["request_id"],
			"response": map[string]any{"sections": []any{map[string]any{"rows": []any{
				map[string]any{"label": "Session ID", "value": sessionID},
				map[string]any{"label": "cwd", "value": cwd},
			}}}},
		},
	}, true
}
