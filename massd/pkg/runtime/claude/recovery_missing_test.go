package claude

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"
	agentclaude "github.com/zoumo/agent-sdk-go/agent/claude"
	nativeclaude "github.com/zoumo/agent-sdk-go/claude"
)

func TestClaudeRecoveryMissingScope(t *testing.T) {
	const id = "6b565466-d8ba-405f-bd5d-360c40885b87"
	for _, tc := range []struct {
		name                         string
		config, cwd, projects, scope bool
		invalidTranscript, missing   bool
	}{
		{name: "projects root missing", config: true, cwd: true, missing: true},
		{name: "workspace project missing", config: true, cwd: true, projects: true, missing: true},
		{name: "transcript missing", config: true, cwd: true, projects: true, scope: true, missing: true},
		{name: "configured directory missing", cwd: true},
		{name: "working directory missing", config: true, projects: true},
		{name: "working directory and projects missing", config: true},
		{name: "transcript is not a file", config: true, cwd: true, projects: true, scope: true, invalidTranscript: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			config, cwd := filepath.Join(root, "config"), filepath.Join(root, "work")
			if tc.config {
				require.NoError(t, os.Mkdir(config, 0o700))
			}
			if tc.cwd {
				require.NoError(t, os.Mkdir(cwd, 0o700))
			}
			projects := filepath.Join(config, "projects")
			if tc.projects {
				require.NoError(t, os.Mkdir(projects, 0o700))
			}
			scope := filepath.Join(projects, regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(cwd, "-"))
			if tc.scope {
				require.NoError(t, os.Mkdir(scope, 0o700))
			}
			if tc.invalidTranscript {
				require.NoError(t, os.Mkdir(filepath.Join(scope, id+".jsonl"), 0o700))
			}
			_, err = nativeclaude.GetSessionInfo(t.Context(), id, nativeclaude.SessionInfoOptions{
				Scope: nativeclaude.SessionQueryScope{ConfigDir: config, ProjectDir: cwd},
			})
			var query *nativeclaude.SessionQueryError
			require.ErrorAs(t, err, &query)
			err = &agent.Error{Code: agent.ErrorFailedPrecondition, Reason: agentclaude.ErrorReasonResumePreflightQueryFailed, Cause: err}
			require.Equal(t, tc.missing, claudeRecoveryMissing(err, id, config, cwd))
		})
	}
}
