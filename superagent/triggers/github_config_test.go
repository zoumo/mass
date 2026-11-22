package triggers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

func TestGitHubConfigAndOptionalContext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "github.yaml")
	cfg := githubConfig{AccountID: "github-account", InstanceID: "github-instance", CoreEndpoint: "http://127.0.0.1:8080", TokenEnv: "CORE_TOKEN", GitHubTokenEnv: "GITHUB_TOKEN", DatabasePath: filepath.Join(dir, "github.sqlite")}
	write := func() {
		data, err := yaml.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		testWrite(t, path, data)
	}
	cfg.Repositories = []githubRepository{{Repository: "Example/Demo", Prompt: "第一行\n第二行"}}
	write()
	loaded, err := loadGitHubConfig(path, nil, "")
	if err != nil || loaded.GitHubTokenEnv != "GITHUB_TOKEN" || loaded.PollSeconds != 60 || loaded.Repositories[0].Repository != "example/demo" || loaded.Repositories[0].Prompt != cfg.Repositories[0].Prompt {
		t.Fatalf("configuration lost defaults, normalization or multiline prompt: %+v %v", loaded, err)
	}
	cfg.Repositories = append(cfg.Repositories, githubRepository{Repository: "example/DEMO"})
	write()
	if _, err := loadGitHubConfig(path, nil, ""); err == nil {
		t.Fatal("accepted duplicate repository")
	}
	cfg.Repositories = cfg.Repositories[:1]
	cfg.Repositories[0].Prompt = strings.Repeat("界", 1366)
	write()
	if _, err := loadGitHubConfig(path, nil, ""); err == nil {
		t.Fatal("accepted a prompt exceeding its UTF-8 byte limit")
	}
	cfg.Repositories[0].Prompt = ""
	cfg.GitHubTokenEnv = cfg.TokenEnv
	write()
	if _, err := loadGitHubConfig(path, nil, ""); err == nil {
		t.Fatal("accepted shared Core and GitHub credential references")
	}
	cfg.GitHubTokenEnv = "TEST_GITHUB_TOKEN"
	write()
	t.Setenv("CORE_TOKEN", testToken)
	t.Setenv("TEST_GITHUB_TOKEN", "")
	if err := exec.CommandContext(t.Context(), triggerBinary, "triggers", "github", "check", "--config", path).Run(); err == nil {
		t.Fatal("check accepted a missing GitHub token")
	}
	t.Setenv("TEST_GITHUB_TOKEN", githubFixtureToken)
	output, err := exec.CommandContext(t.Context(), triggerBinary, "triggers", "github", "check", "--config", path).CombinedOutput()
	if err != nil || !strings.Contains(string(output), "configuration_checked") {
		t.Fatalf("GitHub check with distinct credentials failed: %s %v", output, err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	testWrite(t, path, append(data, []byte("githubEndpoint: http://127.0.0.1:1\n")...))
	if _, err := loadGitHubConfig(path, nil, ""); err == nil {
		t.Fatal("accepted an alternate GitHub endpoint in YAML")
	}
	cfg.DatabasePath = "relative.sqlite"
	write()
	if _, err := loadGitHubConfig(path, nil, ""); err == nil {
		t.Fatal("accepted a relative GitHub database path")
	}
	if text := githubMessageText("source", "body", " \t\n"); strings.Contains(text, "repository-context") {
		t.Fatalf("a blank prompt produced a context section: %s", text)
	}
}

// The local proxy refuses the HTTPS tunnel before the client can send a token.
func TestGitHubStandaloneServeAndStatus(t *testing.T) {
	t.Parallel()
	requests := make(chan string, 4)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case requests <- r.Method + " " + r.Host:
		default:
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer proxy.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "github.yaml")
	config, err := yaml.Marshal(githubConfig{
		AccountID: "account", InstanceID: "fixture-instance", CoreEndpoint: "http://127.0.0.1:1",
		DatabasePath: filepath.Join(dir, "github.sqlite"), TokenEnv: "TEST_CORE_TOKEN", GitHubTokenEnv: "TEST_GITHUB_TOKEN",
		Repositories: []githubRepository{{Repository: "demo/project"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	testWrite(t, path, config)
	process := start(t, triggerBinary, []string{"triggers", "github", "serve", "--config", path}, []string{
		"TEST_CORE_TOKEN=" + testToken, "TEST_GITHUB_TOKEN=" + githubFixtureToken,
		"HTTPS_PROXY=" + proxy.URL, "NO_PROXY=",
	})
	endpoint := process.endpoint(t, "statusEndpoint")
	select {
	case target := <-requests:
		if target != "CONNECT api.github.com:443" {
			t.Fatalf("unexpected GitHub endpoint %q", target)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no GitHub request reached the local proxy: %s", process.text())
	}
	wait(t, "GitHub CLI status shows the repository in retry backoff", func() bool {
		cmd := exec.CommandContext(t.Context(), triggerBinary, "triggers", "github", "status", "--endpoint", endpoint)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("GitHub status failed: %s %v", output, err)
		}
		var status githubStatus
		if err := json.Unmarshal(output, &status); err != nil {
			t.Fatalf("GitHub status was not JSON: %s %v", output, err)
		}
		return status.Source == githubSource && status.InstanceID == "fixture-instance" &&
			status.Repositories["demo/project"].Streams["issues"].State == "stale"
	})
	terminate(t, process)
}
