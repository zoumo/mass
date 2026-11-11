package integration_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/claude"
	"github.com/zoumo/mass/mass-go/pkg/runtime/codex"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

var (
	liveConfigBuild    sync.Once
	liveConfigBinary   string
	liveConfigBuildErr error
)

type configNativeCall struct {
	method     string
	params     map[string]any
	respond    func(any)
	reject     func()
	disconnect func()
}

type configNativeFactory struct {
	runtime.Factory
	peer *configNativePeer
}

func (f configNativeFactory) New(name string, config runtime.Config) (runtime.Driver, error) {
	d, err := f.Factory.New(name, config)
	if err == nil {
		f.peer.driver = d
	}
	return d, err
}

type configNativePeer struct {
	driver        runtime.Driver
	kind, cwd, id string
	listener      net.Listener
	calls         chan configNativeCall
	opened        chan []string
	done          chan struct{}
	mu            sync.Mutex
	conns         map[net.Conn]bool
	wg            sync.WaitGroup
	trace         []string
}

func newConfigNativePeer(t *testing.T, kind, cwd string) *configNativePeer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	p := &configNativePeer{kind: kind, cwd: cwd, id: "native-thread", listener: listener, calls: make(chan configNativeCall, 64), opened: make(chan []string, 8), done: make(chan struct{}), conns: map[net.Conn]bool{}}
	if kind == "claude" {
		p.id = "33333333-3333-4333-8333-333333333333"
	}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			p.conns[conn] = true
			p.mu.Unlock()
			p.wg.Add(1)
			go p.serve(conn)
		}
	}()
	t.Cleanup(func() {
		close(p.done)
		_ = listener.Close()
		p.mu.Lock()
		for c := range p.conns {
			_ = c.Close()
		}
		p.mu.Unlock()
		p.wg.Wait()
		_, err := net.Dial("tcp", listener.Addr().String())
		require.Error(t, err)
	})
	return p
}

func (p *configNativePeer) serve(conn net.Conn) {
	defer p.wg.Done()
	defer conn.Close()
	defer func() { p.mu.Lock(); delete(p.conns, conn); p.mu.Unlock() }()
	var writeMu sync.Mutex
	send := func(v any) { writeMu.Lock(); defer writeMu.Unlock(); _ = json.NewEncoder(conn).Encode(v) }
	thread := func() map[string]any {
		return map[string]any{"id": p.id, "sessionId": "native-session", "cliVersion": "synthetic", "createdAt": 1, "updatedAt": 2, "cwd": p.cwd, "ephemeral": false, "modelProvider": "synthetic", "preview": "", "projectId": nil, "source": "appServer", "status": map[string]any{"type": "idle"}, "turns": []any{}}
	}
	scan := bufio.NewScanner(conn)
	for scan.Scan() {
		raw := append([]byte(nil), scan.Bytes()...)
		p.mu.Lock()
		p.trace = append(p.trace, string(raw))
		p.mu.Unlock()
		var v map[string]any
		if json.Unmarshal(raw, &v) != nil {
			return
		}
		if v["peerOpen"] == true {
			var args []string
			for _, a := range v["args"].([]any) {
				args = append(args, a.(string))
			}
			if p.kind == "claude" {
				for _, arg := range args {
					if id, ok := strings.CutPrefix(arg, "--session-id="); ok {
						p.id = id
					}
					if id, ok := strings.CutPrefix(arg, "--resume="); ok {
						p.id = id
					}
				}
			}
			p.opened <- args
			continue
		}
		method, _ := v["method"].(string)
		params, _ := v["params"].(map[string]any)
		respond := func(result any) { send(map[string]any{"id": v["id"], "result": result}) }
		reject := func() {
			send(map[string]any{"id": v["id"], "error": map[string]any{"code": -32602, "message": "SECRET native rejection"}})
		}
		if p.kind == "claude" {
			if v["type"] != "control_request" {
				continue
			}
			params = v["request"].(map[string]any)
			method = params["subtype"].(string)
			requestID := v["request_id"]
			respond = func(result any) {
				send(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": requestID, "response": result}})
			}
			reject = func() {
				send(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "error", "request_id": requestID, "error": "SECRET native rejection"}})
			}
		}
		switch method {
		case "initialize":
			if p.kind == "codex" {
				respond(map[string]any{"userAgent": "synthetic", "codexHome": "/synthetic", "platformFamily": "unix", "platformOs": "linux"})
			} else {
				respond(map[string]any{})
				send(map[string]any{"type": "system", "subtype": "init", "session_id": p.id, "cwd": p.cwd, "claude_code_version": "2.1.258"})
				send(map[string]any{"type": "system", "subtype": "session_state_changed", "session_id": p.id, "state": "idle"})
			}
		case "initialized":
		case "thread/start", "thread/resume":
			respond(map[string]any{"thread": thread(), "approvalPolicy": "on-request", "approvalsReviewer": "user", "cwd": p.cwd, "model": "synthetic", "modelProvider": "synthetic", "sandbox": map[string]any{"type": "readOnly"}})
			send(map[string]any{"method": "thread/status/changed", "params": map[string]any{"threadId": p.id, "status": map[string]any{"type": "idle"}}})
		case "thread/read":
			respond(map[string]any{"thread": thread()})
		case "thread/unsubscribe":
			respond(map[string]any{"status": "unsubscribed"})
		case "thread/settings/update", "set_model", "get_settings", "apply_flag_settings":
			select {
			case p.calls <- configNativeCall{method: method, params: params, respond: respond, reject: reject, disconnect: func() { _ = conn.Close() }}:
			case <-p.done:
				return
			}
		case "interrupt":
			respond(map[string]any{})
		default:
			panic(fmt.Sprintf("unexpected %s method %s", p.kind, method))
		}
	}
}

func (p *configNativePeer) next(t *testing.T) configNativeCall {
	t.Helper()
	select {
	case call := <-p.calls:
		return call
	case <-ctx(t).Done():
		t.Fatal("native config request missing")
		return configNativeCall{}
	}
}

func liveConfigHTTP(t *testing.T, kind string, allowShutdownError ...bool) (*service.Service, *client.Client, string, *configNativePeer, resource.Session) {
	t.Helper()
	liveConfigBuild.Do(func() {
		liveConfigBinary = filepath.Join(root, "live-config-peer")
		out, err := exec.CommandContext(ctx(t), "go", "build", "-o", liveConfigBinary, "./testdata/live-config-peer").CombinedOutput()
		if err != nil {
			liveConfigBuildErr = fmt.Errorf("build peer: %w: %s", err, out)
		}
	})
	require.NoError(t, liveConfigBuildErr)
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	peer := newConfigNativePeer(t, kind, cwd)
	configDir := t.TempDir()
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	var factory runtime.Factory = codex.Factory{RecoveryDir: t.TempDir()}
	if kind == "claude" {
		factory = claude.Factory{RecoveryDir: t.TempDir()}
	}
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{kind: configNativeFactory{Factory: factory, peer: peer}}))
	t.Cleanup(func() {
		err := svc.Shutdown(ctx(t))
		if len(allowShutdownError) > 0 && allowShutdownError[0] {
			t.Logf("disconnected native cleanup result: %v", err)
		} else {
			require.NoError(t, err)
		}
	})
	_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: kind, Config: runtime.Config{"executable": liveConfigBinary, "envPolicy": "replace", "env": map[string]any{"CONFIG_PEER_ADDR": peer.listener.Addr().String(), "CLAUDE_CONFIG_DIR": configDir}}}})
	require.NoError(t, err)
	h, err := transport.New(svc, transport.Options{LocalOnly: true})
	require.NoError(t, err)
	t.Cleanup(h.Shutdown)
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	c := newHTTPClient(t, server.URL, "")
	var created resource.Session
	require.NoError(t, c.CreateSession(ctx(t), resource.Session{Metadata: resource.Metadata{Namespace: "one", Name: "configured"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: cwd, Config: &runtime.InitialSessionConfig{Model: "initial/model", Effort: "initial/effort"}}}}, &created))
	if kind == "claude" {
		key := regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(cwd, "-")
		dir := filepath.Join(configDir, "projects", key)
		require.NoError(t, os.MkdirAll(dir, 0o700))
		row := map[string]any{"type": "user", "uuid": "saved", "parentUuid": nil, "sessionId": created.Status.SessionID, "cwd": cwd, "message": map[string]any{"role": "user", "content": "retained"}}
		raw, err := json.Marshal(row)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, created.Status.SessionID+".jsonl"), append(raw, '\n'), 0o600))
	}
	return svc, c, server.URL, peer, created
}

type configCallResult struct {
	receipt runtime.ConfigReceipt
	err     error
}

func liveConfigAsync(t *testing.T, c *client.Client, ref resource.SessionRef, change runtime.ConfigChange) <-chan configCallResult {
	t.Helper()
	done := make(chan configCallResult, 1)
	go func() {
		var receipt runtime.ConfigReceipt
		err := c.SetSessionConfig(ctx(t), ref, change, &receipt)
		done <- configCallResult{receipt, err}
	}()
	return done
}

func liveConfigResult(t *testing.T, done <-chan configCallResult) configCallResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-ctx(t).Done():
		t.Fatal("configuration call did not settle")
		return configCallResult{}
	}
}

func TestLiveSessionConfigurationNativePublic(t *testing.T) {
	for _, kind := range []string{"codex", "claude"} {
		t.Run(kind, func(t *testing.T) {
			svc, c, url, peer, created := liveConfigHTTP(t, kind)
			ref := resource.SessionRef{SessionID: created.Status.SessionID}
			for _, id := range []agent.ConfigID{agent.ConfigModel, agent.ConfigEffort} {
				change := runtime.ConfigChange{ID: id, Value: " Native/Value " + string(id)}
				for repeat := 0; repeat < 2; repeat++ {
					// The first model request traverses the real massctl process.
					var done <-chan configCallResult
					var cliDone chan error
					if id == agent.ConfigModel && repeat == 0 {
						cliDone = make(chan error, 1)
						go func() {
							raw, err := exec.CommandContext(ctx(t), ctl, "--url", url, "-o", "json", "session", "set-config", "configured", "-n", "one", "--id", string(id), "--value", change.Value).CombinedOutput()
							if err == nil {
								var receipt runtime.ConfigReceipt
								err = json.Unmarshal(raw, &receipt)
								if err == nil && receipt.Change != change {
									err = fmt.Errorf("CLI receipt mismatch: %s", raw)
								}
							}
							cliDone <- err
						}()
					} else {
						done = liveConfigAsync(t, c, ref, change)
					}
					call := peer.next(t)
					if kind == "codex" {
						require.Equal(t, "thread/settings/update", call.method)
						require.Equal(t, peer.id, call.params["threadId"])
						field := string(id)
						require.Equal(t, change.Value, call.params[field])
						require.Len(t, call.params, 2)
					} else if id == agent.ConfigModel {
						require.Equal(t, "set_model", call.method)
						require.Equal(t, change.Value, call.params["model"])
					} else {
						require.Equal(t, "get_settings", call.method)
						call.respond(map[string]any{"applied": map[string]any{"ultracode": true}})
						call = peer.next(t)
						require.Equal(t, "apply_flag_settings", call.method)
						require.Equal(t, map[string]any{"effortLevel": change.Value, "ultracode": false}, call.params["settings"])
					}
					call.respond(map[string]any{})
					if cliDone != nil {
						require.NoError(t, <-cliDone)
					} else {
						result := liveConfigResult(t, done)
						require.NoError(t, result.err)
						require.Equal(t, change, result.receipt.Change)
						require.Equal(t, agent.Acceptance{Sent: runtime.Yes(), NativeAccepted: runtime.Yes()}, result.receipt.Acceptance)
					}
				}
			}
			// A native refusal is preserved and public diagnostics do not expose the peer's secret.
			change := runtime.ConfigChange{ID: agent.ConfigModel, Value: "rejected/model"}
			done := liveConfigAsync(t, c, resource.SessionRef{Namespace: "one", Name: "configured"}, change)
			peer.next(t).reject()
			result := liveConfigResult(t, done)
			var problem *runtime.OperationError
			require.ErrorAs(t, result.err, &problem)
			require.Equal(t, "native_rejected", problem.Code)
			require.NotContains(t, problem.Message, "SECRET")
			current, err := svc.GetSession(ref)
			require.NoError(t, err)
			require.Equal(t, created.Spec, current.Spec)
			require.Equal(t, created.Metadata.Generation, current.Metadata.Generation)
			history, err := svc.ListHistoryEvents(ref, store.HistoryQuery{})
			require.NoError(t, err)
			for _, event := range history.Events {
				require.NotEqual(t, "session.config_option_update", event.Type())
			}
			require.NoError(t, c.CloseSession(ctx(t), ref, nil))
			var resumed resource.Session
			require.NoError(t, c.ResumeSession(ctx(t), ref, &resumed))
			require.Equal(t, created.Spec, resumed.Spec)
			if kind == "claude" {
				for n := 0; n < 2; n++ {
					select {
					case args := <-peer.opened:
						require.Contains(t, args, "--model=initial/model")
						require.Contains(t, args, "--effort=initial/effort")
					case <-ctx(t).Done():
						t.Fatal("missing process args")
					}
				}
			} else {
				peer.mu.Lock()
				trace := strings.Join(peer.trace, "\n")
				peer.mu.Unlock()
				require.Contains(t, trace, `"model":"initial/model"`)
				require.Contains(t, trace, `"model_reasoning_effort":"initial/effort"`)
			}
			require.NoError(t, c.CloseSession(ctx(t), ref, nil))
		})
	}
}

func TestLiveSessionConfigurationNativeLifecycle(t *testing.T) {
	for _, kind := range []string{"codex", "claude"} {
		for _, operation := range []string{"close", "stop"} {
			t.Run(kind+"/"+operation, func(t *testing.T) {
				svc, c, _, peer, created := liveConfigHTTP(t, kind)
				ref := resource.SessionRef{SessionID: created.Status.SessionID}
				// The peer, rather than elapsed time, proves dispatch before lifecycle work.
				caller, cancel := context.WithCancel(ctx(t))
				result := make(chan configCallResult, 1)
				change := runtime.ConfigChange{ID: agent.ConfigModel, Value: "original/run"}
				go func() { r, err := svc.SetSessionConfig(caller, ref, change); result <- configCallResult{r, err} }()
				old := peer.next(t)
				cancel()
				abandoned := liveConfigResult(t, result)
				var problem *runtime.OperationError
				require.ErrorAs(t, abandoned.err, &problem)
				require.Equal(t, "outcome_unknown", problem.Code)
				require.Equal(t, change, abandoned.receipt.Change)
				require.Nil(t, abandoned.receipt.Acceptance.Sent)
				lifecycle := make(chan error, 1)
				go func() {
					var err error
					if operation == "stop" {
						_, err = svc.StopRuntime(ctx(t), "agent")
					} else {
						_, err = svc.CloseSession(ctx(t), ref)
					}
					lifecycle <- err
				}()
				require.Eventually(t, func() bool {
					if operation == "stop" {
						r, err := svc.GetRuntime("agent")
						return err == nil && r.Status.State == "stopping"
					}
					r, err := svc.GetSession(ref)
					return err == nil && r.Status.State == "closing"
				}, time.Second, time.Millisecond)
				// Both new config and resume are rejected under the sealed original owner.
				_, err := svc.SetSessionConfig(ctx(t), ref, runtime.ConfigChange{ID: agent.ConfigModel, Value: "must-not-send"})
				require.Error(t, err)
				select {
				case err := <-lifecycle:
					t.Fatalf("lifecycle released before original setter settled: %v", err)
				default:
				}
				old.respond(map[string]any{})
				require.NoError(t, <-lifecycle)
				var resumed resource.Session
				require.NoError(t, c.ResumeSession(ctx(t), ref, &resumed))
				require.Equal(t, created.Spec, resumed.Spec)
				require.Equal(t, created.Metadata.Generation, resumed.Metadata.Generation)
				// A delayed duplicate remains on its original native connection/request ID.
				// Claude's old process is gone; Codex keeps one shared provider connection.
				old.respond(map[string]any{})
				fresh := runtime.ConfigChange{ID: agent.ConfigModel, Value: "new/run"}
				done := liveConfigAsync(t, c, ref, fresh)
				next := peer.next(t)
				require.Equal(t, fresh.Value, next.params["model"])
				next.respond(map[string]any{})
				settled := liveConfigResult(t, done)
				require.NoError(t, settled.err)
				require.Equal(t, fresh, settled.receipt.Change)
				var current resource.Session
				require.NoError(t, c.GetSession(ctx(t), ref, &current))
				require.Equal(t, created.Spec, current.Spec)
				require.Equal(t, created.Status.AppliedGeneration, current.Status.AppliedGeneration)
				require.NoError(t, c.CloseSession(ctx(t), ref, nil))
			})
		}
	}
}

func TestLiveSessionConfigurationNativeConcurrent(t *testing.T) {
	for _, kind := range []string{"codex", "claude"} {
		t.Run(kind, func(t *testing.T) {
			_, c, _, peer, created := liveConfigHTTP(t, kind)
			ref := resource.SessionRef{SessionID: created.Status.SessionID}
			a := runtime.ConfigChange{ID: agent.ConfigModel, Value: "first/model"}
			b := runtime.ConfigChange{ID: agent.ConfigModel, Value: "second/model"}
			one := liveConfigAsync(t, c, ref, a)
			first := peer.next(t)
			two := liveConfigAsync(t, c, ref, b)
			second := peer.next(t)
			require.Equal(t, a.Value, first.params["model"])
			require.Equal(t, b.Value, second.params["model"])
			second.respond(map[string]any{})
			r2 := liveConfigResult(t, two)
			require.NoError(t, r2.err)
			require.Equal(t, b, r2.receipt.Change)
			first.reject()
			r1 := liveConfigResult(t, one)
			var public *runtime.OperationError
			require.ErrorAs(t, r1.err, &public)
			require.Equal(t, "native_rejected", public.Code)
			require.NoError(t, c.CloseSession(ctx(t), ref, nil))
		})
	}
}

func TestLiveSessionConfigurationNativeUncertainAndPreflight(t *testing.T) {
	for _, kind := range []string{"codex", "claude"} {
		t.Run(kind+"/dispatched_unknown", func(t *testing.T) {
			_, c, _, peer, created := liveConfigHTTP(t, kind, true)
			ref := resource.SessionRef{SessionID: created.Status.SessionID}
			done := liveConfigAsync(t, c, ref, runtime.ConfigChange{ID: agent.ConfigModel, Value: "unknown/model"})
			peer.next(t).disconnect()
			result := liveConfigResult(t, done)
			var problem *runtime.OperationError
			require.ErrorAs(t, result.err, &problem)
			require.Equal(t, "outcome_unknown", problem.Code)
			require.Equal(t, "set_config_outcome_unknown", problem.Reason)
			require.Zero(t, result.receipt)
		})
	}
	t.Run("claude/effort_preflight", func(t *testing.T) {
		svc, c, _, peer, created := liveConfigHTTP(t, "claude")
		ref := resource.SessionRef{SessionID: created.Status.SessionID}
		change := runtime.ConfigChange{ID: agent.ConfigEffort, Value: "effort"}
		done := make(chan configCallResult, 1)
		go func() { r, err := svc.SetSessionConfig(ctx(t), ref, change); done <- configCallResult{r, err} }()
		call := peer.next(t)
		require.Equal(t, "get_settings", call.method)
		call.reject()
		result := liveConfigResult(t, done)
		var problem *runtime.OperationError
		require.ErrorAs(t, result.err, &problem)
		require.Equal(t, "unsupported", problem.Code)
		require.Equal(t, change, result.receipt.Change)
		require.Equal(t, runtime.Bool(false), result.receipt.Acceptance.Sent)
		require.Nil(t, result.receipt.Acceptance.NativeAccepted)
		// A preflight rejection cannot silently dispatch apply_flag_settings.
		nextDone := liveConfigAsync(t, c, ref, runtime.ConfigChange{ID: agent.ConfigModel, Value: "still-open"})
		call = peer.next(t)
		require.Equal(t, "set_model", call.method)
		call.respond(map[string]any{})
		require.NoError(t, liveConfigResult(t, nextDone).err)
		require.NoError(t, c.CloseSession(ctx(t), ref, nil))
	})
}

func TestLiveSessionConfigurationNativeNotSentContext(t *testing.T) {
	for _, kind := range []string{"codex", "claude"} {
		t.Run(kind, func(t *testing.T) {
			_, c, _, peer, created := liveConfigHTTP(t, kind)
			ref := resource.SessionRef{SessionID: created.Status.SessionID}
			expired, cancel := context.WithDeadline(t.Context(), time.Unix(1, 0))
			defer cancel()
			change := runtime.ConfigChange{ID: agent.ConfigModel, Value: "must-not-write"}
			receipt, err := peer.driver.(runtime.SessionConfigDriver).SetSessionConfig(expired, ref.SessionID, change)
			var problem *runtime.OperationError
			require.ErrorAs(t, err, &problem)
			require.Equal(t, change, receipt.Change)
			require.Equal(t, runtime.Bool(false), receipt.Acceptance.Sent)
			require.Nil(t, receipt.Acceptance.NativeAccepted)
			require.Nil(t, receipt.Acceptance.NativeRecorded)
			var sdkError *agent.Error
			require.ErrorAs(t, err, &sdkError)
			require.Equal(t, agent.ErrorUnavailable, sdkError.Code)
			require.Equal(t, "set_config_failed", sdkError.Reason)
			require.ErrorIs(t, sdkError.Cause, context.DeadlineExceeded)
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.Equal(t, sdkError.Reason, problem.Reason)
			require.Equal(t, "deadline_exceeded", problem.Code)
			done := liveConfigAsync(t, c, ref, runtime.ConfigChange{ID: agent.ConfigModel, Value: "only-native-write"})
			call := peer.next(t)
			require.Equal(t, "only-native-write", call.params["model"])
			call.respond(map[string]any{})
			require.NoError(t, liveConfigResult(t, done).err)
			require.NoError(t, c.CloseSession(ctx(t), ref, nil))
		})
	}
}
