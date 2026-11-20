// Package startup describes the permission exchange expected by test peers
// that exercise MASS's unified SDK entrypoints.
package startup

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/zoumo/mass/mass-go/internal/testpeer/rawjson"
)

const (
	ClaudePermissionMode = "auto"
	ClaudeSandbox        = `{"enabled":true,"failIfUnavailable":true,"allowUnsandboxedCommands":false}`
)

func ClaudeSettings() map[string]any {
	return map[string]any{"effective": map[string]any{"sandbox": json.RawMessage(ClaudeSandbox)}}
}

// ValidateClaudePermissionArgs checks the SDK's auto-review startup options.
func ValidateClaudePermissionArgs(args []string) error {
	modeSeen, sandboxSeen := false, false
	for _, arg := range args {
		switch {
		case strings.HasPrefix(arg, "--permission-mode="):
			if modeSeen || arg != "--permission-mode="+ClaudePermissionMode {
				return errors.New("expected one Claude auto permission mode")
			}
			modeSeen = true
		case strings.HasPrefix(arg, "--settings="):
			settings, err := rawjson.Parse([]byte(strings.TrimPrefix(arg, "--settings=")))
			expected, parseErr := rawjson.Parse([]byte(`{"sandbox":` + ClaudeSandbox + `}`))
			if sandboxSeen || err != nil || parseErr != nil || !rawjson.Equal(settings, expected) {
				return errors.New("expected one Claude approval sandbox settings object")
			}
			sandboxSeen = true
		}
	}
	if !modeSeen || !sandboxSeen {
		return errors.New("missing Claude startup permission mode or sandbox settings")
	}
	return nil
}
