package cliutil

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	ariclient "github.com/zoumo/mass/pkg/ari/client"
	"github.com/zoumo/mass/pkg/workspace"
)

// CreateWorkspace creates a workspace via the ARI client and prints status.
func CreateWorkspace(ctx context.Context, client ariclient.Client, name string, src workspace.Source) (*pkgariapi.Workspace, error) {
	if src.Type == workspace.SourceTypeLocal && !filepath.IsAbs(src.Local.Path) {
		abs, err := filepath.Abs(src.Local.Path)
		if err != nil {
			return nil, fmt.Errorf("resolve local path: %w", err)
		}
		src.Local.Path = abs
	}

	srcJSON, err := json.Marshal(src)
	if err != nil {
		return nil, fmt.Errorf("marshal source: %w", err)
	}
	ws := pkgariapi.Workspace{
		Metadata: pkgariapi.ObjectMeta{Name: name},
		Spec:     pkgariapi.WorkspaceSpec{Source: srcJSON},
	}
	if err := client.Create(ctx, &ws); err != nil {
		return nil, fmt.Errorf("workspace/create: %w", err)
	}
	fmt.Printf("Workspace %q created (phase: %s)\n", ws.Metadata.Name, ws.Status.Phase)
	return &ws, nil
}

// WaitWorkspaceReady polls until the workspace reaches Ready or Error phase.
func WaitWorkspaceReady(ctx context.Context, client ariclient.Client, name string) error {
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

// EnsureWorkspace reuses an existing ready workspace or creates a new one and
// waits for it to become ready.
func EnsureWorkspace(ctx context.Context, client ariclient.Client, name string, src workspace.Source) error {
	var ws pkgariapi.Workspace
	if err := client.Get(ctx, pkgariapi.ObjectKey{Name: name}, &ws); err == nil {
		if ws.Status.Phase == pkgariapi.WorkspacePhaseReady {
			fmt.Printf("Workspace %q already exists (reusing, path: %s)\n", name, ws.Status.Path)
			return nil
		}
		return WaitWorkspaceReady(ctx, client, name)
	}
	if _, err := CreateWorkspace(ctx, client, name, src); err != nil {
		return err
	}
	return WaitWorkspaceReady(ctx, client, name)
}

// CreateAgentRun validates the workflow file (if set), creates an agent run
// via the ARI client, and prints status.
func CreateAgentRun(ctx context.Context, client ariclient.Client, ar *pkgariapi.AgentRun) error {
	if ar.Spec.WorkflowFile != "" {
		abs, err := ResolveFilePath(ar.Spec.WorkflowFile)
		if err != nil {
			return fmt.Errorf("workflow: %w", err)
		}
		ar.Spec.WorkflowFile = abs
	}
	if err := client.Create(ctx, ar); err != nil {
		return fmt.Errorf("agentrun/create %q: %w", ar.Metadata.Name, err)
	}
	fmt.Printf("Agent run %q/%q created (state: %s)\n", ar.Metadata.Workspace, ar.Metadata.Name, ar.Status.Phase)
	return nil
}
