package recordingtest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	agenttestify "github.com/zoumo/agent-sdk-testify"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

const (
	project           = "/var/tmp/mass-consumer/codex-basic/project"
	permissionProject = "/var/tmp/mass-consumer/codex-permission/project"
	prompt            = "Reply with one short greeting. Do not use tools or inspect files."
	permissionPrompt  = "Use the shell tool to execute this exact command in the current project: `printf mass-approved > permission-proof.txt`. The command writes only that project-local file. Do not use apply_patch, do not read other files, and do not just describe the command. Then report the command exit status."
)

type route = agenttestify.Route

type factEvent struct {
	Kind       string `json:"kind"`
	Text       string `json:"text,omitempty"`
	ToolCallID string `json:"toolCallId,omitempty"`
	Status     string `json:"status,omitempty"`
	ExitCode   *int32 `json:"exitCode,omitempty"`
}

type facts struct {
	SessionID   string              `json:"sessionId"`
	State       events.SessionState `json:"state"`
	Accepted    bool                `json:"accepted"`
	Input       *inputFacts         `json:"input,omitempty"`
	Events      []factEvent         `json:"events"`
	StreamKinds []string            `json:"streamKinds,omitempty"`
	Permission  *permissionFacts    `json:"permission,omitempty"`
}

type inputFacts struct {
	Sent           bool  `json:"sent"`
	NativeAccepted *bool `json:"nativeAccepted"`
}

type permissionAnswer struct {
	OptionID       string `json:"optionId"`
	Sent           bool   `json:"sent"`
	NativeAccepted *bool  `json:"nativeAccepted"`
}

type permissionFacts struct {
	Interaction       string                    `json:"interaction"`
	Request           events.InteractionRequest `json:"request"`
	StateBeforeAnswer events.SessionState       `json:"stateBeforeAnswer"`
	Answer            permissionAnswer          `json:"answer"`
	StateAfterAnswer  events.SessionState       `json:"stateAfterAnswer"`
	Completed         bool                      `json:"completed"`
	Closed            bool                      `json:"closed,omitempty"`
}

type caseIndex struct {
	Provider          string            `json:"provider"`
	Case              string            `json:"case"`
	Provenance        string            `json:"provenance"`
	RecordedAt        string            `json:"recordedAt"`
	SourceSession     string            `json:"sourceSession"`
	ExportPath        string            `json:"exportPath"`
	Hashes            map[string]string `json:"sha256"`
	Versions          map[string]string `json:"versions"`
	ToolHashes        map[string]string `json:"toolSHA256"`
	CodexPath         string            `json:"codexPath,omitempty"`
	ClaudePath        string            `json:"claudePath,omitempty"`
	ClaudeAuthMethod  string            `json:"claudeAuthMethod,omitempty"`
	ClaudeAPIProvider string            `json:"claudeAPIProvider,omitempty"`
	Prompt            string            `json:"prompt"`
	Project           string            `json:"project"`
	NativeConfig      []string          `json:"nativeConfig,omitempty"`
	Facts             facts             `json:"facts"`
}

type harness struct {
	t                  *testing.T
	ctx                context.Context
	root, tool, daemon string
	diagnostic         *diagnostic
	phase              string
}

type flowCase struct {
	name               string
	provider           string
	prompt             string
	project            string
	initialInput       bool
	ignoreStreamKinds  bool
	expectPermission   bool
	expectedPermission string
	expectedToolOutput string
	runtimeEnvironment map[string]string
	sessionConfig      *runtime.InitialSessionConfig
	evidence           string
	budget             time.Duration
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	_, source, _, ok := goruntime.Caller(0)
	require.True(t, ok)
	root := filepath.Dir(filepath.Dir(filepath.Dir(source)))
	dir := t.TempDir()
	h := &harness{t: t, ctx: ctx, root: root, tool: filepath.Join(dir, "testify"), daemon: filepath.Join(dir, "massd")}
	t.Cleanup(cancel)
	h.command("go", "build", "-o", h.tool, "github.com/zoumo/agent-sdk-testify/cmd/agent-sdk-testify")
	h.command("go", "build", "-o", h.daemon, "./cmd/massd")
	return h
}

func (h *harness) command(name string, args ...string) string {
	h.t.Helper()
	out, err := h.commandError(name, args...)
	require.NoError(h.t, err, "%s failed", filepath.Base(name))
	return out
}

func (h *harness) commandError(name string, args ...string) (string, error) {
	h.t.Helper()
	if h.diagnostic != nil && name != "git" {
		stage := h.phase
		if stage == "" {
			stage = "build-or-cli"
		}
		if len(args) >= 2 && (args[0] == "login" || args[0] == "auth") && args[1] == "status" {
			stage = "auth"
		}
		out, err := h.diagnostic.command(h.ctx, 10*time.Second, stage, name, args...)
		return strings.TrimSpace(string(out)), err
	}
	commandContext, cancel := context.WithTimeout(h.ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(commandContext, name, args...)
	cmd.Dir = h.root
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

const flowBudget = 12 * time.Minute

func (h *harness) flowContext(budget time.Duration) (context.Context, context.CancelFunc) {
	if budget <= 0 {
		budget = flowBudget
	}
	if h.diagnostic != nil {
		h.diagnostic.report.FlowBudget = budget
	}
	return context.WithTimeout(h.ctx, budget)
}

func readJSON(t *testing.T, path string, out any) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, out))
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(data, '\n'), 0o600))
}

func hashFile(t *testing.T, path string) string {
	t.Helper()
	value, err := fileSHA256(path)
	require.NoError(t, err)
	return value
}

func fileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func quote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

func (h *harness) reserveDiagnostic(provider, scratch string) {
	output := os.Getenv("MASS_RECORD_DIAGNOSTICS")
	if output == "" {
		output = filepath.Join(scratch, "diagnosis")
	}
	diagnostic, err := newDiagnostic(output, provider)
	require.NoError(h.t, err)
	diagnostic.report.Model, diagnostic.report.ModelSource = explicitRecordingModel("")
	diagnostic.report.Boundaries["native_open"] = "not_executed"
	diagnostic.report.RecordingRoot = scratch
	h.diagnostic = diagnostic
	h.t.Cleanup(func() {
		h.diagnostic.observeNative(scratch)
		if h.t.Failed() {
			h.diagnostic.report.Status = "blocker"
			h.diagnostic.report.Blockers = append(h.diagnostic.report.Blockers, "recording test failed at "+h.diagnostic.report.LastStage)
		}
		h.diagnostic.retainArtifacts(scratch)
		require.NoError(h.t, h.diagnostic.save())
	})
}

func (h *harness) preflight(provider string) DiagnosticReport {
	h.t.Helper()
	if err := h.diagnostic.preflight(h.ctx, DiagnosticOptions{Provider: provider}); err != nil {
		h.diagnostic.report.Blockers = append(h.diagnostic.report.Blockers, string(redactDiagnostic([]byte(err.Error()))))
		require.NoError(h.t, err)
	}
	require.NoError(h.t, h.diagnostic.save())
	return h.diagnostic.report
}

func (h *harness) prepare(session, sourceFlag, source, mode, expectedProject string) route {
	h.t.Helper()
	h.phase = "testify-prepare"
	h.command(h.tool, "prepare", sourceFlag, source, "--fixture-dir", session)
	fixture, err := agenttestify.Open(session)
	require.NoError(h.t, err)
	require.NotEmpty(h.t, fixture.FixtureID)
	selected, ok := fixture.Routes[mode]
	require.True(h.t, ok)
	require.True(h.t, selected.Enabled)
	if mode == "mock" {
		require.Equal(h.t, "external-sdk", selected.Executor)
		require.Equal(h.t, "replay", selected.Target)
		require.Equal(h.t, "consumer", selected.Provenance)
	}
	if expectedProject != "" {
		require.Equal(h.t, expectedProject, selected.ProtocolCwd)
	}
	require.True(h.t, filepath.IsAbs(selected.ProtocolCwd))
	info, err := os.Stat(selected.Workspace)
	require.NoError(h.t, err)
	require.True(h.t, info.IsDir())
	require.NotEmpty(h.t, selected.Env)
	require.Len(h.t, selected.Connections, 1)
	return selected
}

func (h *harness) flow(route route, executable string, c flowCase) facts {
	t := h.t
	t.Helper()
	if h.diagnostic != nil {
		h.diagnostic.phase("mass-flow:" + c.name)
	}
	defer func() {
		if h.diagnostic != nil && c.evidence != "" {
			h.diagnostic.observeNative(c.evidence)
			_ = h.diagnostic.save()
		}
	}()
	provider := c.provider
	if provider == "" {
		provider = "codex"
	}
	dir := t.TempDir()
	if c.evidence != "" {
		dir = c.evidence
		require.NoError(t, os.MkdirAll(dir, 0o700))
	}
	ctx, cancelFlow := h.flowContext(c.budget)
	flowStarted := time.Now()
	parentContext := h.ctx
	h.ctx = ctx
	defer func() {
		if h.diagnostic != nil {
			h.diagnostic.report.FlowElapsed = time.Since(flowStarted)
			h.diagnostic.report.FlowTimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
		}
		h.ctx = parentContext
		cancelFlow()
	}()
	config := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(config, []byte("listen: 127.0.0.1:0\nlocal: true\n"), 0o600))
	log, err := os.Create(filepath.Join(dir, "daemon.log"))
	require.NoError(t, err)
	defer log.Close()
	cmd := exec.CommandContext(parentContext, h.daemon, "--config", config, "--data", filepath.Join(dir, "data"))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 15 * time.Second
	cmd.Dir = route.Workspace
	cmd.Env = (runtime.ProcessConfig{EnvPolicy: "replace", Env: route.Env}).Environment(provider)
	cmd.Stderr = log
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	if h.diagnostic != nil {
		h.diagnostic.commandPID("massd", cmd.Process.Pid)
	}
	stopped := false
	stopResult := "not_started"
	var ready struct {
		URL string `json:"url"`
	}
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		defer func() {
			if !groupGone(cmd.Process.Pid) {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
			gone := waitGroupGone(cmd.Process.Pid)
			if h.diagnostic == nil {
				require.True(t, gone, "owned daemon process group must be released")
				return
			}
			for index := range h.diagnostic.report.Stages {
				stage := &h.diagnostic.report.Stages[index]
				if stage.PID == cmd.Process.Pid {
					stage.ProcessGone = processGone(cmd.Process.Pid)
					stage.GroupGone = gone
					stage.ExitCode = cmd.ProcessState.ExitCode()
				}
			}
			if ready.URL != "" {
				dialer := net.Dialer{Timeout: time.Second}
				conn, dialErr := dialer.DialContext(context.Background(), "tcp", strings.TrimPrefix(ready.URL, "http://"))
				if conn != nil {
					_ = conn.Close()
				}
				closed := dialErr != nil
				h.diagnostic.report.ListenerClosed = &closed
			}
			if !gone {
				h.diagnostic.report.Blockers = append(h.diagnostic.report.Blockers, "owned daemon process group cleanup unconfirmed")
				t.Error("owned daemon process group cleanup unconfirmed")
			}
			_ = h.diagnostic.save()
		}()
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			if h.diagnostic != nil {
				if err == nil && processGone(cmd.Process.Pid) {
					stopResult = "process_and_wait_confirmed"
				} else {
					stopResult = fmt.Sprintf("wait=%v processGone=%v", err, processGone(cmd.Process.Pid))
				}
				h.diagnostic.setCleanup("massd", stopResult)
				return
			}
			require.NoError(t, err)
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			if h.diagnostic != nil {
				h.diagnostic.setCleanup("massd", "killed_after_stop_timeout")
				return
			}
			t.Fatal("daemon failed to exit")
		}
	}
	defer stop()
	type readiness struct {
		URL string
		Err error
	}
	decoded := make(chan readiness, 1)
	go func() {
		var message struct {
			URL string `json:"url"`
		}
		decodeErr := json.NewDecoder(stdout).Decode(&message)
		decoded <- readiness{URL: message.URL, Err: decodeErr}
	}()
	select {
	case result := <-decoded:
		require.NoError(t, result.Err)
		ready.URL = result.URL
	case <-ctx.Done():
		require.NoError(t, ctx.Err(), "daemon readiness exceeded flow deadline")
	}
	require.NotEmpty(t, ready.URL)
	api := client.New(ready.URL, "")
	defer api.Close()
	var rt resource.Runtime
	runtimeStopped := false
	defer func() {
		if runtimeStopped {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		cleanupErr := api.StopRuntime(cleanupCtx, "recording", &rt)
		if h.diagnostic != nil {
			h.diagnostic.report.SDKCleanup = fmt.Sprintf("stop_on_failure=%v", cleanupErr)
			if cleanupErr == nil {
				readErr := api.GetRuntime(cleanupCtx, "recording", &rt)
				h.diagnostic.report.SDKCleanup += fmt.Sprintf("; readback=%v state=%s", readErr, rt.Status.State)
			}
			_ = h.diagnostic.save()
		}
	}()
	env := map[string]any{}
	for key, value := range route.Env {
		env[key] = value
	}
	for key, value := range c.runtimeEnvironment {
		env[key] = value
	}
	require.NoError(t, api.CreateRuntime(h.ctx, resource.Runtime{Metadata: resource.Metadata{Name: "recording"}, Spec: resource.RuntimeSpec{Type: provider, Config: runtime.Config{"executable": executable, "envPolicy": "replace", "env": env}}}, &rt))
	require.NoError(t, api.StartRuntime(h.ctx, "recording", &rt))
	require.Equal(t, "running", rt.Status.State)
	if h.diagnostic != nil {
		h.diagnostic.report.Boundaries["sdk"] = "runtime_started"
		h.diagnostic.phase("native-open")
	}
	var session resource.Session
	var receipt runtime.InputReceipt
	input := runtime.Input{Content: []events.ContentBlock{events.Text(c.prompt)}}
	if c.initialInput {
		var created resource.SessionCreated
		require.NoError(t, api.CreateSessionWithInput(h.ctx, resource.SessionCreateInput{Metadata: resource.Metadata{Name: c.name, Namespace: "default"}, Spec: resource.SessionSpec{RuntimeRef: "recording", SessionSpec: runtime.SessionSpec{Cwd: c.project, Config: c.sessionConfig}}, InitialInput: &input}, &created))
		session = created.Session
		require.NotNil(t, created.InitialInputReceipt)
		receipt = *created.InitialInputReceipt
	} else {
		require.NoError(t, api.CreateSession(h.ctx, resource.Session{Metadata: resource.Metadata{Name: c.name, Namespace: "default"}, Spec: resource.SessionSpec{RuntimeRef: "recording", SessionSpec: runtime.SessionSpec{Cwd: c.project, Config: c.sessionConfig}}}, &session))
	}
	require.NotEmpty(t, session.Status.SessionID)
	if h.diagnostic != nil {
		h.diagnostic.report.Boundaries["native_open"] = "session_id_observed"
	}
	ref := resource.SessionRef{SessionID: session.Status.SessionID}
	var byName resource.Session
	require.NoError(t, api.GetSession(h.ctx, resource.SessionRef{Namespace: "default", Name: c.name}, &byName))
	require.Equal(t, session.Status.SessionID, byName.Status.SessionID)
	observer, err := api.Observe(h.ctx, ref)
	require.NoError(t, err)
	defer observer.Close()
	if !c.initialInput {
		if h.diagnostic != nil {
			h.diagnostic.phase("native-input")
		}
		require.NoError(t, api.SendPrompt(h.ctx, ref, input, &receipt))
	}
	require.Equal(t, runtime.Yes(), receipt.Acceptance.Sent)
	if provider == "codex" {
		require.Equal(t, runtime.Yes(), receipt.Acceptance.NativeAccepted)
		if h.diagnostic != nil {
			h.diagnostic.report.Boundaries["native_input_ack"] = "native_accepted"
		}
	} else {
		require.Nil(t, receipt.Acceptance.NativeAccepted)
		if h.diagnostic != nil {
			h.diagnostic.report.Boundaries["native_input_ack"] = "sent_native_acceptance_unknown"
		}
	}
	if h.diagnostic != nil {
		h.diagnostic.phase("mass-sse-completion")
	}
	var persistent []events.SessionEvent
	result := facts{SessionID: ref.SessionID, Accepted: true}
	if provider == "claude" {
		result.Input = &inputFacts{Sent: true, NativeAccepted: receipt.Acceptance.NativeAccepted}
	}
	running := c.initialInput
	answered := false
	approvedToolCompleted := false
	var approvedToolOutput strings.Builder
	finished := false
	if c.initialInput {
		require.NoError(t, api.GetSession(h.ctx, ref, &session))
		switch session.Status.State {
		case events.Idle:
			finished = true
		case events.RequiresAction:
			require.True(t, c.expectPermission, "unexpected interaction state")
			require.NoError(t, h.answerPendingInteraction(api, ref, &result, &answered, session.Status.State, c))
		case events.Running, events.Unknown:
		default:
			t.Fatalf("unexpected initial session state %q", session.Status.State)
		}
	}
	for !finished {
		event, err := observer.Next()
		require.NoError(t, err)
		require.Equal(t, ref.SessionID, event.SessionID)
		result.StreamKinds = append(result.StreamKinds, event.Type())
		if h.diagnostic != nil {
			h.diagnostic.report.Boundaries["mass_relay_sse"] = "observed"
		}
		if event.Persistent() {
			persistent = append(persistent, event)
		}
		if request, ok := event.Payload().(events.InteractionRequested); ok {
			require.True(t, c.expectPermission, "unexpected interaction")
			if answered {
				require.NotNil(t, result.Permission)
				require.Equal(t, result.Permission.Interaction, string(request.Interaction))
			} else {
				require.NoError(t, h.answerPermission(api, ref, &result, &answered, request, c))
			}
		}
		if tool, ok := event.Payload().(events.ToolResult); ok && answered {
			for _, block := range tool.Content {
				approvedToolOutput.WriteString(events.TextOf(block))
			}
			if tool.Status == "completed" && (provider != "codex" || tool.ExitCode != nil && *tool.ExitCode == 0) {
				approvedToolCompleted = true
			}
		}
		if closed, ok := event.Payload().(events.InteractionClosed); ok && answered {
			require.Equal(t, result.Permission.Interaction, string(closed.Interaction))
			result.Permission.Closed = true
		}
		if state, ok := event.Payload().(events.StateUpdate); ok && state.State != nil {
			if answered && result.Permission != nil && result.Permission.StateAfterAnswer == "" {
				result.Permission.StateAfterAnswer = *state.State
			}
			if *state.State == events.Running {
				running = true
			}
			require.NotEqual(t, events.SessionState("failed"), *state.State)
			if running && *state.State == events.Idle {
				break
			}
		}
	}
	require.Equal(t, c.expectPermission, answered, "permission flow must be answered")
	require.NoError(t, api.GetSession(h.ctx, ref, &session))
	require.Equal(t, events.Idle, session.Status.State)
	if h.diagnostic != nil {
		h.diagnostic.report.Boundaries["native_completion"] = "not_directly_observed"
	}
	var history store.HistoryPage
	if h.diagnostic != nil {
		h.diagnostic.phase("history-commit")
	}
	require.NoError(t, api.History(h.ctx, ref, store.HistoryQuery{}, &history))
	require.Empty(t, history.Next)
	require.Equal(t, persistent, history.Events, "public history must equal durable SSE events")
	if h.diagnostic != nil {
		h.diagnostic.report.Boundaries["history_commit"] = "history_equals_relay"
	}
	result.State = session.Status.State
	user, assistant := false, false
	for _, event := range history.Events {
		fact := factEvent{Kind: event.Type()}
		switch payload := event.Payload().(type) {
		case events.UserMessage:
			for _, block := range payload.Content {
				fact.Text += events.TextOf(block)
			}
			require.Equal(t, c.prompt, fact.Text)
			user = true
		case events.ToolCall:
			fact.Text = commandText(payload.RawInput)
			fact.ToolCallID = payload.ID
		case events.ToolResult:
			fact.ToolCallID = payload.ID
			fact.Status = payload.Status
			fact.ExitCode = payload.ExitCode
			for _, block := range payload.Content {
				fact.Text += events.TextOf(block)
			}
		case events.AgentMessage:
			for _, block := range payload.Content {
				fact.Text += events.TextOf(block)
			}
			require.NotEmpty(t, fact.Text)
			assistant = true
		}
		result.Events = append(result.Events, fact)
	}
	if provider != "claude" {
		require.True(t, user)
	}
	require.True(t, assistant)
	if result.Permission != nil {
		require.True(t, result.Permission.Closed, "permission interaction must close")
		require.True(t, approvedToolCompleted, "permission answer must lead to a successful tool result")
		if c.expectedToolOutput != "" {
			require.Contains(t, approvedToolOutput.String(), c.expectedToolOutput)
		}
		result.Permission.Completed = result.State == events.Idle && result.Permission.StateAfterAnswer != "" && approvedToolCompleted
		require.True(t, result.Permission.Completed)
	}
	require.NoError(t, observer.Close())
	h.phase = "mass-stop-runtime"
	if h.diagnostic != nil {
		h.diagnostic.phase(h.phase)
	}
	require.NoError(t, api.StopRuntime(h.ctx, "recording", &rt))
	runtimeStopped = true
	if h.diagnostic != nil {
		h.diagnostic.report.SDKCleanup = "stop_runtime_completed"
	}
	require.NoError(t, api.GetRuntime(h.ctx, "recording", &rt))
	require.Equal(t, "stopped", rt.Status.State)
	stop()
	dialer := net.Dialer{Timeout: time.Second}
	conn, err := dialer.DialContext(h.ctx, "tcp", strings.TrimPrefix(ready.URL, "http://"))
	if conn != nil {
		conn.Close()
	}
	require.Error(t, err, "daemon listener must be released")
	if h.diagnostic != nil {
		closed := err != nil
		h.diagnostic.report.ListenerClosed = &closed
	}
	return result
}

func allowedOption(options []events.PermissionOption) (events.PermissionOption, bool) {
	for _, kind := range []string{"allow_once", "allow_always"} {
		for _, option := range options {
			if string(option.Kind) == kind {
				return option, true
			}
		}
	}
	return events.PermissionOption{}, false
}

func (h *harness) answerPendingInteraction(api *client.Client, ref resource.SessionRef, result *facts, answered *bool, state events.SessionState, c flowCase) error {
	h.t.Helper()
	var history store.HistoryPage
	if err := api.History(h.ctx, ref, store.HistoryQuery{}, &history); err != nil {
		return err
	}
	for _, event := range slices.Backward(history.Events) {
		request, ok := event.Payload().(events.InteractionRequested)
		if ok {
			return h.answerPermissionWithState(api, ref, result, answered, request, c, state)
		}
	}
	return errors.New("session requires an action but no recorded interaction is present")
}

func (h *harness) answerPermission(api *client.Client, ref resource.SessionRef, result *facts, answered *bool, request events.InteractionRequested, c flowCase) error {
	h.t.Helper()
	var session resource.Session
	if err := api.GetSession(h.ctx, ref, &session); err != nil {
		return err
	}
	return h.answerPermissionWithState(api, ref, result, answered, request, c, session.Status.State)
}

func (h *harness) answerPermissionWithState(api *client.Client, ref resource.SessionRef, result *facts, answered *bool, request events.InteractionRequested, c flowCase, state events.SessionState) error {
	h.t.Helper()
	require.Equal(h.t, events.InteractionPermission, request.Request.Kind)
	require.NotEmpty(h.t, request.Request.Options)
	if len(request.Request.Content) == 0 {
		var history store.HistoryPage
		if err := api.History(h.ctx, ref, store.HistoryQuery{}, &history); err != nil {
			return err
		}
		visible := false
		for _, event := range history.Events {
			if tool, ok := event.Payload().(events.ToolCall); ok && tool.ID == request.Request.ToolCallID {
				command := commandText(tool.RawInput)
				require.NotEmpty(h.t, command, "linked native tool input must be visible")
				if c.provider == "" || c.provider == "codex" {
					require.Contains(h.t, command, "printf mass-approved > permission-proof.txt")
				}
				visible = true
			}
		}
		require.True(h.t, visible, "permission request must bind a visible public tool call")
	}
	if c.expectedPermission != "" {
		require.Equal(h.t, c.expectedPermission, request.Request.Title)
	}
	require.Equal(h.t, events.RequiresAction, state)
	option, ok := allowedOption(request.Request.Options)
	require.True(h.t, ok, "real agent must offer an allow option")
	var answer runtime.InteractionReceipt
	if err := api.Answer(h.ctx, ref, string(request.Interaction), map[string]any{"outcome": "selected", "optionId": option.OptionID}, &answer); err != nil {
		return err
	}
	require.Equal(h.t, string(request.Interaction), answer.Interaction)
	require.Equal(h.t, runtime.Yes(), answer.Evidence.Sent)
	require.Nil(h.t, answer.Evidence.NativeAccepted)
	result.Permission = &permissionFacts{
		Interaction:       string(request.Interaction),
		Request:           request.Request,
		StateBeforeAnswer: state,
		Answer: permissionAnswer{
			OptionID:       option.OptionID,
			Sent:           true,
			NativeAccepted: answer.Evidence.NativeAccepted,
		},
	}
	*answered = true
	return nil
}

func commandText(raw any) string {
	value, _ := raw.(map[string]any)
	command, _ := value["command"].(string)
	return command
}

func (h *harness) replay(material string, expected facts, c flowCase) string {
	var plan capturePlan
	readJSON(h.t, filepath.Join(material, "plan.json"), &plan)
	if c.provider != "claude" && plan.Model != "" {
		c.sessionConfig = &runtime.InitialSessionConfig{Model: plan.Model}
	}
	c.budget = 60 * time.Second
	session := filepath.Join(h.t.TempDir(), "session")
	c.evidence = ""
	if h.diagnostic != nil {
		scratch, err := os.MkdirTemp(h.diagnostic.report.RecordingRoot, "replay-")
		require.NoError(h.t, err)
		session = filepath.Join(scratch, "session")
		c.evidence = scratch
	}
	expectedProtocol := c.project
	if c.provider == "claude" {
		expectedProtocol = ""
	}
	prepared := h.prepare(session, "--recording", material, "mock", expectedProtocol)
	require.FileExists(h.t, prepared.Connections[0].Launcher)
	actual := h.flow(prepared, prepared.Connections[0].Launcher, c)
	if c.ignoreStreamKinds {
		expected.StreamKinds = nil
		actual.StreamKinds = nil
	} else if len(expected.StreamKinds) == 0 {
		actual.StreamKinds = nil
	}
	expectedForCompare := expected
	if expected.Permission != nil {
		permission := *expected.Permission
		expectedForCompare.Permission = &permission
		require.NotEmpty(h.t, actual.Permission)
		require.NotEmpty(h.t, actual.Permission.Interaction)
		permission.Interaction = actual.Permission.Interaction
	}
	require.Equal(h.t, expectedForCompare, actual)
	require.NoError(h.t, verifyExecution(session, material))
	return session
}

func verifyExecution(session, material string) error {
	canonicalSession, err := filepath.EvalSymlinks(session)
	if err != nil {
		return err
	}
	canonicalMaterial, err := filepath.EvalSymlinks(material)
	if err != nil {
		return err
	}
	fixture, err := agenttestify.Open(canonicalSession)
	if err != nil {
		return err
	}
	var index struct {
		SchemaVersion int    `json:"schemaVersion"`
		FixtureID     string `json:"fixtureId"`
		Executions    []struct {
			ID               string `json:"id"`
			Command          string `json:"command"`
			Status           string `json:"status"`
			DescriptorSHA256 string `json:"descriptorSHA256"`
		} `json:"executions"`
	}
	if err := readJSONFile(filepath.Join(canonicalSession, "executions", "index.json"), &index); err != nil {
		return err
	}
	// agent-sdk-testify validates its own index on Open; this keeps MASS's
	// reading of the current execution index explicit.
	if index.SchemaVersion != testifyExecutionIndexSchemaVersion || index.FixtureID == "" || index.FixtureID != fixture.FixtureID {
		return fmt.Errorf("execution index fixture identity is incomplete: schemaVersion=%d fixtureId=%q", index.SchemaVersion, index.FixtureID)
	}
	if len(index.Executions) != 1 {
		return fmt.Errorf("expected one execution, got %d", len(index.Executions))
	}
	execution := index.Executions[0]
	if execution.Command != "mock" || execution.Status != "passed" {
		return fmt.Errorf("execution status %q", execution.Status)
	}
	report, err := fixture.ExecutionReport(execution.ID)
	if err != nil {
		return err
	}
	if report.FixtureID != fixture.FixtureID || report.ExecutionID != execution.ID {
		return errors.New("execution report fixture identity is incomplete")
	}
	if report.Status != agenttestify.ExecutionStatusPassed || !report.Complete {
		return fmt.Errorf("execution report status/complete is %q/%t", report.Status, report.Complete)
	}
	descriptorHash, err := fileSHA256(report.ExecutionPath)
	if err != nil {
		return err
	}
	if descriptorHash != report.ExecutionSHA256 || descriptorHash != execution.DescriptorSHA256 {
		return errors.New("execution descriptor hash mismatch")
	}
	if len(report.Connections) != 1 {
		return fmt.Errorf("expected one execution connection, got %d", len(report.Connections))
	}
	connection := report.Connections[0]
	if connection.ID != "main" || connection.Status != agenttestify.ConnectionStatusPassed {
		return fmt.Errorf("execution connection id/status is %q/%q", connection.ID, connection.Status)
	}
	if connection.Transport != agenttestify.MockTransportProcess {
		return fmt.Errorf("execution connection transport = %q, want process", connection.Transport)
	}
	if !connection.FullConsumption {
		return errors.New("receipt is incomplete: full consumption is false")
	}
	if !connection.ClientEOF {
		return errors.New("receipt is incomplete: client EOF is false")
	}
	if connection.NativeLaunched {
		return errors.New("replay launched a native agent")
	}
	if !connection.CleanupComplete {
		return errors.New("receipt is incomplete: cleanup is false")
	}
	if connection.ExitCode == nil || *connection.ExitCode != 0 {
		return fmt.Errorf("receipt exit code is %v", connection.ExitCode)
	}
	if connection.Failure != "" || connection.Error != "" {
		return fmt.Errorf("receipt failure/error is %q/%q", connection.Failure, connection.Error)
	}
	for _, evidence := range []string{
		report.ExecutionPath,
		connection.ReceiptPath,
		connection.ProcessReceiptPath,
		connection.ResultPath,
		report.Material.Path,
	} {
		if err := requireWithin(canonicalSession, evidence); err != nil {
			return err
		}
	}
	if fixture.Source.Kind != "recording" || fixture.Source.Provenance != "consumer" {
		return fmt.Errorf("fixture source kind/provenance is %q/%q", fixture.Source.Kind, fixture.Source.Provenance)
	}
	if fixture.Current == nil || fixture.Current.ID != report.Material.ID ||
		fixture.Current.PlanSHA256 != report.Material.PlanSHA256 ||
		fixture.Current.ManifestSHA256 != report.Material.ManifestSHA256 ||
		fixture.Current.RecordingSHA256 != report.Material.RecordingSHA256 {
		return errors.New("fixture current material differs from the execution")
	}
	planHash, err := fileSHA256(filepath.Join(canonicalMaterial, "plan.json"))
	if err != nil {
		return err
	}
	manifestHash, err := fileSHA256(filepath.Join(canonicalMaterial, "manifest.json"))
	if err != nil {
		return err
	}
	rawHash, err := fileSHA256(filepath.Join(canonicalMaterial, "raw", "raw.jsonl"))
	if err != nil {
		return err
	}
	var materialDocument materialArtifact
	if err := readJSONFile(filepath.Join(canonicalMaterial, "material.json"), &materialDocument); err != nil {
		return err
	}
	if materialDocument.ID != report.Material.ID ||
		materialDocument.Kind != report.Material.Kind ||
		materialDocument.Action != report.Material.Action ||
		materialDocument.Provenance != report.Material.Provenance ||
		materialDocument.PlanSHA256 != planHash ||
		materialDocument.ManifestSHA256 != manifestHash ||
		materialDocument.RecordingSHA256 != rawHash ||
		materialDocument.PlanSHA256 != report.Material.PlanSHA256 ||
		materialDocument.ManifestSHA256 != report.Material.ManifestSHA256 ||
		materialDocument.RecordingSHA256 != report.Material.RecordingSHA256 {
		return errors.New("exported material differs from the execution report")
	}
	if len(materialDocument.Connections) != 1 || materialDocument.Connections[0] != "main" {
		return errors.New("exported material connections are incomplete")
	}
	var plan capturePlan
	if err := readJSONFile(filepath.Join(canonicalMaterial, "plan.json"), &plan); err != nil {
		return err
	}
	if plan.AgentType != fixture.AgentType || plan.DriverProfile != "consumer" {
		return fmt.Errorf("plan agent/driver profile is %q/%q", plan.AgentType, plan.DriverProfile)
	}
	return nil
}

func requireWithin(root, filePath string) error {
	if filePath == "" {
		return errors.New("execution receipt path is missing")
	}
	relative, err := filepath.Rel(root, filePath)
	if err != nil {
		return err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return fmt.Errorf("execution evidence path %q is outside the owned session", filePath)
	}
	return nil
}

func verifyRecordingSession(t *testing.T, session, expectedVersion string) {
	t.Helper()
	canonicalSession, err := filepath.EvalSymlinks(session)
	require.NoError(t, err)
	session = canonicalSession
	fixture, err := agenttestify.Open(session)
	require.NoError(t, err)
	require.Equal(t, "plan", fixture.Source.Kind)
	require.Equal(t, "consumer", fixture.Source.Provenance)
	require.Equal(t, "consumer", fixture.Source.DriverProfile)
	require.Equal(t, agenttestify.ToolSourceExecutable, fixture.Tool.Source)
	require.Equal(t, TestifyVersion, fixture.Tool.Version)
	fixtureHash := hashFile(t, filepath.Join(session, "fixture.json"))
	var index struct {
		SchemaVersion int    `json:"schemaVersion"`
		FixtureID     string `json:"fixtureId"`
		FixtureSHA256 string `json:"fixtureSha256"`
		Executions    []struct {
			ID               string `json:"id"`
			Command          string `json:"command"`
			Status           string `json:"status"`
			DescriptorSHA256 string `json:"descriptorSHA256"`
		} `json:"executions"`
	}
	readJSON(t, filepath.Join(session, "executions", "index.json"), &index)
	require.Equal(t, testifyExecutionIndexSchemaVersion, index.SchemaVersion)
	require.Equal(t, fixture.FixtureID, index.FixtureID)
	require.Equal(t, fixtureHash, index.FixtureSHA256)
	require.Len(t, index.Executions, 1)
	execution := index.Executions[0]
	require.Equal(t, "record", execution.Command)
	require.Equal(t, "passed", execution.Status)
	executionReport, err := fixture.ExecutionReport(execution.ID)
	require.NoError(t, err)
	descriptor := executionReport.ExecutionPath
	require.Equal(t, execution.DescriptorSHA256, hashFile(t, descriptor))
	var desc struct {
		SchemaVersion int    `json:"schemaVersion"`
		ExecutionID   string `json:"executionId"`
		FixtureID     string `json:"fixtureId"`
		Command       string `json:"command"`
		Provenance    string `json:"provenance"`
		Connections   []struct {
			Material        string `json:"material"`
			PlanSHA256      string `json:"planSHA256"`
			ManifestSHA256  string `json:"manifestSHA256"`
			RecordingSHA256 string `json:"recordingSHA256"`
			Receipt         string `json:"receipt"`
		} `json:"connections"`
	}
	readJSON(t, descriptor, &desc)
	require.Equal(t, testifyExecutionIndexSchemaVersion, desc.SchemaVersion)
	require.Equal(t, execution.ID, desc.ExecutionID)
	require.Equal(t, fixture.FixtureID, desc.FixtureID)
	require.Equal(t, "record", desc.Command)
	require.Equal(t, "consumer", desc.Provenance)
	require.Len(t, desc.Connections, 1)
	connection := desc.Connections[0]
	require.NotEmpty(t, connection.Material)
	require.NoError(t, requireWithin(session, connection.Material))
	require.NoError(t, requireWithin(session, connection.Receipt))
	var receipt struct {
		CaptureStatus   string `json:"captureStatus"`
		NativeLaunched  bool   `json:"nativeLaunched"`
		NativeExitCode  int    `json:"nativeExitCode"`
		ClientEOF       bool   `json:"clientEOF"`
		Reaped          bool   `json:"reaped"`
		CleanupComplete bool   `json:"cleanupComplete"`
	}
	readJSON(t, connection.Receipt, &receipt)
	require.Equal(t, "complete", receipt.CaptureStatus)
	require.True(t, receipt.NativeLaunched)
	require.Zero(t, receipt.NativeExitCode)
	require.True(t, receipt.ClientEOF)
	require.True(t, receipt.Reaped)
	require.True(t, receipt.CleanupComplete)
	var report struct {
		Status          string `json:"status"`
		CaptureStatus   string `json:"captureStatus"`
		NativeLaunched  bool   `json:"nativeLaunched"`
		ClientEOF       bool   `json:"clientEOF"`
		CleanupComplete bool   `json:"cleanupComplete"`
		Consumer        bool   `json:"consumer"`
		Provenance      string `json:"provenance"`
	}
	readJSON(t, filepath.Join(connection.Material, "report.json"), &report)
	require.Equal(t, "captured", report.Status)
	require.Equal(t, "complete", report.CaptureStatus)
	require.True(t, report.NativeLaunched)
	require.True(t, report.ClientEOF)
	require.True(t, report.CleanupComplete)
	require.True(t, report.Consumer)
	require.Equal(t, "consumer", report.Provenance)
	var manifest struct {
		CaptureStatus  string            `json:"captureStatus"`
		CredentialScan string            `json:"credentialScan"`
		PlanSHA256     string            `json:"planSHA256"`
		Files          map[string]string `json:"files"`
		SavedFiles     map[string]string `json:"savedFiles"`
		Provenance     string            `json:"provenance"`
		Native         struct {
			Version string `json:"version"`
		} `json:"native"`
	}
	readJSON(t, filepath.Join(connection.Material, "manifest.json"), &manifest)
	require.Equal(t, "complete", manifest.CaptureStatus)
	require.Equal(t, "pass", manifest.CredentialScan)
	require.Equal(t, connection.PlanSHA256, manifest.PlanSHA256)
	require.Equal(t, "consumer", manifest.Provenance)
	require.Equal(t, expectedVersion, manifest.Native.Version)
	require.NotEmpty(t, manifest.SavedFiles)
	rawHash := hashFile(t, filepath.Join(connection.Material, "raw", "raw.jsonl"))
	require.Equal(t, connection.RecordingSHA256, rawHash)
	require.Equal(t, rawHash, manifest.Files["raw.jsonl"])
}

func readJSONFile(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func loadCase(t *testing.T, root, provider, name, expectedProject, expectedPrompt string) (string, caseIndex) {
	t.Helper()
	spec, err := selectedRecordingCase(provider, name)
	require.NoError(t, err)
	require.Equal(t, expectedPrompt, spec.flow.prompt)
	if spec.suite == "" {
		require.Equal(t, expectedProject, spec.flow.project)
	}
	material, index, err := auditRecordingCase(root, spec)
	require.NoError(t, err)
	return material, index
}

func RunCodexBasicReplay(t *testing.T) {
	h := newHarness(t)
	material, index := loadCase(t, h.root, "codex", "basic", project, prompt)
	h.replay(material, index.Facts, flowCase{name: "basic", provider: "codex", prompt: index.Prompt, project: index.Project})
}

func RunCodexPermissionReplay(t *testing.T) {
	h := newHarness(t)
	material, index := loadCase(t, h.root, "codex", "permission", permissionProject, permissionPrompt)
	permission := index.Facts.Permission
	require.NotNil(t, permission)
	require.NotEmpty(t, permission.Interaction)
	require.Equal(t, events.InteractionPermission, permission.Request.Kind)
	require.Equal(t, "Approve command execution", permission.Request.Title)
	require.NotEmpty(t, permission.Request.ToolCallID)
	require.NotEmpty(t, permission.Request.Options)
	require.True(t, permission.Closed)
	require.Equal(t, events.RequiresAction, permission.StateBeforeAnswer)
	option, ok := offeredOption(permission.Request.Options, permission.Answer.OptionID)
	require.True(t, ok, "answer must use an offered option")
	require.Contains(t, []events.PermissionOptionKind{events.PermissionAllowOnce, events.PermissionAllowAlways}, option.Kind)
	require.True(t, permission.Answer.Sent)
	require.Nil(t, permission.Answer.NativeAccepted)
	require.NotEmpty(t, permission.StateAfterAnswer)
	require.NotEqual(t, events.RequiresAction, permission.StateAfterAnswer)
	require.True(t, permission.Completed)
	require.Contains(t, index.Facts.StreamKinds, "session.interaction_requested")
	kinds := eventKinds(index.Facts.Events)
	require.Equal(t, "user.message", kinds[0])
	require.Contains(t, kinds, "agent.tool_call")
	require.Contains(t, kinds, "agent.tool_result")
	require.Equal(t, "agent.message", kinds[len(kinds)-1])
	session := h.replay(material, index.Facts, flowCase{name: "permission", provider: "codex", prompt: index.Prompt, project: index.Project, expectPermission: true, expectedPermission: "Approve command execution"})
	require.NoError(t, verifyExecution(session, material))
	receipt := executionReceiptPath(t, session)
	var value map[string]any
	readJSON(t, receipt, &value)
	delete(value, "fullConsumption")
	writeJSON(t, receipt, value)
	require.ErrorContains(t, verifyExecution(session, material), "fullConsumption")
}

func offeredOption(options []events.PermissionOption, id string) (events.PermissionOption, bool) {
	for _, option := range options {
		if option.OptionID == id {
			return option, true
		}
	}
	return events.PermissionOption{}, false
}

func eventKinds(facts []factEvent) []string {
	kinds := make([]string, 0, len(facts))
	for _, fact := range facts {
		kinds = append(kinds, fact.Kind)
	}
	return kinds
}

func executionReceiptPath(t *testing.T, session string) string {
	t.Helper()
	var index struct {
		Executions []struct {
			ID string `json:"id"`
		} `json:"executions"`
	}
	readJSON(t, filepath.Join(session, "executions", "index.json"), &index)
	require.Len(t, index.Executions, 1)
	fixture, err := agenttestify.Open(session)
	require.NoError(t, err)
	report, err := fixture.ExecutionReport(index.Executions[0].ID)
	require.NoError(t, err)
	require.Len(t, report.Connections, 1)
	return report.Connections[0].ReceiptPath
}

func RunRejectsInvalidMaterial(t *testing.T) {
	h := newHarness(t)
	for _, provider := range []string{"codex", "claude"} {
		for _, name := range []string{"basic", "permission"} {
			for _, scenario := range []string{"missing", "hash-mismatch"} {
				t.Run(provider+"/"+name+"/"+scenario, func(t *testing.T) {
					spec, err := selectedRecordingCase(provider, name)
					require.NoError(t, err)
					source := filepath.Join(h.root, spec.casePath(), "material")
					root := t.TempDir()
					material := filepath.Join(root, "material")
					require.NoError(t, os.CopyFS(material, os.DirFS(source)))
					raw := filepath.Join(material, "raw", "raw.jsonl")
					if scenario == "missing" {
						require.NoError(t, os.Remove(raw))
					} else {
						require.NoError(t, os.WriteFile(raw, []byte("{}\n"), 0o600))
					}
					session := filepath.Join(root, "session")
					cmd := exec.CommandContext(h.ctx, h.tool, "prepare", "--recording", material, "--fixture-dir", session)
					out, err := cmd.CombinedOutput()
					require.Error(t, err)
					require.NotEmpty(t, out)
					if scenario == "hash-mismatch" {
						require.Contains(t, string(out), "hash mismatch")
					}
					_, err = os.Stat(filepath.Join(session, "fixture.json"))
					require.True(t, os.IsNotExist(err), "invalid material must not produce a prepared fixture")
				})
			}
		}
	}
}

func requireNoPersonalPaths(t *testing.T, root string) {
	t.Helper()
	require.NoError(t, auditSensitiveTree(root))
}

func recordingDirectory() (string, error) {
	return os.MkdirTemp("/var/tmp", "mass-record-codex-")
}

func permissionRecordingDirectory() (string, error) {
	return os.MkdirTemp("/var/tmp", "mass-record-codex-permission-")
}

func codexRecordingConfig(t *testing.T) ([]string, string) {
	t.Helper()
	var overrides []string
	if value := os.Getenv("MASS_RECORD_CODEX_CONFIG_JSON"); value != "" {
		require.NoError(t, json.Unmarshal([]byte(value), &overrides))
	}
	var arguments strings.Builder
	for _, override := range overrides {
		require.True(t, strings.HasPrefix(override, "model_provider=") || strings.HasPrefix(override, "model_providers."), "recording overrides only select the native provider; model belongs to SessionSpec.Config")
		arguments.WriteString(" -c " + quote(override))
	}
	return overrides, arguments.String()
}

func RunRecordCodexBasic(t *testing.T) {
	if os.Getenv("MASS_RECORD") != "1" {
		t.Skip("set MASS_RECORD=1 to record a real Codex interaction")
	}
	h := newHarness(t)
	scratch, err := recordingDirectory()
	require.NoError(t, err)
	h.reserveDiagnostic("codex", scratch)
	h.phase = "preflight"
	recordProject := filepath.Join(scratch, "project")
	session := filepath.Join(scratch, "session")
	export := filepath.Join(h.root, "tests", "testdata", "simulation", "recordings", "codex", "basic")
	if output := os.Getenv("MASS_RECORD_OUTPUT"); output != "" {
		require.True(t, filepath.IsAbs(output))
		export = output
	}
	_, err = os.Stat(export)
	require.True(t, os.IsNotExist(err), "existing export must not be overwritten")
	_, err = os.Stat(session)
	require.True(t, os.IsNotExist(err), "retain prior session for audit; move it explicitly before retry")
	identity := h.preflight("codex")
	codex, version := identity.Executable, identity.Version
	require.NoError(t, os.MkdirAll(recordProject, 0o700))
	selectedModel := identity.Model
	plan := filepath.Join(scratch, "plan.json")
	writeJSON(t, plan, map[string]any{"version": 1, "agentType": "codex", "caseId": "mass-codex-basic", "driverProfile": "consumer", "project": recordProject, "cliVersion": version, "model": selectedModel})
	h.phase = "testify-prepare"
	prepared := h.prepare(session, "--plan", plan, "record", recordProject)
	cli := filepath.Join(scratch, "codex")
	require.NoError(t, os.Symlink(codex, cli))
	native := filepath.Join(scratch, "native-codex")
	overrides, configArgs := codexRecordingConfig(t)
	require.NoError(t, os.WriteFile(native, []byte("#!/bin/sh\ncd "+quote(recordProject)+" || exit 1\nexec "+quote(cli)+configArgs+" -c 'project_root_markers=[]' -c 'project_doc_max_bytes=0' \"$@\"\n"), 0o700))
	wrapper := filepath.Join(scratch, "record-codex")
	require.NoError(t, os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+quote(h.tool)+" agent record --fixture-dir "+quote(session)+" --cmd "+quote(native)+" -- \"$@\"\n"), 0o700))
	h.phase = "mass-record-real-session"
	observed := h.flow(prepared, wrapper, flowCase{name: "basic", prompt: prompt, project: recordProject, sessionConfig: &runtime.InitialSessionConfig{Model: selectedModel}, evidence: scratch, budget: 2 * time.Minute})
	writeJSON(t, filepath.Join(scratch, "facts.json"), observed)
	h.phase = "testify-export"
	if h.diagnostic != nil {
		h.diagnostic.report.Boundaries["mass_relay_sse"] = "completed"
		h.diagnostic.report.Boundaries["history_commit"] = "completed"
	}
	stage := filepath.Join(scratch, "export")
	material := filepath.Join(stage, "material")
	require.NoError(t, os.MkdirAll(stage, 0o700))
	h.command(h.tool, "export", "--fixture-dir", session, "--output", material)
	verifyRecordingSession(t, session, version)
	require.NoError(t, auditCodexCapture(material, selectedModel, observed.SessionID))
	h.phase = "testify-strict-replay"
	h.replay(material, observed, flowCase{name: "basic", prompt: prompt, project: recordProject})
	h.replay(material, observed, flowCase{name: "basic", prompt: prompt, project: recordProject})
	if h.diagnostic != nil {
		h.diagnostic.report.Boundaries["testify_export_strict_replay"] = "completed"
	}
	index := caseIndex{Provider: "codex", Case: "basic", Provenance: "consumer", RecordedAt: time.Now().UTC().Format(time.RFC3339), SourceSession: filepath.Join(filepath.Base(scratch), "session"), ExportPath: "tests/testdata/simulation/recordings/codex/basic/material", Hashes: map[string]string{}, Versions: map[string]string{"sdk": RuntimeSDKVersion, "testify": TestifyVersion, "codex": version, "mass": h.command("git", "rev-parse", "HEAD"), "massWorktree": h.command("git", "status", "--porcelain")}, ToolHashes: map[string]string{"massd": hashFile(t, h.daemon), "testify": hashFile(t, h.tool), "codex": hashFile(t, codex)}, CodexPath: filepath.Base(codex), Prompt: prompt, Project: recordProject, Facts: observed}
	exportRelative, err := filepath.Rel(h.root, filepath.Join(export, "material"))
	require.NoError(t, err)
	index.ExportPath = filepath.ToSlash(exportRelative)
	index.NativeConfig = overrides
	for _, name := range []string{"plan.json", "material.json", "manifest.json", "raw/raw.jsonl", "raw/manifest.json", "raw/stderr.raw", "raw/transport.json"} {
		index.Hashes[name] = hashFile(t, filepath.Join(material, name))
	}
	writeJSON(t, filepath.Join(stage, "index.json"), index)
	requireNoPersonalPaths(t, stage)
	require.NoError(t, os.MkdirAll(filepath.Dir(export), 0o755))
	require.NoError(t, os.Rename(stage, export))
	if h.diagnostic != nil {
		h.diagnostic.report.Status = "ready"
	}
	t.Logf("real recording and two strict replays passed; export=%s source=%s hashes=%v", filepath.Base(export), index.SourceSession, index.Hashes)
}

func RunRecordCodexPermission(t *testing.T) {
	if os.Getenv("MASS_RECORD") != "1" {
		t.Skip("set MASS_RECORD=1 to record a real Codex permission interaction")
	}
	h := newHarness(t)
	scratch, err := permissionRecordingDirectory()
	require.NoError(t, err)
	h.reserveDiagnostic("codex", scratch)
	h.phase = "preflight"
	recordProject := filepath.Join(scratch, "project")
	session := filepath.Join(scratch, "session")
	export := filepath.Join(h.root, "tests", "testdata", "simulation", "recordings", "codex", "permission")
	if output := os.Getenv("MASS_RECORD_OUTPUT"); output != "" {
		require.True(t, filepath.IsAbs(output))
		export = output
	}
	_, err = os.Stat(export)
	require.True(t, os.IsNotExist(err), "existing export must not be overwritten")
	_, err = os.Stat(session)
	require.True(t, os.IsNotExist(err), "retain prior session for audit; move it explicitly before retry")
	identity := h.preflight("codex")
	codex, version := identity.Executable, identity.Version
	require.NoError(t, os.MkdirAll(recordProject, 0o700))
	proof := filepath.Join(recordProject, "permission-proof.txt")
	if err := os.Remove(proof); err != nil && !os.IsNotExist(err) {
		require.NoError(t, err)
	}
	selectedModel := identity.Model
	plan := filepath.Join(scratch, "plan.json")
	writeJSON(t, plan, map[string]any{"version": 1, "agentType": "codex", "caseId": "mass-codex-permission", "driverProfile": "consumer", "project": recordProject, "cliVersion": version, "model": selectedModel})
	h.phase = "testify-prepare"
	prepared := h.prepare(session, "--plan", plan, "record", recordProject)
	cli := filepath.Join(scratch, "codex")
	require.NoError(t, os.Symlink(codex, cli))
	native := filepath.Join(scratch, "native-codex")
	overrides, configArgs := codexRecordingConfig(t)
	config := "exec " + quote(cli) + configArgs + " -c 'project_root_markers=[]' -c 'project_doc_max_bytes=0' -c 'approval_policy=\"on-request\"' -c 'sandbox_mode=\"read-only\"' \"$@\"\n"
	require.NoError(t, os.WriteFile(native, []byte("#!/bin/sh\ncd "+quote(recordProject)+" || exit 1\n"+config), 0o700))
	wrapper := filepath.Join(scratch, "record-codex")
	require.NoError(t, os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+quote(h.tool)+" agent record --fixture-dir "+quote(session)+" --cmd "+quote(native)+" -- \"$@\"\n"), 0o700))
	h.phase = "mass-record-real-session"
	observed := h.flow(prepared, wrapper, flowCase{name: "permission", prompt: permissionPrompt, project: recordProject, expectPermission: true, sessionConfig: &runtime.InitialSessionConfig{Model: selectedModel}, evidence: scratch, budget: 2 * time.Minute})
	writeJSON(t, filepath.Join(scratch, "facts.json"), observed)
	h.phase = "testify-export"
	proofData, err := os.ReadFile(proof)
	require.NoError(t, err)
	require.Equal(t, "mass-approved", strings.TrimSpace(string(proofData)), "allowed command must execute")
	stage := filepath.Join(scratch, "export")
	material := filepath.Join(stage, "material")
	require.NoError(t, os.MkdirAll(stage, 0o700))
	h.command(h.tool, "export", "--fixture-dir", session, "--output", material)
	verifyRecordingSession(t, session, version)
	require.NoError(t, auditCodexCapture(material, selectedModel, observed.SessionID))
	h.phase = "testify-strict-replay"
	h.replay(material, observed, flowCase{name: "permission", prompt: permissionPrompt, project: recordProject, expectPermission: true})
	h.replay(material, observed, flowCase{name: "permission", prompt: permissionPrompt, project: recordProject, expectPermission: true})
	if h.diagnostic != nil {
		h.diagnostic.report.Boundaries["testify_export_strict_replay"] = "completed"
	}
	index := caseIndex{Provider: "codex", Case: "permission", Provenance: "consumer", RecordedAt: time.Now().UTC().Format(time.RFC3339), SourceSession: filepath.Join(filepath.Base(scratch), "session"), ExportPath: "tests/testdata/simulation/recordings/codex/permission/material", Hashes: map[string]string{}, Versions: map[string]string{"sdk": RuntimeSDKVersion, "testify": TestifyVersion, "codex": version, "mass": h.command("git", "rev-parse", "HEAD"), "massWorktree": h.command("git", "status", "--porcelain")}, ToolHashes: map[string]string{"massd": hashFile(t, h.daemon), "testify": hashFile(t, h.tool), "codex": hashFile(t, codex)}, CodexPath: filepath.Base(codex), Prompt: permissionPrompt, Project: recordProject, Facts: observed}
	exportRelative, err := filepath.Rel(h.root, filepath.Join(export, "material"))
	require.NoError(t, err)
	index.ExportPath = filepath.ToSlash(exportRelative)
	index.NativeConfig = overrides
	for _, name := range []string{"plan.json", "material.json", "manifest.json", "raw/raw.jsonl", "raw/manifest.json", "raw/stderr.raw", "raw/transport.json"} {
		index.Hashes[name] = hashFile(t, filepath.Join(material, name))
	}
	writeJSON(t, filepath.Join(stage, "index.json"), index)
	requireNoPersonalPaths(t, stage)
	require.NoError(t, os.MkdirAll(filepath.Dir(export), 0o755))
	require.NoError(t, os.Rename(stage, export))
	if h.diagnostic != nil {
		h.diagnostic.report.Status = "ready"
	}
	t.Logf("real permission recording and two strict replays passed; export=%s source=%s hashes=%v permission=%+v", filepath.Base(export), index.SourceSession, index.Hashes, *observed.Permission)
}
