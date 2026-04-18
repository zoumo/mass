// Package agentd implements the agent daemon that manages agent runtime sessions.
// This file defines the Options struct for --root-based path derivation.
package agentd

import (
	"fmt"
	"os"
	"path/filepath"
)

// DefaultRoot returns the default root directory for mass data.
// It resolves to $HOME/.mass at runtime.
func DefaultRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".mass")
	}
	return filepath.Join(home, ".mass")
}

// Options holds the root-path-based configuration for the mass daemon.
// All derived paths are computed from Root, eliminating the need for a
// config.yaml file.
type Options struct {
	// Root is the base directory for all mass-managed files.
	// Defaults to DefaultRoot() ($HOME/.mass) if not overridden.
	Root string
}

// Validate returns an error if Options contains invalid or missing fields.
func (o Options) Validate() error {
	if o.Root == "" {
		return fmt.Errorf("mass: Root must not be empty")
	}
	return nil
}

// SocketPath returns the Unix socket path for the ARI JSON-RPC server.
func (o Options) SocketPath() string {
	return filepath.Join(o.Root, "mass.sock")
}

// WorkspaceRoot returns the root directory for workspace creation.
func (o Options) WorkspaceRoot() string {
	return filepath.Join(o.Root, "workspaces")
}

// BundleRoot returns the root directory for agent bundle creation.
// Each agent gets a subdirectory: <BundleRoot>/<workspace>-<name>/.
func (o Options) BundleRoot() string {
	return filepath.Join(o.Root, "bundles")
}

// MetaDBPath returns the path to the bbolt metadata database file.
func (o Options) MetaDBPath() string {
	return filepath.Join(o.Root, "mass.db")
}
