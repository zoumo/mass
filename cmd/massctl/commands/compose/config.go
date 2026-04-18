// Package compose implements the "massctl compose" subcommand for declarative workspace setup.
package compose

import (
	"fmt"

	"sigs.k8s.io/yaml"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

// Config is the top-level document for `massctl compose`.
// kind must be "workspace-compose".
// All structs use json tags — sigs.k8s.io/yaml unmarshals YAML via json tags.
type Config struct {
	Kind     string               `json:"kind"`
	Metadata ConfigMetadata       `json:"metadata"`
	Spec     WorkspaceComposeSpec `json:"spec"`
}

// ConfigMetadata holds the workspace name (and future labels/annotations).
type ConfigMetadata struct {
	Name string `json:"name"`
}

// WorkspaceComposeSpec describes the workspace source and the agent runs to create.
type WorkspaceComposeSpec struct {
	Source SourceConfig    `json:"source"`
	Agents []AgentRunEntry `json:"agents"`
}

// SourceConfig describes the workspace source (local, git, or emptyDir).
type SourceConfig struct {
	Type string `json:"type"`
	// local
	Path string `json:"path,omitempty"`
	// git
	URL string `json:"url,omitempty"`
	Ref string `json:"ref,omitempty"`
}

// AgentRunEntry describes a single agent run following the metadata/spec pattern.
type AgentRunEntry struct {
	Metadata AgentRunMetadata       `json:"metadata"`
	Spec     pkgariapi.AgentRunSpec `json:"spec"`
}

// AgentRunMetadata holds the agent run's name within the workspace.
type AgentRunMetadata struct {
	Name string `json:"name"`
}

// parseConfig parses and validates YAML bytes into a Config.
func parseConfig(data []byte) (Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	if err := validateConfig(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func validateConfig(cfg Config) error {
	if cfg.Kind != "workspace-compose" {
		return fmt.Errorf("kind must be %q, got %q", "workspace-compose", cfg.Kind)
	}
	if cfg.Metadata.Name == "" {
		return fmt.Errorf("metadata.name is required")
	}
	for i, a := range cfg.Spec.Agents {
		if a.Metadata.Name == "" {
			return fmt.Errorf("spec.agents[%d].metadata.name is required", i)
		}
		if a.Spec.Agent == "" {
			return fmt.Errorf("spec.agents[%d].spec.agent is required", i)
		}
	}
	return nil
}
