package up

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/zoumo/oar/api"
	ari "github.com/zoumo/oar/api/ari"
	"github.com/zoumo/oar/cmd/agentdctl/subcommands/cliutil"
	ariclient "github.com/zoumo/oar/pkg/ari"
	"github.com/zoumo/oar/pkg/workspace"
)

// NewCommand returns the "up" cobra command.
func NewCommand(getClient cliutil.ClientFn) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Create workspace and agent runs from a declarative config file",
		Long: `up reads a workspace-up YAML file and creates the workspace and all agent runs.
It waits for the workspace to be ready and each agent to reach idle state,
then prints the shim socket path for each agent.

Example config (kind: workspace-up):
  kind: workspace-up
  meta
    name: agentd-e2e
  spec:
    source:
      type: local
      path: /path/to/project
    agents:
      - metadata:
          name: codex
        spec:
          agent: codex
      - metadata:
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

			wsName := cfg.Metadata.Name
			if err := createWorkspace(client, cfg); err != nil {
				return err
			}
			if err := waitWorkspaceReady(client, wsName); err != nil {
				return err
			}
			for _, a := range cfg.Spec.Agents {
				if err := createAgentRun(client, wsName, a); err != nil {
					return err
				}
			}
			for _, a := range cfg.Spec.Agents {
				if err := waitAgentIdle(client, wsName, a.Metadata.Name); err != nil {
					return err
				}
			}

			fmt.Println("\nAll agents are ready. Attach info:")
			for _, a := range cfg.Spec.Agents {
				printAttachInfo(client, wsName, a.Metadata.Name)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "Path to workspace-up YAML file (required)")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

func createWorkspace(client *ariclient.Client, cfg Config) error {
	src, err := buildSource(cfg.Spec.Source)
	if err != nil {
		return err
	}
	srcJSON, err := json.Marshal(src)
	if err != nil {
		return fmt.Errorf("marshal source: %w", err)
	}
	params := ari.WorkspaceCreateParams{Name: cfg.Metadata.Name, Source: srcJSON}
	var result ari.WorkspaceCreateResult
	if err := client.Call(api.MethodWorkspaceCreate, params, &result); err != nil {
		return fmt.Errorf("workspace/create: %w", err)
	}
	fmt.Printf("Workspace %q created (phase: %s)\n", result.Workspace.Metadata.Name, result.Workspace.Status.Phase)
	return nil
}

func waitWorkspaceReady(client *ariclient.Client, name string) error {
	fmt.Printf("Waiting for workspace %q to be ready...\n", name)
	for {
		time.Sleep(500 * time.Millisecond)
		var result ari.WorkspaceStatusResult
		if err := client.Call(api.MethodWorkspaceStatus, ari.WorkspaceStatusParams{Name: name}, &result); err != nil {
			return fmt.Errorf("workspace/status: %w", err)
		}
		switch result.Workspace.Status.Phase {
		case "ready":
			fmt.Printf("Workspace %q is ready (path: %s)\n", name, result.Workspace.Status.Path)
			return nil
		case "error":
			return fmt.Errorf("workspace %q entered error state", name)
		}
	}
}

func createAgentRun(client *ariclient.Client, wsName string, a AgentRunEntry) error {
	params := ari.AgentRunCreateParams{
		Workspace:     wsName,
		Name:          a.Metadata.Name,
		Agent:         a.Spec.Agent,
		RestartPolicy: a.Spec.RestartPolicy,
		SystemPrompt:  a.Spec.SystemPrompt,
	}
	var result ari.AgentRunCreateResult
	if err := client.Call(api.MethodAgentRunCreate, params, &result); err != nil {
		return fmt.Errorf("agentrun/create %q: %w", a.Metadata.Name, err)
	}
	fmt.Printf("Agent run %q/%q created (state: %s)\n", wsName, a.Metadata.Name, result.AgentRun.Status.State)
	return nil
}

func waitAgentIdle(client *ariclient.Client, wsName, agName string) error {
	fmt.Printf("Waiting for agent %q/%q to be idle...\n", wsName, agName)
	for {
		time.Sleep(500 * time.Millisecond)
		var result ari.AgentRunStatusResult
		if err := client.Call(api.MethodAgentRunStatus, ari.AgentRunStatusParams{Workspace: wsName, Name: agName}, &result); err != nil {
			return fmt.Errorf("agentrun/status %q: %w", agName, err)
		}
		switch result.AgentRun.Status.State {
		case "idle":
			fmt.Printf("Agent %q/%q is idle\n", wsName, agName)
			return nil
		case "error":
			return fmt.Errorf("agent %q/%q entered error state: %s", wsName, agName, result.AgentRun.Status.ErrorMessage)
		case "stopped":
			return fmt.Errorf("agent %q/%q stopped unexpectedly", wsName, agName)
		}
	}
}

func printAttachInfo(client *ariclient.Client, wsName, agName string) {
	var result ari.AgentRunAttachResult
	if err := client.Call(api.MethodAgentRunAttach, ari.AgentRunAttachParams{Workspace: wsName, Name: agName}, &result); err != nil {
		fmt.Printf("  %s/%s: (attach failed: %v)\n", wsName, agName, err)
		return
	}
	fmt.Printf("  %s/%s: %s\n", wsName, agName, result.SocketPath)
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
