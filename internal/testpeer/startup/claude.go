// Package startup describes the permission exchange expected by test peers
// that exercise MASS's unified SDK entrypoints.
package startup

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/zoumo/mass/internal/testpeer/rawjson"
)

const (
	ClaudeFullAccessMode    = "bypassPermissions"
	ClaudeFullAccessSandbox = `{"enabled":false}`
	ClaudeAutoReviewMode    = "auto"
	ClaudeAutoReviewSandbox = `{"enabled":true,"failIfUnavailable":true,"allowUnsandboxedCommands":false}`
)

func ClaudeSettings(sandbox string) map[string]any {
	return map[string]any{"effective": map[string]any{"sandbox": json.RawMessage(sandbox)}}
}

// ValidateClaudePermissionArgs checks the SDK's startup mode and sandbox.
func ValidateClaudePermissionArgs(args []string, mode, sandbox string) error {
	modeSeen, sandboxSeen := false, false
	for _, arg := range args {
		switch {
		case strings.HasPrefix(arg, "--permission-mode="):
			if modeSeen || arg != "--permission-mode="+mode {
				return errors.New("expected one Claude permission mode: " + mode)
			}
			modeSeen = true
		case strings.HasPrefix(arg, "--settings="):
			settings, err := rawjson.Parse([]byte(strings.TrimPrefix(arg, "--settings=")))
			expected, parseErr := rawjson.Parse([]byte(`{"sandbox":` + sandbox + `}`))
			if sandboxSeen || err != nil || parseErr != nil || !rawjson.Equal(settings, expected) {
				return errors.New("expected one Claude sandbox settings object: " + sandbox)
			}
			sandboxSeen = true
		}
	}
	if !modeSeen || !sandboxSeen {
		return errors.New("missing Claude startup permission mode or sandbox settings")
	}
	return nil
}
