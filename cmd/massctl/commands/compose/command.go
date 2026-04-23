package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	"github.com/zoumo/mass/pkg/workspace"
)

// NewCommand returns the "compose" cobra command with apply and run subcommands.
func NewCommand(getClient cliutil.ClientFn) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compose",
		Short: "Declarative workspace and agent-run management",
	}
	cmd.AddCommand(
		newApplyCmd(getClient),
		newRunCmd(getClient),
	)
	return cmd
}

// ────────────────────────────────────────────────────────────────────────────
// Shared helpers used by both apply and run subcommands
// ────────────────────────────────────────────────────────────────────────────

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

// ensureWorkspace reuses an existing ready workspace or creates a new one.
func ensureWorkspace(ctx context.Context, client pkgariapi.Client, wsName string, src SourceConfig) error {
	var ws pkgariapi.Workspace
	if err := client.Get(ctx, pkgariapi.ObjectKey{Name: wsName}, &ws); err == nil {
		if ws.Status.Phase == pkgariapi.WorkspacePhaseReady {
			fmt.Printf("Workspace %q already exists (reusing, path: %s)\n", wsName, ws.Status.Path)
			return nil
		}
		// Exists but not yet ready — wait.
		return waitWorkspaceReady(ctx, client, wsName)
	}
	// Not found — create new.
	cfg := Config{
		Metadata: ConfigMetadata{Name: wsName},
		Spec:     WorkspaceComposeSpec{Source: src},
	}
	if err := createWorkspace(ctx, client, cfg); err != nil {
		return err
	}
	return waitWorkspaceReady(ctx, client, wsName)
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
			Name:      a.Name,
		},
		Spec: pkgariapi.AgentRunSpec{
			Agent:        a.Agent,
			SystemPrompt: a.SystemPrompt,
			Permissions:  a.Permissions,
			McpServers:   a.McpServers,
		},
	}
	if err := client.Create(ctx, &ar); err != nil {
		return fmt.Errorf("agentrun/create %q: %w", a.Name, err)
	}
	fmt.Printf("Agent run %q/%q created (state: %s)\n", wsName, a.Name, ar.Status.Status)
	return nil
}


func printSocketInfo(ctx context.Context, client pkgariapi.Client, wsName, agName string) {
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
