package core_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"
	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

var dispatchBuild struct {
	sync.Once
	massd, relay string
	log          []byte
	err          error
}

func dispatchBinaries(t *testing.T) (string, string) {
	t.Helper()
	dispatchBuild.Do(func() {
		dir := filepath.Dir(binary)
		dispatchBuild.massd = filepath.Join(dir, "massd")
		dispatchBuild.relay = filepath.Join(dir, "live-config-peer")
		for target, pkg := range map[string]string{dispatchBuild.massd: "./cmd/massd", dispatchBuild.relay: "./tests/integration/testdata/live-config-peer"} {
			cmd := exec.CommandContext(t.Context(), "go", "build", "-race", "-o", target, pkg)
			cmd.Dir = "../../../mass-go"
			cmd.Env = append(os.Environ(), "GOPROXY=off", "GOSUMDB=off")
			dispatchBuild.log, dispatchBuild.err = cmd.CombinedOutput()
			if dispatchBuild.err != nil {
				return
			}
		}
	})
	require.NoError(t, dispatchBuild.err, "%s", dispatchBuild.log)
	return dispatchBuild.massd, dispatchBuild.relay
}

type nativeSubmitCall struct {
	method, session, text string
	answer                chan nativeSubmitAnswer
	notify                func(string, any)
}

type nativeSubmitAnswer struct {
	code           int
	message        string
	unknown        bool
	complete       bool
	final          string
	workID         string
	endBeforeReply bool
}

type nativeSubmitPeer struct {
	listener        net.Listener
	calls           chan nativeSubmitCall
	trace           chan string
	active          sync.Map
	done            chan struct{}
	released        chan struct{}
	threadIDs       atomic.Int64
	workspaces      sync.Map
	initialConfigs  sync.Map
	resumeConfigs   sync.Map
	nativeQuestions sync.Map
	resumeGates     sync.Map
	nextStartGate   atomic.Pointer[nativeOpenGate]
}

type nativeOpenGate struct {
	entered chan struct{}
	release chan struct{}
}

func newNativeSubmitPeer(t *testing.T) *nativeSubmitPeer {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	p := &nativeSubmitPeer{listener: l, calls: make(chan nativeSubmitCall, 32), trace: make(chan string, 256), done: make(chan struct{}), released: make(chan struct{}, 16)}
	var accepting, serving sync.WaitGroup
	accepting.Go(func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			p.active.Store(conn, struct{}{})
			serving.Go(func() {
				defer p.active.Delete(conn)
				defer conn.Close()
				p.serve(conn)
				p.released <- struct{}{}
			})
		}
	})
	t.Cleanup(func() {
		close(p.done)
		require.NoError(t, l.Close())
		accepting.Wait()
		p.active.Range(func(key, _ any) bool {
			_ = key.(net.Conn).Close()
			return true
		})
		serving.Wait()
		_, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", l.Addr().String())
		require.Error(t, err)
		close(p.trace)
		var trace []string
		for method := range p.trace {
			trace = append(trace, method)
		}
		t.Logf("native CLI methods: %v", trace)
		t.Log("controlled native CLI listener and connections released")
	})
	return p
}

type nativeSubmitRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		ThreadID          string `json:"threadId"`
		TurnID            string `json:"turnId"`
		Cwd               string `json:"cwd"`
		Model             string `json:"model"`
		ApprovalPolicy    string `json:"approvalPolicy"`
		ApprovalsReviewer string `json:"approvalsReviewer"`
		Sandbox           string `json:"sandbox"`
		Config            struct {
			Effort    string `json:"model_reasoning_effort"`
			Questions *bool  `json:"tools.experimental_request_user_input.enabled"`
		} `json:"config"`
		Input []struct {
			Text string `json:"text"`
		} `json:"input"`
		Items []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"items"`
	} `json:"params"`
}

func (p *nativeSubmitPeer) serve(conn net.Conn) {
	enc := json.NewEncoder(conn)
	var writes sync.Mutex
	send := func(data any) {
		writes.Lock()
		defer writes.Unlock()
		if err := enc.Encode(data); err != nil {
			_ = conn.Close()
		}
	}
	idle := map[string]string{"type": "idle"}
	notify := func(method string, params any) {
		send(map[string]any{"method": method, "params": params})
	}
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64*1024), 512*1024)
	for scanner.Scan() {
		var req nativeSubmitRequest
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			return
		}
		session := req.Params.ThreadID
		workspace := func() string {
			cwd, _ := p.workspaces.Load(session)
			value, _ := cwd.(string)
			return value
		}
		thread := func() map[string]any {
			return map[string]any{
				"id": session, "sessionId": session, "cliVersion": "synthetic", "createdAt": 1, "updatedAt": 2,
				"cwd": workspace(), "ephemeral": false, "modelProvider": "synthetic", "preview": "", "projectId": nil, "source": "appServer", "status": idle, "turns": []any{},
			}
		}
		respond := func(data any) { send(map[string]any{"id": req.ID, "result": data}) }
		if req.Method != "" {
			p.trace <- req.Method
		}
		switch req.Method {
		case "", "initialized":
		case "initialize":
			respond(map[string]string{"userAgent": "synthetic", "codexHome": "/synthetic", "platformFamily": "unix", "platformOs": "linux"})
		case "thread/start", "thread/resume":
			config := runtime.InitialSessionConfig{Model: req.Params.Model, Effort: req.Params.Config.Effort}
			reviewer := req.Params.ApprovalsReviewer
			var sandbox string
			switch {
			case req.Params.ApprovalPolicy == "never" && reviewer == "" && req.Params.Sandbox == "danger-full-access":
				reviewer, sandbox = "user", "dangerFullAccess"
				config.PermissionPreset = agent.PermissionPresetFullAccess
			case req.Params.ApprovalPolicy == "on-request" && reviewer == "user" && req.Params.Sandbox == "workspace-write":
				sandbox = "workspaceWrite"
				config.PermissionPreset = agent.PermissionPresetManualReview
			case req.Params.ApprovalPolicy == "on-request" && reviewer == "auto_review" && req.Params.Sandbox == "workspace-write":
				sandbox = "workspaceWrite"
				config.PermissionPreset = agent.PermissionPresetAutoReview
			default:
				send(map[string]any{"id": req.ID, "error": map[string]any{"code": -32602, "message": "unexpected synthetic permission preset"}})
				continue
			}
			if req.Method == "thread/start" {
				session = "native-event-dispatch-" + strconv.FormatInt(p.threadIDs.Add(1), 10)
				p.workspaces.Store(session, req.Params.Cwd)
				p.initialConfigs.Store(session, config)
			} else {
				p.resumeConfigs.Store(session, config)
			}
			p.nativeQuestions.Store(session, req.Params.Config.Questions)
			opened := func() {
				respond(map[string]any{
					"thread": thread(), "approvalPolicy": req.Params.ApprovalPolicy, "approvalsReviewer": reviewer, "cwd": workspace(),
					"model": "synthetic", "modelProvider": "synthetic", "sandbox": map[string]string{"type": sandbox},
				})
				notify("thread/status/changed", map[string]any{"threadId": session, "status": idle})
			}
			var gate *nativeOpenGate
			if req.Method == "thread/start" {
				gate = p.nextStartGate.Swap(nil)
			} else if value, found := p.resumeGates.LoadAndDelete(session); found {
				gate = value.(*nativeOpenGate)
			}
			if gate == nil {
				opened()
				continue
			}
			go func() {
				close(gate.entered)
				select {
				case <-gate.release:
					opened()
				case <-p.done:
				}
			}()
		case "thread/read":
			respond(map[string]any{"thread": thread()})
		case "thread/unsubscribe":
			respond(map[string]string{"status": "unsubscribed"})
		case "turn/interrupt":
			respond(map[string]any{})
			notify("turn/completed", map[string]any{"threadId": session, "turn": map[string]any{"id": req.Params.TurnID, "status": "interrupted", "items": []any{}}})
		case "thread/inject_items", "turn/start":
			go p.input(req, respond, send, notify)
		default:
			send(map[string]any{"id": req.ID, "error": map[string]any{"code": -32601, "message": "unexpected synthetic request"}})
		}
	}
}

func (p *nativeSubmitPeer) input(req nativeSubmitRequest, respond, send func(any), notify func(string, any)) {
	call := nativeSubmitCall{method: req.Method, session: req.Params.ThreadID, answer: make(chan nativeSubmitAnswer, 1), notify: notify}
	switch {
	case req.Method == "thread/inject_items" && len(req.Params.Items) == 1 && len(req.Params.Items[0].Content) == 1:
		call.text = req.Params.Items[0].Content[0].Text
	case len(req.Params.Input) == 1:
		call.text = req.Params.Input[0].Text
	}
	select {
	case p.calls <- call:
	case <-p.done:
		return
	}
	var result nativeSubmitAnswer
	select {
	case result = <-call.answer:
	case <-p.done:
		return
	}
	switch {
	case result.code != 0:
		send(map[string]any{"id": req.ID, "error": map[string]any{"code": result.code, "message": result.message}})
	case call.method == "thread/inject_items":
		respond(map[string]any{})
	default:
		id := result.workID
		if id == "" {
			id = "turn-" + call.session
		}
		if result.unknown {
			id = ""
		}
		turn := map[string]any{"id": id, "status": "inProgress", "items": []any{}}
		if result.endBeforeReply {
			notify("turn/started", map[string]any{"threadId": call.session, "turn": turn})
			call.end(id)
			respond(map[string]any{"turn": turn})
			return
		}
		respond(map[string]any{"turn": turn})
		if !result.unknown {
			notify("turn/started", map[string]any{"threadId": call.session, "turn": turn})
		}
		if result.complete {
			if result.final != "" {
				notify("item/completed", map[string]any{
					"threadId": call.session, "turnId": id, "completedAtMs": 1,
					"item": map[string]any{"id": "work-final", "type": "agentMessage", "text": result.final},
				})
			}
			call.end(id)
		}
	}
}

func (p *nativeSubmitPeer) next(t *testing.T, method string) nativeSubmitCall {
	t.Helper()
	select {
	case call := <-p.calls:
		if method != "" {
			require.Equal(t, method, call.method)
		}
		require.NotEmpty(t, call.text)
		return call
	case <-time.After(8 * time.Second):
		t.Fatalf("native call %s not observed", method)
		return nativeSubmitCall{}
	}
}

func (p *nativeSubmitPeer) quiet(t *testing.T, duration time.Duration) {
	t.Helper()
	select {
	case call := <-p.calls:
		t.Fatalf("unexpected native input %s on %s", call.method, call.session)
	case <-time.After(duration):
	}
}

func (call nativeSubmitCall) accept() { call.answer <- nativeSubmitAnswer{} }

func (call nativeSubmitCall) end(workID string) {
	call.notify("turn/completed", map[string]any{
		"threadId": call.session, "turn": map[string]any{"id": workID, "status": "completed", "items": []any{}},
	})
}

func (call nativeSubmitCall) idle() {
	call.notify("thread/status/changed", map[string]any{"threadId": call.session, "status": map[string]string{"type": "idle"}})
}

func startEventTestMassd(t *testing.T, peer *nativeSubmitPeer) (string, *client.Client) {
	t.Helper()
	endpoint, c, _ := startEventTestMassdWithData(t, peer)
	return endpoint, c
}

func startEventTestMassdWithData(t *testing.T, peer *nativeSubmitPeer) (string, *client.Client, string) {
	t.Helper()
	massd, relay := dispatchBinaries(t)
	return startNativeMassd(t, massd, resource.RuntimeSpec{
		Type:     "codex",
		Config:   runtime.Config{"executable": relay, "envPolicy": "replace", "env": map[string]any{"CONFIG_PEER_ADDR": peer.listener.Addr().String()}},
		Features: &runtime.SessionFeatures{AgentQuestions: new(true)},
	})
}

func startNativeMassd(t *testing.T, massd string, spec resource.RuntimeSpec) (string, *client.Client, string) {
	t.Helper()
	dir := t.TempDir()
	config := filepath.Join(dir, "massd.yaml")
	write(t, config, "{}")
	data := filepath.Join(dir, "massd")
	cmd := exec.CommandContext(context.Background(), massd, "--listen", "127.0.0.1:0", "--config", config, "--data", data, "--session-idle-timeout", "0")
	cmd.Env = []string{"HOME=" + dir, "PATH=" + dir}
	log, err := os.Create(filepath.Join(dir, "massd.log"))
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	cmd.Stderr = log
	require.NoError(t, cmd.Start())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var startup struct {
		URL string `json:"url"`
	}
	t.Cleanup(func() {
		require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
		select {
		case err := <-done:
			_ = log.Close()
			if err != nil {
				data, _ := os.ReadFile(log.Name())
				t.Errorf("massd shutdown: %v\n%s", err, data)
			}
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			_ = log.Close()
			t.Error("massd did not settle")
		}
		if startup.URL != "" {
			c, err := client.New(startup.URL, "", client.Options{WriteTimeout: time.Second, SubscriptionQueueCapacity: 8})
			require.NoError(t, err)
			defer c.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			require.Error(t, c.Initialize(ctx, nil, nil))
		}
		t.Logf("massd process released pid=%d", cmd.Process.Pid)
	})
	require.NoError(t, json.NewDecoder(stdout).Decode(&startup))
	require.NotEmpty(t, startup.URL)
	c, err := client.New(startup.URL, "", client.Options{WriteTimeout: time.Second, SubscriptionQueueCapacity: 8})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.Close()) })
	r := resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: spec}
	require.NoError(t, c.CreateRuntime(t.Context(), r, nil))
	return startup.URL, c, data
}
