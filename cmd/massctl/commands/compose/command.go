package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	"github.com/zoumo/mass/pkg/workspace"
)

// NewCommand returns the "compose" cobra command.
func NewCommand(getClient cliutil.ClientFn) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "compose",
		Short: "Create workspace and agent runs from a declarative config file",
		Long: `compose reads a workspace-compose YAML file and creates the workspace and all agent runs.
It waits for the workspace to be ready and each agent to reach idle state,
then prints the run socket path for each agent.

Example config (kind: workspace-compose):
  kind: workspace-compose
  meta
    name: mass-e2e
  spec:
    source:
      type: local
      path: /path/to/project
    agents:
      - metadata:
          name: codex
        spec:
          agent: codex
      - meta
          name: claude-code
        spec:
          agent: claude`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(file)
			if err != nil {
				return fmt.Errorf("reading config %q: %w", file, err)
			}
			cfg, err := parseConfig(data)
			if err != nil {
				return err
			}

			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			ctx := context.Background()
			wsName := cfg.Metadata.Name
			if err := createWorkspace(ctx, client, cfg); err != nil {
				return err
			}
			if err := waitWorkspaceReady(ctx, client, wsName); err != nil {
				return err
			}
			for _, a := range cfg.Spec.Agents {
				if err := createAgentRun(ctx, client, wsName, a); err != nil {
					return err
				}
			}
			for _, a := range cfg.Spec.Agents {
				if err := waitAgentIdle(ctx, client, wsName, a.Metadata.Name); err != nil {
					return err
				}
			}

			fmt.Println("\nAll agents are ready. Socket info:")
			for _, a := range cfg.Spec.Agents {
				printSocketInfo(ctx, client, wsName, a.Metadata.Name)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "Path to workspace-compose YAML file (required)")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

func createWorkspace(ctx context.Context, client pkgariapi.Client, cfg Config) error {
	src, err := buildSource(cfg.Spec.Source)
	if err != nil {
		return err
	}
	srcJSON, err := json.Marshal(src)
	if err != nil {
		return fmt.Errorf("marshal source: %w", err)
	}
	ws := pkgariapi.Workspace{
		Metadata: pkgariapi.ObjectMeta{Name: cfg.Metadata.Name},
		Spec:     pkgariapi.WorkspaceSpec{Source: srcJSON},
	}
	if err := client.Create(ctx, &ws); err != nil {
		return fmt.Errorf("workspace/create: %w", err)
	}
	fmt.Printf("Workspace %q created (phase: %s)\n", ws.Metadata.Name, ws.Status.Phase)
	return nil
}

func waitWorkspaceReady(ctx context.Context, client pkgariapi.Client, name string) error {
	fmt.Printf("Waiting for workspace %q to be ready...\n", name)
	for {
		time.Sleep(500 * time.Millisecond)
		var ws pkgariapi.Workspace
		if err := client.Get(ctx, pkgariapi.ObjectKey{Name: name}, &ws); err != nil {
			return fmt.Errorf("workspace/get: %w", err)
		}
		switch ws.Status.Phase {
		case pkgariapi.WorkspacePhaseReady:
			fmt.Printf("Workspace %q is ready (path: %s)\n", name, ws.Status.Path)
			return nil
		case pkgariapi.WorkspacePhaseError:
			return fmt.Errorf("workspace %q entered error state", name)
		}
	}
}

func createAgentRun(ctx context.Context, client pkgariapi.Client, wsName string, a AgentRunEntry) error {
	ar := pkgariapi.AgentRun{
		Metadata: pkgariapi.ObjectMeta{
			Workspace: wsName,
			Name:      a.Metadata.Name,
		},
		Spec: a.Spec,
	}
	if err := client.Create(ctx, &ar); err != nil {
		return fmt.Errorf("agentrun/create %q: %w", a.Metadata.Name, err)
	}
	fmt.Printf("Agent run %q/%q created (state: %s)\n", wsName, a.Metadata.Name, ar.Status.State)
	return nil
}

func waitAgentIdle(ctx context.Context, client pkgariapi.Client, wsName, agName string) error {
	fmt.Printf("Waiting for agent %q/%q to be idle...\n", wsName, agName)
	for {
		time.Sleep(500 * time.Millisecond)
		var ar pkgariapi.AgentRun
		if err := client.Get(ctx, pkgariapi.ObjectKey{Workspace: wsName, Name: agName}, &ar); err != nil {
			return fmt.Errorf("agentrun/get %q: %w", agName, err)
		}
		switch ar.Status.State {
		case "idle":
			fmt.Printf("Agent %q/%q is idle\n", wsName, agName)
			return nil
		case "error":
			return fmt.Errorf("agent %q/%q entered error state: %s", wsName, agName, ar.Status.ErrorMessage)
		case "stopped":
			return fmt.Errorf("agent %q/%q stopped unexpectedly", wsName, agName)
		}
	}
}

func printSocketInfo(ctx context.Context, client pkgariapi.Client, wsName, agName string) {
	var ar pkgariapi.AgentRun
	if err := client.Get(ctx, pkgariapi.ObjectKey{Workspace: wsName, Name: agName}, &ar); err != nil {
		fmt.Printf("  %s/%s: (get failed: %v)\n", wsName, agName, err)
		return
	}
	if ar.Status.Run != nil && ar.Status.Run.SocketPath != "" {
		fmt.Printf("  %s/%s: %s\n", wsName, agName, ar.Status.Run.SocketPath)
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
