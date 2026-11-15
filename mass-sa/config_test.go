package sa

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigSupportsMultipleProjects(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
stateFile: state/state.json
mass:
  endpoint: ws://127.0.0.1:24368
  namespace: sa-system
  tokenEnv: MASS_TOKEN
multica:
  executable: multica
projects:
  - id: mass
    name: MASS
    cwd: /work/mass
    memory: |
      Always inspect the harness.
      Keep changes minimal.
    runtimeRef: deepseek-claude
    router:
      model: deepseek-router-a
    worker:
      model: deepseek-worker
    multica:
      workspaceId: test-workspace
      projectId: 22222222-2222-4222-8222-222222222222
  - id: mass-other
    cwd: /work/mass-other
    memory: MASS secondary project memory.
    runtimeRef: deepseek-claude
    router: {}
    worker: {}
    multica:
      workspaceId: test-workspace
      projectId: second-project
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.StateFile, filepath.Join(dir, "state", "state.json"); got != want {
		t.Fatalf("state file = %q, want %q", got, want)
	}
	if got, want := cfg.MASS.Endpoint, "ws://127.0.0.1:24368/rpc"; got != want {
		t.Fatalf("endpoint = %q, want %q", got, want)
	}
	if len(cfg.Projects) != 2 {
		t.Fatalf("projects = %d, want 2", len(cfg.Projects))
	}
	if cfg.Projects[0].Router.Effort != "low" || cfg.Projects[0].Worker.Effort != "high" {
		t.Fatalf("default efforts = %q/%q, want low/high", cfg.Projects[0].Router.Effort, cfg.Projects[0].Worker.Effort)
	}
	if cfg.Projects[0].Inbox != (InboxConfig{
		MergeWindowMS:  defaultInboxMergeWindowMS,
		MaxBatchSize:   defaultInboxMaxBatchSize,
		MaxPromptBytes: defaultInboxMaxPromptBytes,
	}) {
		t.Fatalf("default inbox = %+v", cfg.Projects[0].Inbox)
	}
	if cfg.Projects[0].Router.Model != "deepseek-router-a" || cfg.Projects[1].Router.Model != "" {
		t.Fatalf("router model literals were not preserved")
	}
	if !strings.Contains(cfg.Projects[0].Memory, "Always inspect the harness.") {
		t.Fatalf("project memory was not preserved")
	}
	if cfg.Projects[1].Name != "mass-other" {
		t.Fatalf("default display name = %q", cfg.Projects[1].Name)
	}
}

func TestExampleConfigLoads(t *testing.T) {
	if _, err := LoadConfig("config.example.yaml"); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfigRejectsUnknownFieldAndMultipleDocuments(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"unknown": `
stateFile: state.json
unknown: true
`,
		"multiple": `
stateFile: state.json
mass: {endpoint: ws://127.0.0.1:24368/rpc, namespace: sa-system}
multica: {executable: multica}
projects: []
---
{}
`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name+".yaml")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(path); err == nil {
				t.Fatal("LoadConfig succeeded, want validation error")
			}
		})
	}
}

func TestConfigValidation(t *testing.T) {
	cases := map[string]func(*Config){
		"missing namespace": func(cfg *Config) { cfg.MASS.Namespace = "" },
		"wrong namespace":   func(cfg *Config) { cfg.MASS.Namespace = "other" },
		"invalid endpoint":  func(cfg *Config) { cfg.MASS.Endpoint = "http://example.test/rpc" },
		"relative state":    func(cfg *Config) { cfg.StateFile = "state.json" },
		"both tokens": func(cfg *Config) {
			cfg.MASS.TokenEnv = "MASS_TOKEN"
			cfg.MASS.TokenFile = "/tmp/token"
		},
		"duplicate project": func(cfg *Config) {
			cfg.Projects = append(cfg.Projects, newTestProject("one"))
		},
		"duplicate router": func(cfg *Config) {
			second := newTestProject("two")
			second.Router.Name = RouterName(cfg.Projects[0])
			cfg.Projects = append(cfg.Projects, second)
		},
		"relative cwd": func(cfg *Config) { cfg.Projects[0].CWD = "work" },
		"blank memory": func(cfg *Config) { cfg.Projects[0].Memory = "  \n" },
		"missing runtime": func(cfg *Config) {
			cfg.Projects[0].RuntimeRef = ""
		},
		"missing multica project": func(cfg *Config) {
			cfg.Projects[0].Multica.ProjectID = ""
		},
		"nul model": func(cfg *Config) { cfg.Projects[0].Router.Model = "bad\x00model" },
		"negative merge window": func(cfg *Config) {
			cfg.Projects[0].Inbox.MergeWindowMS = -1
		},
		"negative batch size": func(cfg *Config) {
			cfg.Projects[0].Inbox.MaxBatchSize = -1
		},
		"negative prompt budget": func(cfg *Config) {
			cfg.Projects[0].Inbox.MaxPromptBytes = -1
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := newTestConfig(t, "ws://127.0.0.1:24368/rpc", filepath.Join(t.TempDir(), "state.json"), newTestProject("one"))
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate succeeded, want error")
			}
		})
	}
}

func TestManagementEndpointResolvesNamedServicePort(t *testing.T) {
	cfg := Config{Management: ManagementConfig{Listen: "127.0.0.1:http"}}
	got := cfg.ManagementEndpoint()
	want := "http://127.0.0.1:80/v1/management"
	if got != want {
		t.Fatalf("ManagementEndpoint() = %q, want %q", got, want)
	}
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("url.Parse(%q) returned error: %v", got, err)
	}
	if parsed.Port() != "80" {
		t.Fatalf("resolved endpoint port = %q, want 80", parsed.Port())
	}
}

func TestManagementEndpointResolvesIPv6NamedServicePort(t *testing.T) {
	cfg := Config{Management: ManagementConfig{Listen: "[::1]:http"}}
	got := cfg.ManagementEndpoint()
	want := "http://[::1]:80/v1/management"
	if got != want {
		t.Fatalf("ManagementEndpoint() = %q, want %q", got, want)
	}
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("url.Parse(%q) returned error: %v", got, err)
	}
	if parsed.Hostname() != "::1" || parsed.Port() != "80" {
		t.Fatalf("parsed host:port = %q:%q, want ::1:80", parsed.Hostname(), parsed.Port())
	}
}

func TestManagementEndpointKeepsNumericPort(t *testing.T) {
	cfg := Config{Management: ManagementConfig{Listen: "127.0.0.1:8088"}}
	if got, want := cfg.ManagementEndpoint(), "http://127.0.0.1:8088/v1/management"; got != want {
		t.Fatalf("ManagementEndpoint() = %q, want %q", got, want)
	}
}

func TestManagementEndpointLeavesUnknownServiceUnchanged(t *testing.T) {
	cfg := Config{Management: ManagementConfig{Listen: "127.0.0.1:not-a-service"}}
	got := cfg.ManagementEndpoint()
	want := "http://127.0.0.1:not-a-service/v1/management"
	if got != want {
		t.Fatalf("ManagementEndpoint() = %q, want %q", got, want)
	}
	if _, err := url.Parse(got); err == nil {
		t.Fatal("url.Parse accepted an unresolved named service port")
	}
}

func newTestConfig(t *testing.T, endpoint, stateFile string, projects ...ProjectConfig) Config {
	t.Helper()
	cfg := Config{
		StateFile: stateFile,
		MASS: MASSConfig{
			Endpoint:  endpoint,
			Namespace: RouterNamespace,
		},
		Multica:  MulticaConfig{Executable: "multica"},
		Projects: projects,
	}
	cfg.applyDefaults()
	if err := cfg.normalize(""); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func newTestProject(id string) ProjectConfig {
	return ProjectConfig{
		ID:         id,
		Name:       strings.ToUpper(id),
		CWD:        "/work/" + id,
		Memory:     "memory for " + id,
		RuntimeRef: "deepseek-claude",
		Router: RouterConfig{
			Model:  "deepseek-router-" + id,
			Effort: "low",
		},
		Worker: RoleConfig{
			Model:  "deepseek-worker",
			Effort: "high",
		},
		Multica: ProjectMultica{
			WorkspaceID: "test-workspace",
			ProjectID:   "project-" + id,
		},
	}
}
