// Package agentd implements the agent daemon that manages agent runtime sessions.
// This file defines the Config struct and YAML parsing for agentd configuration.
package agentd

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// RuntimeConfig defines runtime behavior for agent sessions.
type RuntimeConfig struct {
	// DefaultClass is the default runtime class for sessions without explicit class.
	DefaultClass string `yaml:"defaultClass"`

	// TimeoutSeconds is the default session timeout in seconds.
	TimeoutSeconds int `yaml:"timeoutSeconds"`
}

// SessionPolicyConfig defines session lifecycle policies.
type SessionPolicyConfig struct {
	// MaxSessions is the maximum number of concurrent sessions.
	MaxSessions int `yaml:"maxSessions"`

	// IdleTimeoutSeconds is the idle timeout before session cleanup.
	IdleTimeoutSeconds int `yaml:"idleTimeoutSeconds"`

	// AutoCleanup enables automatic cleanup of idle sessions.
	AutoCleanup bool `yaml:"autoCleanup"`
}

// CapabilitiesConfig defines capability flags for a runtime class.
type CapabilitiesConfig struct {
	// Streaming enables real-time output streaming (default: true).
	Streaming bool `yaml:"streaming"`

	// SessionLoad enables session load/restore capability (default: false).
	SessionLoad bool `yaml:"sessionLoad"`

	// ConcurrentSessions is the max concurrent sessions per runtime class (default: 1).
	ConcurrentSessions int `yaml:"concurrentSessions"`
}

// RuntimeClassConfig defines a specific runtime class configuration.
type RuntimeClassConfig struct {
	// Command is the executable command for this runtime class (required).
	Command string `yaml:"command"`

	// Args are command-line arguments for the runtime class (optional).
	Args []string `yaml:"args,omitempty"`

	// Env is environment variables for this class (optional).
	Env map[string]string `yaml:"env,omitempty"`

	// Capabilities defines runtime capabilities (optional, defaults applied).
	Capabilities CapabilitiesConfig `yaml:"capabilities"`
}

// Config is the agentd daemon configuration loaded from config.yaml.
type Config struct {
	// Socket is the Unix socket path for ARI JSON-RPC server.
	Socket string `yaml:"socket"`

	// WorkspaceRoot is the root directory for workspace creation.
	WorkspaceRoot string `yaml:"workspaceRoot"`

	// BundleRoot is the root directory for agent bundle creation.
	// Each agent gets a subdirectory: <BundleRoot>/<workspace>-<name>/
	// The bundle directory also serves as the shim state directory, so the
	// socket and state files (agent-shim.sock, state.json, events.jsonl) all
	// live in the same place.
	// Defaults to WorkspaceRoot if not specified.
	BundleRoot string `yaml:"bundleRoot"`

	// MetaDB is the path to the metadata database file (SQLite or similar).
	MetaDB string `yaml:"metaDB"`

	// Runtime defines default runtime behavior.
	Runtime RuntimeConfig `yaml:"runtime"`

	// SessionPolicy defines session lifecycle policies.
	SessionPolicy SessionPolicyConfig `yaml:"sessionPolicy"`

	// RuntimeClasses maps class name to RuntimeClassConfig.
	RuntimeClasses map[string]RuntimeClassConfig `yaml:"runtimeClasses"`
}

// ParseConfig reads a YAML configuration file and returns a Config struct.
// Returns error if:
//   - file doesn't exist
//   - file cannot be read
//   - YAML parsing fails
//   - required fields are missing (Socket, WorkspaceRoot)
func ParseConfig(path string) (Config, error) {
	// Check if file exists.
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return Config{}, fmt.Errorf("config file not found: %s", path)
		}
		return Config{}, fmt.Errorf("cannot access config file %s: %w", path, err)
	}

	// Read file contents.
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("cannot read config file %s: %w", path, err)
	}

	// Parse YAML.
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("cannot parse config file %s: %w", path, err)
	}

	// Validate required fields.
	if cfg.Socket == "" {
		return Config{}, fmt.Errorf("config missing required field: socket")
	}
	if cfg.WorkspaceRoot == "" {
		return Config{}, fmt.Errorf("config missing required field: workspaceRoot")
	}
	if cfg.BundleRoot == "" {
		cfg.BundleRoot = cfg.WorkspaceRoot
	}

	return cfg, nil
}
