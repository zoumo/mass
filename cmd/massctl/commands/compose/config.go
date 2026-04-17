// Package compose implements the "massctl compose" subcommand for declarative workspace setup.
package compose

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// Config is the top-level document for `massctl compose`.
// kind must be "workspace-compose".
type Config struct {
	Kind     string               `yaml:"kind"`
	Metadata ConfigMetadata       `yaml:"metadata"`
	Spec     WorkspaceComposeSpec `yaml:"spec"`
}

// ConfigMetadata holds the workspace name (and future labels/annotations).
type ConfigMetadata struct {
	Name string `yaml:"name"`
}

// WorkspaceComposeSpec describes the workspace source and the agent runs to create.
type WorkspaceComposeSpec struct {
	Source SourceConfig    `yaml:"source"`
	Agents []AgentRunEntry `yaml:"agents"`
}

// SourceConfig describes the workspace source (local, git, or emptyDir).
type SourceConfig struct {
	Type string `yaml:"type"`
	// local
	Path string `yaml:"path,omitempty"`
	// git
	URL string `yaml:"url,omitempty"`
	Ref string `yaml:"ref,omitempty"`
}

// AgentRunEntry describes a single agent run following the metadata/spec pattern.
type AgentRunEntry struct {
	Metadata AgentRunMetadata `yaml:"metadata"`
	Spec     AgentRunSpec     `yaml:"spec"`
}

// AgentRunMetadata holds the agent run's name within the workspace.
type AgentRunMetadata struct {
	Name string `yaml:"name"`
}

// AgentRunSpec describes the desired agent run configuration.
type AgentRunSpec struct {
	Agent         string `yaml:"agent"`
	RestartPolicy string `yaml:"restartPolicy,omitempty"`
	SystemPrompt  string `yaml:"systemPrompt,omitempty"`
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
