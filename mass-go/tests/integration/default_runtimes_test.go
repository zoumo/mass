package integration_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

func defaultRuntimeDaemon(t *testing.T, backend, data string, env []string) (*client.Client, string, func()) {
	t.Helper()
	config := filepath.Join(t.TempDir(), "massd.yaml")
	require.NoError(t, os.WriteFile(config, []byte("listen: 127.0.0.1:0\nmetaStore:\n  backend: "+backend+"\n"), 0o600))
	c, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 45*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(c, app, "--config", config, "--data", data)
	cmd.Env = env
	var stderr lockedBuffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	stop := func() {
		if cmd.ProcessState == nil {
			err := cmd.Process.Signal(syscall.SIGTERM)
			waitErr := cmd.Wait()
			require.NoError(t, err, stderr.String())
			require.NoError(t, waitErr, stderr.String())
		}
	}
	t.Cleanup(stop)
	var ready struct {
		URL string `json:"url"`
	}
	require.NoError(t, json.NewDecoder(stdout).Decode(&ready), stderr.String())
	return newHTTPClient(t, ready.URL, ""), ready.URL, stop
}

func TestDefaultRuntimeRegistration(t *testing.T) {
	peer := ticket20Peer(t)
	for _, backend := range []string{"sqlite", "file"} {
		t.Run(backend, func(t *testing.T) {
			data := t.TempDir()
			env := []string{"HOME=" + t.TempDir(), "PATH=" + t.TempDir(), "PEER_KIND=codex", "PEER_DIR=" + t.TempDir()}
			c, url, stop := defaultRuntimeDaemon(t, backend, data, env)
			out, err := exec.CommandContext(ctx(t), ctl, "--url", url, "-o", "json", "runtime", "list").CombinedOutput()
			require.NoError(t, err, string(out))
			var runtimes []resource.Runtime
			require.NoError(t, json.Unmarshal(out, &runtimes))
			require.Len(t, runtimes, 2)
			byName := map[string]resource.Runtime{}
			for _, r := range runtimes {
				byName[r.Metadata.Name] = r
				require.Equal(t, r.Metadata.Name, r.Spec.Type)
				require.Equal(t, runtime.Config{}, r.Spec.Config)
				require.Equal(t, agent.PermissionPresetFullAccess, r.Spec.PermissionPreset)
				require.Equal(t, "stopped", r.Status.State)
				require.NotEmpty(t, r.Metadata.UID)
			}
			require.Contains(t, byName, "claude")
			require.Contains(t, byName, "codex")
			for _, name := range []string{"claude", "codex"} {
				output, startErr := exec.CommandContext(ctx(t), ctl, "--url", url, "runtime", "start", name).CombinedOutput()
				require.Error(t, startErr, string(output))
				require.Contains(t, string(output), "agent_executable_unavailable")
				require.Contains(t, string(output), `"`+name+`"`)
			}

			out, err = exec.CommandContext(ctx(t), ctl, "--url", url, "-o", "json", "runtime", "update", "codex", "--executable", peer, "--permission-preset", "manualReview").CombinedOutput()
			require.NoError(t, err, string(out))
			var edited resource.Runtime
			require.NoError(t, json.Unmarshal(out, &edited))
			require.Equal(t, byName["codex"].Metadata.UID, edited.Metadata.UID)
			require.Equal(t, agent.PermissionPresetManualReview, edited.Spec.PermissionPreset)
			require.Equal(t, peer, edited.Spec.Config["executable"])
			require.NoError(t, c.StartRuntime(ctx(t), "codex", &resource.Runtime{}))
			require.NoError(t, c.StopRuntime(ctx(t), "codex", &resource.Runtime{}))
			require.NoError(t, c.GetRuntime(ctx(t), "codex", &edited))

			require.NoError(t, c.DeleteRuntime(ctx(t), "claude"))
			var replacement resource.Runtime
			require.NoError(t, c.CreateRuntime(ctx(t), resource.Runtime{
				Metadata: resource.Metadata{Name: "claude"},
				Spec:     resource.RuntimeSpec{Type: "codex", Config: runtime.Config{"executable": peer}},
			}, &replacement))
			stop()

			c, _, stop = defaultRuntimeDaemon(t, backend, data, env)
			var got resource.Runtime
			require.NoError(t, c.GetRuntime(ctx(t), "claude", &got))
			require.Equal(t, replacement, got)
			got = resource.Runtime{}
			require.NoError(t, c.GetRuntime(ctx(t), "codex", &got))
			require.Equal(t, edited, got)
			require.NoError(t, c.DeleteRuntime(ctx(t), "codex"))
			stop()

			c, _, _ = defaultRuntimeDaemon(t, backend, data, env)
			got = resource.Runtime{}
			require.NoError(t, c.GetRuntime(ctx(t), "claude", &got))
			require.Equal(t, replacement, got)
			got = resource.Runtime{}
			require.NoError(t, c.GetRuntime(ctx(t), "codex", &got))
			require.NotEqual(t, edited.Metadata.UID, got.Metadata.UID)
			require.Equal(t, byName["codex"].Spec, got.Spec)
			require.Equal(t, "stopped", got.Status.State)
		})
	}
}

func TestDefaultRuntimeSessionPermissions(t *testing.T) {
	peer := ticket20Peer(t)
	for _, backend := range []string{"sqlite", "file"} {
		for _, scenario := range []struct {
			kind   string
			preset agent.PermissionPreset
		}{
			{"claude", ""},
			{"codex", ""},
			{"codex", agent.PermissionPresetManualReview},
		} {
			name := backend + "/" + scenario.kind + "/inherited"
			if scenario.preset != "" {
				name = backend + "/" + scenario.kind + "/" + string(scenario.preset)
			}
			t.Run(name, func(t *testing.T) {
				kind := scenario.kind
				work, err := filepath.EvalSymlinks(t.TempDir())
				require.NoError(t, err)
				native, home, path := t.TempDir(), t.TempDir(), t.TempDir()
				require.NoError(t, os.Symlink(peer, filepath.Join(path, kind)))
				config, err := filepath.EvalSymlinks(t.TempDir())
				require.NoError(t, err)
				project := filepath.Join(config, "projects", regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(work, "-"))
				require.NoError(t, os.MkdirAll(project, 0o700))
				env := []string{"HOME=" + home, "PATH=" + path, "PEER_DIR=" + native, "PEER_KIND=" + kind, "WORK_DIR=" + work}
				if kind == "claude" {
					env = append(env, "CLAUDE_CONFIG_DIR="+config, "SAVED_TRANSCRIPT_DIR="+project, "PEER_CLAUDE_FULL_ACCESS=1")
				}
				data := t.TempDir()
				c, _, stop := defaultRuntimeDaemon(t, backend, data, env)
				wire := filepath.Join(native, "wire.jsonl")
				require.NoFileExists(t, wire)
				spec := resource.SessionSpec{RuntimeRef: kind, SessionSpec: runtime.SessionSpec{Cwd: work}}
				expected := agent.PermissionPresetFullAccess
				if scenario.preset != "" {
					spec.Config = &runtime.InitialSessionConfig{PermissionPreset: scenario.preset}
					expected = scenario.preset
				}
				input := runtime.Input{Content: []events.ContentBlock{events.Text("seed native history")}}
				var created resource.SessionCreated
				require.NoError(t, c.CreateSessionWithInput(ctx(t), resource.SessionCreateInput{
					Metadata: resource.Metadata{Namespace: "default", Name: "original"}, Spec: spec, InitialInput: &input,
				}, &created))
				require.NotNil(t, created.InitialInputReceipt)
				require.NotNil(t, created.Spec.Config)
				require.Equal(t, expected, created.Spec.Config.PermissionPreset)
				ref := resource.SessionRef{SessionID: created.Status.SessionID}
				require.Eventually(t, func() bool {
					var page store.HistoryPage
					var current resource.Session
					return c.History(ctx(t), ref, store.HistoryQuery{Types: []string{"agent.message"}}, &page) == nil && len(page.Events) == 1 &&
						c.GetSession(ctx(t), ref, &current) == nil && current.Status.State == events.Idle
				}, 5*time.Second, 10*time.Millisecond)
				require.NoError(t, c.CloseSession(ctx(t), ref, &resource.Session{}))
				require.NoError(t, c.StopRuntime(ctx(t), kind, &resource.Runtime{}))

				var r resource.Runtime
				require.NoError(t, c.GetRuntime(ctx(t), kind, &r))
				r.Spec.PermissionPreset = agent.PermissionPresetAutoReview
				if kind == "claude" {
					r.Spec.Config["env"] = map[string]any{"CLAUDE_CONFIG_DIR": config}
				}
				require.NoError(t, c.UpdateRuntime(ctx(t), kind, r.Spec, &r))
				stop()

				c, _, _ = defaultRuntimeDaemon(t, backend, data, env)
				var persisted, resumed resource.Session
				require.NoError(t, c.GetSession(ctx(t), ref, &persisted))
				require.Equal(t, created.Spec.Config, persisted.Spec.Config)
				require.NoError(t, c.ResumeSession(ctx(t), ref, &resumed))
				require.Equal(t, ref.SessionID, resumed.Status.SessionID)
				require.Equal(t, created.Spec.Config, resumed.Spec.Config)
				require.NoError(t, c.CloseSession(ctx(t), ref, &resource.Session{}))

				var methods []string
				for _, call := range ticket12Calls(t, wire) {
					if kind == "claude" && call.Method == "full_access" {
						require.Equal(t, ref.SessionID, call.Params["sessionId"])
						methods = append(methods, "full_access")
					}
					if kind == "codex" && (call.Method == "thread/start" || call.Method == "thread/resume") {
						methods = append(methods, call.Method)
						if expected == agent.PermissionPresetFullAccess {
							require.Equal(t, "never", call.Params["approvalPolicy"])
							require.Equal(t, "danger-full-access", call.Params["sandbox"])
						} else {
							require.Equal(t, "on-request", call.Params["approvalPolicy"])
							require.Equal(t, "user", call.Params["approvalsReviewer"])
							require.Equal(t, "workspace-write", call.Params["sandbox"])
						}
					}
				}
				if kind == "claude" {
					require.Equal(t, []string{"full_access", "full_access"}, methods)
				} else {
					require.Equal(t, []string{"thread/start", "thread/resume"}, methods)
				}
			})
		}
	}
}
