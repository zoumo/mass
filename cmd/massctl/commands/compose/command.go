package compose

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	ariclient "github.com/zoumo/mass/pkg/ari/client"
	"github.com/zoumo/mass/pkg/workspace"
)

// NewCommand returns the "compose" cobra command with apply and run subcommands.
func NewCommand(getClient cliutil.ClientFn) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compose",
		Short: "Declarative workspace and agent-run management",
		Long:  `Declarative workspace and agent-run management.`,
		Example: `  # Declarative multi-agent setup from a compose file
  massctl compose apply -f compose.yaml

  # Quick-start a single agent (uses current directory as local workspace)
  massctl compose run -w my-ws --agent claude

  # With system prompt
  massctl compose run -w my-ws --agent claude --system-prompt "You are a reviewer"

  # Custom run name
  massctl compose run -w my-ws --agent claude --name my-claude`,
	}
	cmd.AddCommand(
		newApplyCmd(getClient),
		newRunCmd(getClient),
	)
	return cmd
}

func createAgentRun(ctx context.Context, client ariclient.Client, wsName string, a AgentRunEntry) error {
	ar := pkgariapi.AgentRun{
		Metadata: pkgariapi.ObjectMeta{
			Workspace: wsName,
			Name:      a.Name,
		},
		Spec: pkgariapi.AgentRunSpec{
			Agent:        a.Agent,
			SystemPrompt: a.SystemPrompt,
			Permissions:  a.Permissions,
			McpServers:   a.McpServers,
			WorkflowFile: a.WorkflowFile,
		},
	}
	return cliutil.CreateAgentRun(ctx, client, &ar)
}

func printSocketInfo(ctx context.Context, client ariclient.Client, wsName, agName string) {
	var ar pkgariapi.AgentRun
	if err := client.Get(ctx, pkgariapi.ObjectKey{Workspace: wsName, Name: agName}, &ar); err != nil {
		fmt.Printf("  %s/%s: (get failed: %v)\n", wsName, agName, err)
		return
	}
	if ar.Status.SocketPath != "" {
		fmt.Printf("  %s/%s: %s\n", wsName, agName, ar.Status.SocketPath)
	} else {
		fmt.Printf("  %s/%s: (no socket info)\n", wsName, agName)
	}
}

func buildSource(s SourceConfig) (workspace.Source, error) {
	switch s.Type {
	case "local":
		if s.Path == "" {
			return workspace.Source{}, fmt.Errorf("local source requires path")
		}
		return workspace.Source{
			Type:  workspace.SourceTypeLocal,
			Local: workspace.LocalSource{Path: s.Path},
		}, nil
	case "git":
		if s.URL == "" {
			return workspace.Source{}, fmt.Errorf("git source requires url")
		}
		return workspace.Source{
			Type: workspace.SourceTypeGit,
			Git:  workspace.GitSource{URL: s.URL, Ref: s.Ref},
		}, nil
	case "emptyDir":
		return workspace.Source{Type: workspace.SourceTypeEmptyDir}, nil
	default:
		return workspace.Source{}, fmt.Errorf("unknown source type %q (valid: local, git, emptyDir)", s.Type)
	}
}
