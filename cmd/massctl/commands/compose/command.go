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
