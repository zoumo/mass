// Package agentd implements the agent daemon that manages agent runtime sessions.
// This file defines the Options struct for --root-based path derivation.
package agentd

import (
	"fmt"
	"path/filepath"
)

// DefaultRoot is the default root directory for agentd data.
const DefaultRoot = "/var/run/agentd"

// Options holds the root-path-based configuration for the agentd daemon.
// All derived paths are computed from Root, eliminating the need for a
// config.yaml file.
type Options struct {
	// Root is the base directory for all agentd-managed files.
	// Defaults to DefaultRoot ("/var/run/agentd") if not overridden.
	Root string
}

// Validate returns an error if Options contains invalid or missing fields.
func (o Options) Validate() error {
	if o.Root == "" {
		return fmt.Errorf("agentd: Root must not be empty")
	}
	return nil
}

// SocketPath returns the Unix socket path for the ARI JSON-RPC server.
func (o Options) SocketPath() string {
	return filepath.Join(o.Root, "agentd.sock")
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
	return filepath.Join(o.Root, "agentd.db")
}
