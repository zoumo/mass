package pipeline

import apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"

// Pipeline is the top-level pipeline document.
// All structs use json tags because sigs.k8s.io/yaml unmarshals YAML via json tags.
type Pipeline struct {
	Name        string              `json:"name"`
	Description string              `json:"description,omitempty"`
	Workspace   Workspace           `json:"workspace"`
	AgentRuns   map[string]AgentRun `json:"agentRuns"`
	Stages      []Stage             `json:"stages"`
	Output      *OutputConfig       `json:"output,omitempty"`
	Cleanup     *CleanupConfig      `json:"cleanup,omitempty"`
}

type Workspace struct {
	Source WorkspaceSource `json:"source"`
}

type WorkspaceSource struct {
	Type string `json:"type"`
	Path string `json:"path,omitempty"`
	URL  string `json:"url,omitempty"`
	Ref  string `json:"ref,omitempty"`
}

type AgentRun struct {
	Agent        string                      `json:"agent"`
	SystemPrompt string                      `json:"systemPrompt,omitempty"`
	Permissions  apiruntime.PermissionPolicy `json:"permissions,omitempty"`
	McpServers   []apiruntime.McpServer      `json:"mcpServers,omitempty"`
	WorkflowFile string                      `json:"workflowFile,omitempty"`
	Fallback     []FallbackEntry             `json:"fallback,omitempty"`
}

type FallbackEntry struct {
	Agent string `json:"agent"`
}

type Stage struct {
	Name        string   `json:"name"`
	Type        string   `json:"type,omitempty"`
	AgentRun    string   `json:"agentRun,omitempty"`
	Description string   `json:"description,omitempty"`
	InputFiles  []string `json:"input_files,omitempty"`
	InputFrom   []string `json:"input_from,omitempty"`
	MaxRetries  *int     `json:"max_retries,omitempty"`
	Routes      []Route  `json:"routes"`
	Tasks       []Task   `json:"tasks,omitempty"`
	Wait        string   `json:"wait,omitempty"`
}

type Task struct {
	AgentRun    string   `json:"agentRun"`
	Description string   `json:"description"`
	InputFiles  []string `json:"input_files,omitempty"`
	InputFrom   []string `json:"input_from,omitempty"`
}

type Route struct {
	When string `json:"when"`
	Goto string `json:"goto"`
}

type OutputConfig struct {
	Summary *bool `json:"summary,omitempty"`
}

type CleanupConfig struct {
	PreserveWorkspace *bool `json:"preserve_workspace,omitempty"`
}
