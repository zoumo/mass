package integration_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The SDK validates saved native identity before starting a resume process.
// These constructed rows are protocol fixtures, never recorded Agent evidence.
func prepareClaudeSavedSession(t *testing.T, config, cwd, id string) {
	t.Helper()
	project := filepath.Join(config, "projects", regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(cwd, "-"))
	require.NoError(t, os.MkdirAll(project, 0o700))
	f, err := os.Create(filepath.Join(project, id+".jsonl"))
	require.NoError(t, err)
	for _, row := range ticket07Rows() {
		row["sessionId"] = id
		row["cwd"] = cwd
		require.NoError(t, json.NewEncoder(f).Encode(row))
	}
	require.NoError(t, f.Close())
}

func replaceClaudeScenarioSessionID(t *testing.T, path, id string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	out := filepath.Join(t.TempDir(), "native-session-scenario.json")
	require.NoError(t, os.WriteFile(out, []byte(strings.ReplaceAll(string(data), `"claude-session"`, `"`+id+`"`)), 0o600))
	return out
}
