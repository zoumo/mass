package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

var initialCases = []struct {
	name   string
	config *runtime.InitialSessionConfig
}{
	{"both", &runtime.InitialSessionConfig{Model: " Native Model ", Effort: " MAX "}},
	{"model", &runtime.InitialSessionConfig{Model: "native-model"}},
	{"effort", &runtime.InitialSessionConfig{Effort: "effort-only"}},
	{"omitted", nil},
	{"empty", &runtime.InitialSessionConfig{}},
}

// Invalid public requests must leave the same name available for the valid
// create/load that follows, and cannot consume the native scenario's frames.
func initialReject(t *testing.T, url, cwd string) {
	t.Helper()
	for _, action := range []string{"create", "load"} {
		for _, field := range []string{"model", "effort"} {
			for _, value := range []string{`null`, `42`, `true`, `"a\u0000b"`, `"\ud800"`, "\"" + string([]byte{0xff}) + "\""} {
				config := `{"` + field + `":` + value + `}`
				path := url + "/apis/v1/sessions"
				body := fmt.Sprintf(`{"metadata":{"namespace":"one","name":"configured"},"spec":{"runtimeRef":"agent","cwd":%q,"config":%s}}`, cwd, config)
				if action == "load" {
					path += "/load"
					body = fmt.Sprintf(`{"metadata":{"namespace":"one","name":"configured"},"runtimeRef":"agent","cwd":%q,"sessionId":"invalid-native","config":%s}`, cwd, config)
				}
				req, err := http.NewRequestWithContext(ctx(t), http.MethodPost, path, strings.NewReader(body))
				require.NoError(t, err)
				req.Header.Set("Content-Type", "application/json")
				res, err := http.DefaultClient.Do(req)
				require.NoError(t, err)
				raw, err := io.ReadAll(res.Body)
				require.NoError(t, err)
				require.NoError(t, res.Body.Close())
				require.Equal(t, 400, res.StatusCode, "%s", raw)
			}
		}
	}
}

func initialCLI(t *testing.T, url, action, cwd, id string, config *runtime.InitialSessionConfig, out any) {
	t.Helper()
	args := []string{"--url", url, "-o", "json", "session", action, "configured", "-n", "one", "--runtime", "agent", "--cwd", cwd}
	if action == "load" {
		args = append(args, "--session-id", id)
	}
	if config != nil {
		args = append(args, "--model", config.Model, "--effort", config.Effort)
	}
	raw, err := exec.CommandContext(ctx(t), ctl, args...).CombinedOutput()
	require.NoError(t, err, "%s", raw)
	require.NoError(t, json.Unmarshal(raw, out), "%s", raw)
}

// Every configured start/resume is matched against a full strict native frame.
// Authored temporary scenarios do not modify historical recordings.
func TestInitialSessionConfigurationCodexCreateResume(t *testing.T) {
	for _, tc := range initialCases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/codex-close-resume.json")
			require.NoError(t, err)
			var fixture map[string]any
			d := json.NewDecoder(bytes.NewReader(raw))
			d.UseNumber()
			require.NoError(t, d.Decode(&fixture))
			for _, value := range fixture["frames"].([]any) {
				frame := value.(map[string]any)
				wire := frame["wire"].(map[string]any)
				if frame["direction"] != "c2s" || (wire["method"] != "thread/start" && wire["method"] != "thread/resume") {
					continue
				}
				params := wire["params"].(map[string]any)
				if tc.config != nil && tc.config.Model != "" {
					params["model"] = tc.config.Model
				}
				if tc.config != nil && tc.config.Effort != "" {
					params["config"] = map[string]any{"model_reasoning_effort": tc.config.Effort}
				}
			}
			ensureNativeReadyObservation(fixture, "codex")
			path, cwd := writeNativeScenario(t, fixture)
			svc, _, dir := setup(t, nil)
			_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "codex", Config: runtime.Config{"executable": mock, "scenario": path}}})
			require.NoError(t, err)
			handler, err := transport.New(svc, transport.Options{LocalOnly: true})
			require.NoError(t, err)
			defer handler.Shutdown()
			server := httptest.NewServer(handler)
			defer server.Close()
			initialReject(t, server.URL, cwd)
			var created resource.SessionCreated
			initialCLI(t, server.URL, "create", cwd, "", tc.config, &created)
			require.Equal(t, tc.config, created.Spec.Config)
			ref := resource.SessionRef{SessionID: created.Status.SessionID}
			finishInput(t, svc, ref, "hello")
			c := newHTTPClient(t, server.URL, "")
			require.NoError(t, c.CloseSession(ctx(t), ref, nil))
			// Inspect durable bytes and decode independently of Service's resource cache.
			files := 0
			require.NoError(t, filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() || !strings.HasSuffix(path, ".json") {
					return nil
				}
				b, e := os.ReadFile(path)
				if e != nil {
					return e
				}
				var saved resource.Session
				if json.Unmarshal(b, &saved) == nil && saved.Metadata.Name == "configured" && saved.Status.SessionID != "" {
					files++
					require.Equal(t, tc.config, saved.Spec.Config)
				}
				return nil
			}))
			require.Positive(t, files, "durable Session must be readable")
			var resumed resource.Session
			require.NoError(t, c.ResumeSession(ctx(t), ref, &resumed))
			require.Equal(t, created.Status.SessionID, resumed.Status.SessionID)
			require.Equal(t, created.Metadata.UID, resumed.Metadata.UID)
			require.Equal(t, created.Spec, resumed.Spec)
			finishInput(t, svc, ref, "continue")
			require.NoError(t, c.CloseSession(ctx(t), ref, nil))
		})
	}
}

func initialClaudeArgs(t *testing.T, evidence string, config *runtime.InitialSessionConfig, expected int) {
	t.Helper()
	raw, err := os.ReadFile(evidence)
	require.NoError(t, err)
	count := 0
	for line := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
		var row struct {
			Event string   `json:"event"`
			Args  []string `json:"args"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &row))
		if row.Event != "opened" {
			continue
		}
		count++
		for flag, value := range map[string]string{"--model": config.SDKConfig().Model, "--effort": config.SDKConfig().Effort} {
			seen := false
			for _, arg := range row.Args {
				if strings.HasPrefix(arg, flag+"=") {
					require.Equal(t, flag+"="+value, arg)
					seen = true
				}
			}
			require.Equal(t, value != "", seen, "native argv %v", row.Args)
		}
	}
	require.Equal(t, expected, count)
}

func TestInitialSessionConfigurationClaudeCreateResume(t *testing.T) {
	for _, tc := range initialCases {
		t.Run(tc.name, func(t *testing.T) {
			var setup *ticket07Setup
			_, url, cwd, evidence := ticket07HTTP(t, "", ticket07Rows(), func(s *ticket07Setup) {
				setup = s
			})
			initialReject(t, url, cwd)
			var created resource.SessionCreated
			initialCLI(t, url, "create", cwd, "", tc.config, &created)
			require.Equal(t, tc.config, created.Spec.Config)
			// Supply the SDK preflight's native-format metadata for the assigned identity.
			raw, err := os.ReadFile(setup.path)
			require.NoError(t, err)
			id := created.Status.SessionID
			nativePath := filepath.Join(filepath.Dir(setup.path), id+".jsonl")
			require.NoError(t, os.WriteFile(nativePath, bytes.ReplaceAll(raw, []byte(claudeLoadID), []byte(id)), 0o600))
			c := newHTTPClient(t, url, "")
			ref := resource.SessionRef{SessionID: created.Status.SessionID}
			require.NoError(t, c.CloseSession(ctx(t), ref, nil))
			var resumed resource.Session
			require.NoError(t, c.ResumeSession(ctx(t), ref, &resumed))
			require.Equal(t, created.Status.SessionID, resumed.Status.SessionID)
			require.Equal(t, created.Metadata.UID, resumed.Metadata.UID)
			require.Equal(t, created.Spec, resumed.Spec)
			var receipt runtime.InputReceipt
			require.NoError(t, c.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("continue")}}, &receipt))
			require.Eventually(t, func() bool {
				var r resource.Session
				return c.GetSession(ctx(t), ref, &r) == nil && r.Status.State == events.Idle
			}, time.Second, time.Millisecond)
			initialClaudeArgs(t, evidence, tc.config, 2)
			require.NoError(t, c.CloseSession(ctx(t), ref, nil))
		})
	}
}

func initialLoadComplete(t *testing.T, c *client.Client, id string, config *runtime.InitialSessionConfig) {
	t.Helper()
	ref := resource.SessionRef{SessionID: id}
	var loaded resource.Session
	require.Eventually(t, func() bool {
		return c.GetSession(ctx(t), ref, &loaded) == nil && (loaded.Status.HistoryLoad.Phase == "completed" || loaded.Status.HistoryLoad.Phase == "failed")
	}, 5*time.Second, time.Millisecond)
	require.Equal(t, "completed", loaded.Status.HistoryLoad.Phase, "%+v", loaded.Status.HistoryLoad)
	require.Equal(t, config, loaded.Spec.Config)
	require.Equal(t, id, loaded.Status.SessionID)
	var receipt runtime.InputReceipt
	require.NoError(t, c.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("continue retained native session")}}, &receipt))
	require.Eventually(t, func() bool {
		var history store.HistoryPage
		if c.History(ctx(t), ref, store.HistoryQuery{}, &history) != nil {
			return false
		}
		for _, e := range history.Events {
			if e.Replay == nil && e.Type() == "agent.message" {
				return true
			}
		}
		return false
	}, time.Second, time.Millisecond)
}

func TestInitialSessionConfigurationLoadNative(t *testing.T) {
	for _, tc := range initialCases {
		for _, kind := range []string{"codex", "claude"} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				var url, cwd, evidence, id string
				if kind == "claude" {
					_, url, cwd, evidence = ticket07HTTP(t, "", ticket07Rows())
					id = claudeLoadID
				} else {
					evidence = filepath.Join(t.TempDir(), "native.jsonl")
					svc, work := ticket06Service(t, "", map[string]any{"LOAD_EVIDENCE": evidence})
					cwd = work
					id = "native-thread"
					h, err := transport.New(svc, transport.Options{LocalOnly: true})
					require.NoError(t, err)
					t.Cleanup(h.Shutdown)
					server := httptest.NewServer(h)
					t.Cleanup(server.Close)
					url = server.URL
				}
				initialReject(t, url, cwd)
				var accepted resource.SessionLoadAccepted
				initialCLI(t, url, "load", cwd, id, tc.config, &accepted)
				require.Equal(t, tc.config, accepted.Session.Spec.Config)
				c := newHTTPClient(t, url, "")
				initialLoadComplete(t, c, id, tc.config)
				if kind == "claude" {
					initialClaudeArgs(t, evidence, tc.config, 1)
				} else {
					found := 0
					for _, call := range ticket12Calls(t, evidence) {
						if call.Method != "thread/resume" {
							continue
						}
						found++
						if tc.config != nil && tc.config.Model != "" {
							require.Equal(t, tc.config.Model, call.Params["model"])
						} else {
							require.NotContains(t, call.Params, "model")
						}
						if tc.config != nil && tc.config.Effort != "" {
							require.Equal(t, tc.config.Effort, call.Params["config"].(map[string]any)["model_reasoning_effort"])
						} else {
							require.NotContains(t, call.Params, "config")
						}
					}
					require.Equal(t, 1, found)
				}
				require.NoError(t, c.CloseSession(ctx(t), resource.SessionRef{SessionID: id}, nil))
			})
		}
	}
}
