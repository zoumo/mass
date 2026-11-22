package github

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/zoumo/mass/superagent/triggers/internal/triggertest"
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
		triggertest.Write(t, path, data)
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
	t.Setenv("CORE_TOKEN", triggertest.Token)
	t.Setenv("TEST_GITHUB_TOKEN", "")
	if err := exec.CommandContext(t.Context(), triggerBinary, "triggers", "github", "check", "--config", path).Run(); err == nil {
		t.Fatal("check accepted a missing GitHub token")
	}
	cfg.Repositories = nil
	write()
	require.NoError(t, configCheck(path))
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []string{"match: {}\n", "match: {labels: [ci]}\n", "match: null\n"} {
		triggertest.Write(t, path, append(append([]byte(nil), data...), legacy...))
		require.ErrorContains(t, configCheck(path), "match is no longer supported")
	}
	triggertest.Write(t, path, append(data, []byte("githubEndpoint: http://127.0.0.1:1\n")...))
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

func configCheck(path string) error {
	_, err := loadGitHubConfig(path, nil, "")
	return err
}

func TestGitHubCheckProbesNotifications(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := githubConfig{
		AccountID: "account", InstanceID: "fixture-instance", CoreEndpoint: "http://127.0.0.1:1",
		DatabasePath: filepath.Join(dir, "github.sqlite"), TokenEnv: "TEST_CORE_TOKEN", GitHubTokenEnv: "TEST_GITHUB_TOKEN",
	}
	path := githubTestConfigFile(t, cfg)
	for _, code := range []int{http.StatusOK, http.StatusForbidden} {
		provider := httptest.NewServer(githubFixtureHandler(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("per_page") != "1" {
				t.Error("check must make a minimal read")
			}
			w.WriteHeader(code)
			githubFixtureJSON(w, []any{})
		}, nil))
		cmd := exec.CommandContext(t.Context(), githubTestBinary(t), "triggers", "github", "check", "--config", path)
		cmd.Env = append(os.Environ(), githubFixtureEnv(provider.URL)...)
		out, err := cmd.CombinedOutput()
		provider.Close()
		if code == http.StatusOK {
			require.NoError(t, err, string(out))
			require.Contains(t, string(out), "notifications_checked")
		} else {
			require.Error(t, err)
			require.Contains(t, string(out), "cannot read GitHub Notifications")
			require.Contains(t, string(out), "github_http_403")
		}
	}
	_, err := os.Stat(cfg.DatabasePath)
	require.ErrorIs(t, err, os.ErrNotExist)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	triggertest.Write(t, path, append(data, []byte("match: {labels: [ci]}\n")...))
	out, err := exec.CommandContext(t.Context(), githubTestBinary(t), "triggers", "github", "check", "--config", path).CombinedOutput()
	require.Error(t, err)
	require.Contains(t, string(out), "match is no longer supported")
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
	triggertest.Write(t, path, config)
	process := triggertest.Start(t, triggerBinary, []string{"triggers", "github", "serve", "--config", path}, []string{
		"TEST_CORE_TOKEN=" + triggertest.Token, "TEST_GITHUB_TOKEN=" + githubFixtureToken,
		"HTTPS_PROXY=" + proxy.URL, "NO_PROXY=",
	})
	endpoint := process.Endpoint(t, "statusEndpoint")
	select {
	case target := <-requests:
		if target != "CONNECT api.github.com:443" {
			t.Fatalf("unexpected GitHub endpoint %q", target)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no GitHub request reached the local proxy: %s", process.Text())
	}
	triggertest.Wait(t, "GitHub CLI status shows the repository in retry backoff", func() bool {
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
			status.Notifications.State == "stale"
	})
	triggertest.Terminate(t, process)
}
