package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVerifyClaudeResumeHistory(t *testing.T) {
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	config := t.TempDir()
	id := "00000000-0000-4000-8000-000000000001"
	key := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, cwd)
	dir := filepath.Join(config, "projects", key)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	row, err := json.Marshal(map[string]any{
		"type":       "user",
		"uuid":       "message",
		"parentUuid": nil,
		"sessionId":  id,
		"cwd":        cwd,
		"message":    map[string]any{"role": "user", "content": "hello"},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, id+".jsonl"), append(row, '\n'), 0o600))

	env := []string{"CLAUDE_CONFIG_DIR=" + config}
	verified, err := verifyClaudeResumeHistory(context.Background(), env, id, cwd)
	require.NoError(t, err)
	require.True(t, verified)
	verified, err = verifyClaudeResumeHistory(context.Background(), nil, id, cwd)
	require.NoError(t, err)
	require.False(t, verified)
	verified, err = verifyClaudeResumeHistory(context.Background(), env, id, t.TempDir())
	require.NoError(t, err)
	require.False(t, verified)
	verified, err = verifyClaudeResumeHistory(context.Background(), env, "00000000-0000-4000-8000-000000000002", cwd)
	require.NoError(t, err)
	require.False(t, verified)
	verified, err = verifyClaudeResumeHistory(context.Background(), env, "claude-session", cwd)
	require.NoError(t, err)
	require.False(t, verified)

	corruptID := "00000000-0000-4000-8000-000000000003"
	require.NoError(t, os.WriteFile(filepath.Join(dir, corruptID+".jsonl"), []byte("{\n"), 0o600))
	verified, err = verifyClaudeResumeHistory(context.Background(), env, corruptID, cwd)
	require.Error(t, err)
	require.False(t, verified)
}
