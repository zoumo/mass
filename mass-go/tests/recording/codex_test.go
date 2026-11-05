package recording_test

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
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/internal/testpeer"
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
	model             = "gpt-6.1-sol"
)

type route struct {
	Enabled     bool              `json:"enabled"`
	Executor    string            `json:"executor"`
	Target      string            `json:"target"`
	Provenance  string            `json:"provenance"`
	Workspace   string            `json:"workspace"`
	ProtocolCwd string            `json:"protocolCwd"`
	Env         map[string]string `json:"env"`
	Connections []struct {
		ID       string `json:"id"`
		Launcher string `json:"launcher"`
	} `json:"connections"`
}

type prepared struct {
	SchemaVersion int              `json:"schemaVersion"`
	SessionID     string           `json:"sessionId"`
	Routes        map[string]route `json:"routes"`
}

type factEvent struct {
	Kind string `json:"kind"`
	Text string `json:"text,omitempty"`
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
	Facts             facts             `json:"facts"`
}

type harness struct {
	t                  *testing.T
	ctx                context.Context
	root, tool, daemon string
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
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	t.Cleanup(cancel)
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	dir := t.TempDir()
	h := &harness{t: t, ctx: ctx, root: root, tool: filepath.Join(dir, "testify"), daemon: filepath.Join(dir, "massd")}
	h.command("go", "build", "-o", h.tool, "github.com/zoumo/agent-sdk-testify")
	h.command("go", "build", "-o", h.daemon, "./cmd/massd")
	return h
}

func (h *harness) command(name string, args ...string) string {
	h.t.Helper()
	cmd := exec.CommandContext(h.ctx, name, args...)
	cmd.Dir = h.root
	out, err := cmd.CombinedOutput()
	require.NoError(h.t, err, "%s failed: %s", filepath.Base(name), out)
	return strings.TrimSpace(string(out))
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

func (h *harness) prepare(session, sourceFlag, source, mode, expectedProject string) route {
	h.t.Helper()
	h.command(h.tool, "prepare", sourceFlag, source, "--session-dir", session)
	var desc prepared
	readJSON(h.t, filepath.Join(session, "session.json"), &desc)
	require.Equal(h.t, 3, desc.SchemaVersion)
	require.NotEmpty(h.t, desc.SessionID)
	selected := desc.Routes[mode]
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
	provider := c.provider
	if provider == "" {
		provider = "codex"
	}
	dir := t.TempDir()
	config := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(config, []byte("listen: 127.0.0.1:0\nlocal: true\n"), 0o600))
	log, err := os.Create(filepath.Join(dir, "daemon.log"))
	require.NoError(t, err)
	defer log.Close()
	cmd := exec.CommandContext(h.ctx, h.daemon, "--config", config, "--data", filepath.Join(dir, "data"))
	cmd.Dir = route.Workspace
	cmd.Env = (runtime.ProcessConfig{EnvPolicy: "replace", Env: route.Env}).Environment(provider)
	cmd.Stderr = log
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			t.Fatal("daemon failed to exit")
		}
	}
	defer stop()
	var ready struct {
		URL string `json:"url"`
	}
	require.NoError(t, json.NewDecoder(stdout).Decode(&ready))
	require.NotEmpty(t, ready.URL)
	api := client.New(ready.URL, "")
	defer api.Close()
	env := map[string]any{}
	for key, value := range route.Env {
		env[key] = value
	}
	for key, value := range c.runtimeEnvironment {
		env[key] = value
	}
	var rt resource.Runtime
	require.NoError(t, api.CreateRuntime(h.ctx, resource.Runtime{Metadata: resource.Metadata{Name: "recording"}, Spec: resource.RuntimeSpec{Type: provider, Config: runtime.Config{"executable": executable, "envPolicy": "replace", "env": env}}}, &rt))
	require.NoError(t, api.StartRuntime(h.ctx, "recording", &rt))
	require.Equal(t, "running", rt.Status.State)
	var session resource.Session
	var receipt runtime.InputReceipt
	input := runtime.Input{Content: []events.ContentBlock{events.Text(c.prompt)}}
	if c.initialInput {
		var created resource.SessionCreated
		require.NoError(t, api.CreateSessionWithInput(h.ctx, resource.SessionCreateInput{Metadata: resource.Metadata{Name: c.name, Namespace: "default"}, Spec: resource.SessionSpec{RuntimeRef: "recording", SessionSpec: runtime.SessionSpec{Cwd: c.project}}, InitialInput: &input}, &created))
		session = created.Session
		require.NotNil(t, created.InitialInputReceipt)
		receipt = *created.InitialInputReceipt
	} else {
		require.NoError(t, api.CreateSession(h.ctx, resource.Session{Metadata: resource.Metadata{Name: c.name, Namespace: "default"}, Spec: resource.SessionSpec{RuntimeRef: "recording", SessionSpec: runtime.SessionSpec{Cwd: c.project}}}, &session))
	}
	require.NotEmpty(t, session.Status.SessionID)
	ref := resource.SessionRef{SessionID: session.Status.SessionID}
	var byName resource.Session
	require.NoError(t, api.GetSession(h.ctx, resource.SessionRef{Namespace: "default", Name: c.name}, &byName))
	require.Equal(t, session.Status.SessionID, byName.Status.SessionID)
	observer, err := api.Observe(h.ctx, ref)
	require.NoError(t, err)
	defer observer.Close()
	if !c.initialInput {
		require.NoError(t, api.SendPrompt(h.ctx, ref, input, &receipt))
	}
	require.Equal(t, runtime.Yes(), receipt.Acceptance.Sent)
	if provider == "codex" {
		require.Equal(t, runtime.Yes(), receipt.Acceptance.NativeAccepted)
	} else {
		require.Nil(t, receipt.Acceptance.NativeAccepted)
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
		if event.Persistent() {
			persistent = append(persistent, event)
		}
		if request, ok := event.Payload().(events.InteractionRequested); ok {
			require.True(t, c.expectPermission, "unexpected interaction")
			if answered {
				require.NotNil(t, result.Permission)
				require.Equal(t, result.Permission.Interaction, request.Interaction)
			} else {
				require.NoError(t, h.answerPermission(api, ref, &result, &answered, request, c))
			}
		}
		if tool, ok := event.Payload().(events.ToolResult); ok && answered {
			for _, block := range tool.Content {
				approvedToolOutput.WriteString(block.Text)
			}
			if tool.Status == "completed" && (provider != "codex" || tool.ExitCode != nil && *tool.ExitCode == 0) {
				approvedToolCompleted = true
			}
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
	var history store.HistoryPage
	require.NoError(t, api.History(h.ctx, ref, store.HistoryQuery{}, &history))
	require.Empty(t, history.Next)
	require.Equal(t, persistent, history.Events, "public history must equal durable SSE events")
	result.State = session.Status.State
	user, assistant := false, false
	for _, event := range history.Events {
		fact := factEvent{Kind: event.Type()}
		switch payload := event.Payload().(type) {
		case events.UserMessage:
			for _, block := range payload.Content {
				fact.Text += block.Text
			}
			require.Equal(t, c.prompt, fact.Text)
			user = true
		case events.ToolCall:
			fact.Text = commandText(payload.RawInput)
		case events.ToolResult:
			for _, block := range payload.Content {
				fact.Text += block.Text
			}
		case events.AgentMessage:
			for _, block := range payload.Content {
				fact.Text += block.Text
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
		require.True(t, approvedToolCompleted, "permission answer must lead to a successful tool result")
		if c.expectedToolOutput != "" {
			require.Contains(t, approvedToolOutput.String(), c.expectedToolOutput)
		}
		result.Permission.Completed = result.State == events.Idle && result.Permission.StateAfterAnswer != "" && approvedToolCompleted
		require.True(t, result.Permission.Completed)
	}
	require.NoError(t, observer.Close())
	require.NoError(t, api.StopRuntime(h.ctx, "recording", &rt))
	require.NoError(t, api.GetRuntime(h.ctx, "recording", &rt))
	require.Equal(t, "stopped", rt.Status.State)
	stop()
	dialer := net.Dialer{Timeout: time.Second}
	conn, err := dialer.DialContext(h.ctx, "tcp", strings.TrimPrefix(ready.URL, "http://"))
	if conn != nil {
		conn.Close()
	}
	require.Error(t, err, "daemon listener must be released")
	return result
}

func allowedOption(options []events.PermissionOption) (events.PermissionOption, bool) {
	for _, kind := range []string{"allow_once", "allow_always"} {
		for _, option := range options {
			if option.Kind == kind {
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
	require.Equal(h.t, "permission", request.Request.Kind)
	require.NotEmpty(h.t, request.Request.Options)
	require.NotEmpty(h.t, request.Request.Content, "native tool input must be visible")
	if c.expectedPermission != "" {
		require.Equal(h.t, c.expectedPermission, request.Request.Title)
	}
	require.Equal(h.t, events.RequiresAction, state)
	option, ok := allowedOption(request.Request.Options)
	require.True(h.t, ok, "real agent must offer an allow option")
	var answer runtime.InteractionReceipt
	if err := api.Answer(h.ctx, ref, request.Interaction, map[string]any{"outcome": "selected", "optionId": option.OptionID}, &answer); err != nil {
		return err
	}
	require.Equal(h.t, request.Interaction, answer.Interaction)
	require.Equal(h.t, runtime.Yes(), answer.Evidence.Sent)
	require.Nil(h.t, answer.Evidence.NativeAccepted)
	result.Permission = &permissionFacts{
		Interaction:       request.Interaction,
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
	session := filepath.Join(h.t.TempDir(), "session")
	expectedProtocol := c.project
	if c.provider == "claude" {
		expectedProtocol = ""
	}
	route := h.prepare(session, "--recording", material, "mock", expectedProtocol)
	require.FileExists(h.t, route.Connections[0].Launcher)
	actual := h.flow(route, route.Connections[0].Launcher, c)
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
	var sessionDocument struct {
		SessionID string `json:"sessionId"`
	}
	if err := readJSONFile(filepath.Join(session, "session.json"), &sessionDocument); err != nil {
		return err
	}
	sessionHash, err := fileSHA256(filepath.Join(canonicalSession, "session.json"))
	if err != nil {
		return err
	}
	var index struct {
		SchemaVersion int    `json:"schemaVersion"`
		SessionID     string `json:"sessionId"`
		SessionSHA256 string `json:"sessionSHA256"`
		Executions    []struct {
			ID               string `json:"id"`
			Command          string `json:"command"`
			Status           string `json:"status"`
			DescriptorSHA256 string `json:"descriptorSHA256"`
			Connections      []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"connections"`
		} `json:"executions"`
	}
	if err := readJSONFile(filepath.Join(session, "executions", "index.json"), &index); err != nil {
		return err
	}
	if index.SchemaVersion != 3 || index.SessionID == "" || index.SessionID != sessionDocument.SessionID {
		return errors.New("execution index session identity is incomplete")
	}
	if index.SessionSHA256 != sessionHash {
		return errors.New("execution index session hash mismatch")
	}
	if len(index.Executions) != 1 {
		return fmt.Errorf("expected one execution, got %d", len(index.Executions))
	}
	execution := index.Executions[0]
	if execution.Command != "mock" || execution.Status != "passed" {
		return fmt.Errorf("execution status %q", execution.Status)
	}
	descriptor := filepath.Join(canonicalSession, "executions", execution.ID, "execution.json")
	if err := requireWithin(canonicalSession, descriptor); err != nil {
		return err
	}
	descriptorHash, err := fileSHA256(descriptor)
	if err != nil {
		return err
	}
	if descriptorHash != execution.DescriptorSHA256 {
		return errors.New("execution descriptor hash mismatch")
	}
	var desc struct {
		SchemaVersion int    `json:"schemaVersion"`
		ExecutionID   string `json:"executionId"`
		SessionID     string `json:"sessionId"`
		Command       string `json:"command"`
		Provenance    string `json:"provenance"`
		ArtifactRoot  string `json:"artifactRoot"`
		Connections   []struct {
			ID              string `json:"id"`
			Material        string `json:"material"`
			PlanSHA256      string `json:"planSHA256"`
			ManifestSHA256  string `json:"manifestSHA256"`
			RecordingSHA256 string `json:"recordingSHA256"`
			Receipt         string `json:"receipt"`
			ProcessReceipt  string `json:"processReceipt"`
		} `json:"connections"`
	}
	if err := readJSONFile(descriptor, &desc); err != nil {
		return err
	}
	if desc.SchemaVersion != 3 || desc.ExecutionID != execution.ID || desc.SessionID != index.SessionID {
		return errors.New("execution descriptor identity is incomplete")
	}
	if desc.Command != "mock" || desc.Provenance != "consumer" {
		return fmt.Errorf("execution descriptor command/provenance is %q/%q", desc.Command, desc.Provenance)
	}
	artifactRoot, err := filepath.EvalSymlinks(desc.ArtifactRoot)
	if err != nil {
		return err
	}
	if artifactRoot != filepath.Join(canonicalSession, "executions", execution.ID, "mock") {
		return errors.New("execution artifact root differs from owned session")
	}
	if len(desc.Connections) != 1 {
		return fmt.Errorf("expected one execution connection, got %d", len(desc.Connections))
	}
	if len(execution.Connections) != 1 || execution.Connections[0].ID != "main" || execution.Connections[0].Status != "passed" {
		return errors.New("execution index connections are incomplete")
	}
	connection := desc.Connections[0]
	if connection.ID != "main" {
		return fmt.Errorf("execution connection id = %q, want main", connection.ID)
	}
	planHash, err := fileSHA256(filepath.Join(material, "plan.json"))
	if err != nil {
		return err
	}
	manifestHash, err := fileSHA256(filepath.Join(material, "manifest.json"))
	if err != nil {
		return err
	}
	rawHash, err := fileSHA256(filepath.Join(material, "raw", "raw.jsonl"))
	if err != nil {
		return err
	}
	connectionMaterial, err := filepath.EvalSymlinks(connection.Material)
	if err != nil {
		return err
	}
	if err := requireWithin(canonicalSession, connectionMaterial); err != nil {
		return err
	}
	var materialDocument materialArtifact
	if err := readJSONFile(filepath.Join(canonicalMaterial, "material.json"), &materialDocument); err != nil {
		return err
	}
	if filepath.Base(connectionMaterial) != materialDocument.ID || connection.PlanSHA256 != planHash ||
		connection.ManifestSHA256 != manifestHash || connection.RecordingSHA256 != rawHash {
		return fmt.Errorf("execution connection material identity differs: material=%q id=%q plan=%q/%q manifest=%q/%q raw=%q/%q",
			connectionMaterial, materialDocument.ID,
			connection.PlanSHA256, planHash,
			connection.ManifestSHA256, manifestHash,
			connection.RecordingSHA256, rawHash)
	}
	var plan capturePlan
	if err := readJSONFile(filepath.Join(material, "plan.json"), &plan); err != nil {
		return err
	}
	if err := requireWithin(canonicalSession, connection.Receipt); err != nil {
		return err
	}
	if err := requireWithin(canonicalSession, connection.ProcessReceipt); err != nil {
		return err
	}
	var receipt struct {
		SchemaVersion   int    `json:"schemaVersion"`
		Version         int    `json:"version"`
		Status          string `json:"status"`
		Provider        string `json:"provider"`
		Provenance      string `json:"provenance"`
		FullConsumption bool   `json:"fullConsumption"`
		ClientEOF       bool   `json:"clientEOF"`
		NativeLaunched  bool   `json:"nativeLaunched"`
		CleanupComplete bool   `json:"cleanupComplete"`
		Recording       string `json:"recording"`
		RecordingSHA256 string `json:"recordingSHA256"`
		PlanSHA256      string `json:"planSHA256"`
		Frames          int    `json:"frames"`
		ExitCode        int    `json:"exitCode"`
		Error           string `json:"error"`
	}
	if err := readJSONFile(connection.Receipt, &receipt); err != nil {
		return err
	}
	if receipt.SchemaVersion != 1 || receipt.Version != 1 || receipt.Status != "passed" {
		return fmt.Errorf("receipt version/status is %d/%d/%q", receipt.SchemaVersion, receipt.Version, receipt.Status)
	}
	if receipt.Provider != plan.AgentType || receipt.Provenance != "consumer" {
		return fmt.Errorf("receipt provider/provenance is %q/%q", receipt.Provider, receipt.Provenance)
	}
	if !receipt.FullConsumption {
		return errors.New("receipt is incomplete: full consumption is false")
	}
	if !receipt.ClientEOF {
		return errors.New("receipt is incomplete: client EOF is false")
	}
	if receipt.NativeLaunched {
		return errors.New("replay launched a native agent")
	}
	if !receipt.CleanupComplete {
		return errors.New("receipt is incomplete: cleanup is false")
	}
	if receipt.ExitCode != 0 || receipt.Error != "" {
		return fmt.Errorf("receipt exit/error is %d/%q", receipt.ExitCode, receipt.Error)
	}
	receiptMaterial, err := filepath.EvalSymlinks(receipt.Recording)
	if err != nil {
		return err
	}
	if receipt.PlanSHA256 != planHash || receiptMaterial != connectionMaterial || receipt.Frames <= 0 {
		return errors.New("receipt material identity is incomplete")
	}
	if receipt.RecordingSHA256 != rawHash {
		return errors.New("receipt recording hash mismatch")
	}
	var process struct {
		SchemaVersion int    `json:"schemaVersion"`
		Status        string `json:"status"`
		SessionID     string `json:"sessionId"`
		ExecutionID   string `json:"executionId"`
		ConnectionID  string `json:"connectionId"`
		ExitCode      int    `json:"exitCode"`
		Error         string `json:"error"`
	}
	if err := readJSONFile(connection.ProcessReceipt, &process); err != nil {
		return err
	}
	if process.SchemaVersion != 1 || process.Status != "passed" ||
		process.SessionID != index.SessionID || process.ExecutionID != execution.ID ||
		process.ConnectionID != "main" || process.ExitCode != 0 || process.Error != "" {
		return errors.New("mock process receipt is incomplete")
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
	var index struct {
		Executions []struct {
			ID               string `json:"id"`
			Command          string `json:"command"`
			Status           string `json:"status"`
			DescriptorSHA256 string `json:"descriptorSHA256"`
		} `json:"executions"`
	}
	readJSON(t, filepath.Join(session, "executions", "index.json"), &index)
	require.Len(t, index.Executions, 1)
	execution := index.Executions[0]
	require.Equal(t, "record", execution.Command)
	require.Equal(t, "passed", execution.Status)
	descriptor := filepath.Join(session, "executions", execution.ID, "execution.json")
	require.Equal(t, execution.DescriptorSHA256, hashFile(t, descriptor))
	var desc struct {
		Command     string `json:"command"`
		Connections []struct {
			Material        string `json:"material"`
			PlanSHA256      string `json:"planSHA256"`
			ManifestSHA256  string `json:"manifestSHA256"`
			RecordingSHA256 string `json:"recordingSHA256"`
			Receipt         string `json:"receipt"`
		} `json:"connections"`
	}
	readJSON(t, descriptor, &desc)
	require.Equal(t, "record", desc.Command)
	require.Len(t, desc.Connections, 1)
	connection := desc.Connections[0]
	require.NotEmpty(t, connection.Material)
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
	material, index, err := auditRecordingCase(root, artifactCase{
		provider: provider,
		name:     name,
		flow: flowCase{
			name: name, provider: provider, project: expectedProject, prompt: expectedPrompt,
			initialInput: provider == "claude", ignoreStreamKinds: provider == "claude",
			expectPermission: name == "permission",
		},
	})
	require.NoError(t, err)
	return material, index
}

func TestCodexBasicReplay(t *testing.T) {
	h := newHarness(t)
	material, index := loadCase(t, h.root, "codex", "basic", project, prompt)
	h.replay(material, index.Facts, flowCase{name: "basic", provider: "codex", prompt: index.Prompt, project: index.Project})
}

func TestCodexPermissionReplay(t *testing.T) {
	h := newHarness(t)
	material, index := loadCase(t, h.root, "codex", "permission", permissionProject, permissionPrompt)
	permission := index.Facts.Permission
	require.NotNil(t, permission)
	require.NotEmpty(t, permission.Interaction)
	require.Equal(t, "permission", permission.Request.Kind)
	require.Equal(t, "item/commandExecution/requestApproval", permission.Request.Title)
	require.NotEmpty(t, permission.Request.ToolCallID)
	require.NotEmpty(t, permission.Request.Options)
	require.NotEmpty(t, permission.Request.Content)
	require.Equal(t, events.RequiresAction, permission.StateBeforeAnswer)
	option, ok := offeredOption(permission.Request.Options, permission.Answer.OptionID)
	require.True(t, ok, "answer must use an offered option")
	require.Contains(t, []string{"allow_once", "allow_always"}, option.Kind)
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
	session := h.replay(material, index.Facts, flowCase{name: "permission", provider: "codex", prompt: index.Prompt, project: index.Project, expectPermission: true, expectedPermission: "item/commandExecution/requestApproval"})
	require.NoError(t, verifyExecution(session, material))
	receipt := executionReceiptPath(t, session)
	var value map[string]any
	readJSON(t, receipt, &value)
	delete(value, "fullConsumption")
	writeJSON(t, receipt, value)
	require.ErrorContains(t, verifyExecution(session, material), "receipt is incomplete")
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
	var descriptor struct {
		Connections []struct {
			Receipt string `json:"receipt"`
		} `json:"connections"`
	}
	readJSON(t, filepath.Join(session, "executions", index.Executions[0].ID, "execution.json"), &descriptor)
	require.Len(t, descriptor.Connections, 1)
	return descriptor.Connections[0].Receipt
}

func TestRecordingRejectsInvalidMaterial(t *testing.T) {
	h := newHarness(t)
	for _, provider := range []string{"codex", "claude"} {
		for _, name := range []string{"basic", "permission"} {
			for _, scenario := range []string{"missing", "hash-mismatch"} {
				t.Run(provider+"/"+name+"/"+scenario, func(t *testing.T) {
					source := filepath.Join(h.root, "tests", "testdata", "simulation", "recordings", provider, name, "material")
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
					cmd := exec.CommandContext(h.ctx, h.tool, "prepare", "--recording", material, "--session-dir", session)
					out, err := cmd.CombinedOutput()
					require.Error(t, err)
					require.NotEmpty(t, out)
					if scenario == "hash-mismatch" {
						require.Contains(t, string(out), "hash mismatch")
					}
					_, err = os.Stat(filepath.Join(session, "session.json"))
					require.True(t, os.IsNotExist(err), "invalid material must not produce a prepared session")
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

func TestRecordCodexBasic(t *testing.T) {
	if os.Getenv("MASS_RECORD") != "1" {
		t.Skip("set MASS_RECORD=1 to record a real Codex interaction")
	}
	h := newHarness(t)
	scratch, err := recordingDirectory()
	require.NoError(t, err)
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
	codex := os.Getenv("MASS_RECORD_CODEX")
	err = nil
	if codex == "" {
		codex, err = exec.LookPath("codex")
	}
	require.NoError(t, err)
	version := h.command(codex, "--version")
	require.Contains(t, h.command(codex, "login", "status"), "Logged in")
	require.NoError(t, os.MkdirAll(project, 0o700))
	plan := filepath.Join(scratch, "plan.json")
	writeJSON(t, plan, map[string]any{"version": 1, "agentType": "codex", "caseId": "mass-codex-basic", "driverProfile": "consumer", "project": project, "cliVersion": version, "model": model})
	route := h.prepare(session, "--plan", plan, "record", project)
	cli := filepath.Join(scratch, "codex")
	require.NoError(t, os.Symlink(codex, cli))
	native := filepath.Join(scratch, "native-codex")
	require.NoError(t, os.WriteFile(native, []byte("#!/bin/sh\ncd "+quote(project)+" || exit 1\nexec "+quote(cli)+" -c "+quote("model=\""+model+"\"")+" -c 'project_root_markers=[]' -c 'project_doc_max_bytes=0' \"$@\"\n"), 0o700))
	wrapper := filepath.Join(scratch, "record-codex")
	require.NoError(t, os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+quote(h.tool)+" agent record --session-dir "+quote(session)+" --cmd "+quote(native)+" -- \"$@\"\n"), 0o700))
	observed := h.flow(route, wrapper, flowCase{name: "basic", prompt: prompt, project: project})
	stage := filepath.Join(scratch, "export")
	material := filepath.Join(stage, "material")
	require.NoError(t, os.MkdirAll(stage, 0o700))
	h.command(h.tool, "export", "--session-dir", session, "--output", material)
	h.replay(material, observed, flowCase{name: "basic", prompt: prompt, project: project})
	h.replay(material, observed, flowCase{name: "basic", prompt: prompt, project: project})
	index := caseIndex{Provider: "codex", Case: "basic", Provenance: "consumer", RecordedAt: time.Now().UTC().Format(time.RFC3339), SourceSession: filepath.Join(filepath.Base(scratch), "session"), ExportPath: "tests/testdata/simulation/recordings/codex/basic/material", Hashes: map[string]string{}, Versions: map[string]string{"sdk": testpeer.SDKVersion, "testify": testpeer.TestifyVersion, "codex": version, "mass": h.command("git", "rev-parse", "HEAD"), "massWorktree": h.command("git", "status", "--porcelain")}, ToolHashes: map[string]string{"massd": hashFile(t, h.daemon), "testify": hashFile(t, h.tool), "codex": hashFile(t, codex)}, CodexPath: filepath.Base(codex), Prompt: prompt, Project: project, Facts: observed}
	for _, name := range []string{"plan.json", "material.json", "manifest.json", "raw/raw.jsonl"} {
		index.Hashes[name] = hashFile(t, filepath.Join(material, name))
	}
	writeJSON(t, filepath.Join(stage, "index.json"), index)
	requireNoPersonalPaths(t, stage)
	require.NoError(t, os.MkdirAll(filepath.Dir(export), 0o755))
	require.NoError(t, os.Rename(stage, export))
	t.Logf("real recording and two strict replays passed; export=%s source=%s hashes=%v", filepath.Base(export), index.SourceSession, index.Hashes)
}

func TestRecordCodexPermission(t *testing.T) {
	if os.Getenv("MASS_RECORD") != "1" {
		t.Skip("set MASS_RECORD=1 to record a real Codex permission interaction")
	}
	h := newHarness(t)
	scratch, err := permissionRecordingDirectory()
	require.NoError(t, err)
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
	codex := os.Getenv("MASS_RECORD_CODEX")
	err = nil
	if codex == "" {
		codex, err = exec.LookPath("codex")
	}
	require.NoError(t, err)
	version := h.command(codex, "--version")
	require.Contains(t, h.command(codex, "login", "status"), "Logged in")
	require.NoError(t, os.MkdirAll(permissionProject, 0o700))
	proof := filepath.Join(permissionProject, "permission-proof.txt")
	if err := os.Remove(proof); err != nil && !os.IsNotExist(err) {
		require.NoError(t, err)
	}
	plan := filepath.Join(scratch, "plan.json")
	writeJSON(t, plan, map[string]any{"version": 1, "agentType": "codex", "caseId": "mass-codex-permission", "driverProfile": "consumer", "project": permissionProject, "cliVersion": version, "model": model})
	route := h.prepare(session, "--plan", plan, "record", permissionProject)
	cli := filepath.Join(scratch, "codex")
	require.NoError(t, os.Symlink(codex, cli))
	native := filepath.Join(scratch, "native-codex")
	config := "exec " + quote(cli) + " -c " + quote("model=\""+model+"\"") + " -c 'project_root_markers=[]' -c 'project_doc_max_bytes=0' -c 'approval_policy=\"on-request\"' -c 'sandbox_mode=\"read-only\"' \"$@\"\n"
	require.NoError(t, os.WriteFile(native, []byte("#!/bin/sh\ncd "+quote(permissionProject)+" || exit 1\n"+config), 0o700))
	wrapper := filepath.Join(scratch, "record-codex")
	require.NoError(t, os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+quote(h.tool)+" agent record --session-dir "+quote(session)+" --cmd "+quote(native)+" -- \"$@\"\n"), 0o700))
	observed := h.flow(route, wrapper, flowCase{name: "permission", prompt: permissionPrompt, project: permissionProject, expectPermission: true})
	proofData, err := os.ReadFile(proof)
	require.NoError(t, err)
	require.Equal(t, "mass-approved", strings.TrimSpace(string(proofData)), "allowed command must execute")
	stage := filepath.Join(scratch, "export")
	material := filepath.Join(stage, "material")
	require.NoError(t, os.MkdirAll(stage, 0o700))
	h.command(h.tool, "export", "--session-dir", session, "--output", material)
	h.replay(material, observed, flowCase{name: "permission", prompt: permissionPrompt, project: permissionProject, expectPermission: true})
	h.replay(material, observed, flowCase{name: "permission", prompt: permissionPrompt, project: permissionProject, expectPermission: true})
	index := caseIndex{Provider: "codex", Case: "permission", Provenance: "consumer", RecordedAt: time.Now().UTC().Format(time.RFC3339), SourceSession: filepath.Join(filepath.Base(scratch), "session"), ExportPath: "tests/testdata/simulation/recordings/codex/permission/material", Hashes: map[string]string{}, Versions: map[string]string{"sdk": testpeer.SDKVersion, "testify": testpeer.TestifyVersion, "codex": version, "mass": h.command("git", "rev-parse", "HEAD"), "massWorktree": h.command("git", "status", "--porcelain")}, ToolHashes: map[string]string{"massd": hashFile(t, h.daemon), "testify": hashFile(t, h.tool), "codex": hashFile(t, codex)}, CodexPath: filepath.Base(codex), Prompt: permissionPrompt, Project: permissionProject, Facts: observed}
	for _, name := range []string{"plan.json", "material.json", "manifest.json", "raw/raw.jsonl"} {
		index.Hashes[name] = hashFile(t, filepath.Join(material, name))
	}
	writeJSON(t, filepath.Join(stage, "index.json"), index)
	requireNoPersonalPaths(t, stage)
	require.NoError(t, os.MkdirAll(filepath.Dir(export), 0o755))
	require.NoError(t, os.Rename(stage, export))
	t.Logf("real permission recording and two strict replays passed; export=%s source=%s hashes=%v permission=%+v", filepath.Base(export), index.SourceSession, index.Hashes, *observed.Permission)
}
