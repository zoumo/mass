package recordingtest

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

const (
	claudeProject           = "/private/var/tmp/mass-consumer/claude-basic/project"
	claudePermissionProject = "/private/var/tmp/mass-consumer/claude-permission/project"
	claudePrompt            = "Reply with one short greeting. Do not use tools or inspect files."
	claudePermissionPrompt  = "Use the Bash tool to execute this exact command in the current project: `printf mass-claude-approved`. Do not use any other tool. Do not only describe the command; execute it once and report its output."
)

type claudeIdentity struct {
	path        string
	version     string
	model       string
	authMethod  string
	apiProvider string
}

func claudeIdentityForTest(t *testing.T, h *harness) claudeIdentity {
	t.Helper()
	identity := h.preflight("claude")
	return claudeIdentity{
		path:        identity.Executable,
		version:     identity.Version,
		model:       identity.Model,
		authMethod:  identity.AuthMethod,
		apiProvider: identity.APIProvider,
	}
}

func (h *harness) recordClaude(c flowCase, scratchPrefix, outputEnvironment string) {
	t := h.t
	t.Helper()
	scratch, err := os.MkdirTemp("/var/tmp", scratchPrefix)
	require.NoError(t, err)
	h.reserveDiagnostic("claude", scratch)
	h.phase = "preflight"
	identity := claudeIdentityForTest(t, h)
	c.project = filepath.Join(scratch, "project")
	c.evidence = scratch
	session := filepath.Join(scratch, "session")
	export := filepath.Join(h.root, "tests", "testdata", "simulation", "recordings", "claude", c.name)
	if output := os.Getenv(outputEnvironment); output != "" {
		require.True(t, filepath.IsAbs(output))
		export = output
	}
	_, err = os.Stat(export)
	require.True(t, os.IsNotExist(err), "existing export must not be overwritten")
	_, err = os.Stat(session)
	require.True(t, os.IsNotExist(err), "retain prior session for audit; move it explicitly before retry")
	require.NoError(t, os.MkdirAll(c.project, 0o700))
	plan := filepath.Join(scratch, "plan.json")
	planValue := map[string]any{
		"version":       1,
		"agentType":     "claude",
		"caseId":        "mass-claude-" + c.name,
		"driverProfile": "consumer",
		"project":       c.project,
		"cliVersion":    identity.version,
	}
	planValue["model"] = identity.model
	writeJSON(t, plan, planValue)
	h.phase = "testify-prepare"
	route := h.prepare(session, "--plan", plan, "record", "")
	cli := filepath.Join(scratch, "claude")
	require.NoError(t, os.Symlink(identity.path, cli))
	native := filepath.Join(scratch, "native-claude")
	command := "exec " + quote(cli)
	if c.expectPermission {
		command += " --settings " + quote(`{"permissions":{"ask":["Bash"]}}`)
	}
	command += " \"$@\"\n"
	require.NoError(t, os.WriteFile(native, []byte("#!/bin/sh\ncd "+quote(c.project)+" || exit 1\n"+command), 0o700))
	wrapper := filepath.Join(scratch, "record-claude")
	require.NoError(t, os.WriteFile(wrapper, []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then exec \"$MASS_RECORD_NATIVE\" \"$@\"; fi\nexec \"$MASS_RECORD_TOOL\" agent record --fixture-dir \"$MASS_RECORD_SESSION\" --cmd \"$MASS_RECORD_NATIVE\" -- \"$@\"\n"), 0o700))
	c.provider = "claude"
	c.runtimeEnvironment = map[string]string{
		"MASS_RECORD_TOOL":    h.tool,
		"MASS_RECORD_SESSION": session,
		"MASS_RECORD_NATIVE":  native,
	}
	c.sessionConfig = &runtime.InitialSessionConfig{Model: identity.model}
	h.phase = "mass-record-real-session"
	observed := h.flow(route, wrapper, c)
	h.phase = "testify-export"
	if h.diagnostic != nil {
		h.diagnostic.report.Boundaries["mass_relay_sse"] = "completed"
		h.diagnostic.report.Boundaries["history_commit"] = "completed"
	}
	h.publishClaudeRecording(scratch, session, export, c, observed, identity)
}

func (h *harness) publishClaudeRecording(scratch, session, export string, c flowCase, observed facts, identity claudeIdentity) {
	t := h.t
	t.Helper()
	verifyRecordingSession(t, session, identity.version)
	requireNoPersonalPaths(t, recordedPackageMaterial(t, session))
	stage := filepath.Join(scratch, "export")
	material := filepath.Join(stage, "material")
	require.NoError(t, os.MkdirAll(stage, 0o700))
	h.command(h.tool, "export", "--fixture-dir", session, "--output", material)
	requireNoPersonalPaths(t, material)
	require.NoError(t, auditClaudeCapture(material, identity.model, observed.SessionID, c.project))
	h.phase = "testify-strict-replay"
	replayCase := c
	replayCase.runtimeEnvironment = nil
	replayCase.initialInput = true
	replayCase.ignoreStreamKinds = true
	h.replay(material, observed, replayCase)
	h.replay(material, observed, replayCase)
	if h.diagnostic != nil {
		h.diagnostic.report.Boundaries["testify_export_strict_replay"] = "completed"
	}
	index := caseIndex{
		Provider:          "claude",
		Case:              c.name,
		Provenance:        "consumer",
		RecordedAt:        time.Now().UTC().Format(time.RFC3339),
		SourceSession:     filepath.Join(filepath.Base(scratch), "session"),
		ExportPath:        filepath.ToSlash(filepath.Join("tests", "testdata", "simulation", "recordings", "claude", c.name, "material")),
		Hashes:            map[string]string{},
		Versions:          map[string]string{"sdk": RuntimeSDKVersion, "testify": TestifyVersion, "claude": identity.version, "mass": h.command("git", "rev-parse", "HEAD"), "massWorktree": h.command("git", "status", "--porcelain")},
		ToolHashes:        map[string]string{"massd": hashFile(t, h.daemon), "testify": hashFile(t, h.tool), "claude": hashFile(t, identity.path)},
		ClaudePath:        filepath.Base(identity.path),
		Prompt:            c.prompt,
		Project:           c.project,
		Facts:             observed,
		ClaudeAuthMethod:  identity.authMethod,
		ClaudeAPIProvider: identity.apiProvider,
	}
	if identity.model != "" {
		index.Versions["claudeModel"] = identity.model
	}
	exportRelative, err := filepath.Rel(h.root, filepath.Join(export, "material"))
	require.NoError(t, err)
	index.ExportPath = filepath.ToSlash(exportRelative)
	for _, name := range []string{"plan.json", "material.json", "manifest.json", "raw/raw.jsonl", "raw/manifest.json", "raw/transport.json", "raw/stderr.raw"} {
		index.Hashes[name] = hashFile(t, filepath.Join(material, name))
	}
	writeJSON(t, filepath.Join(stage, "index.json"), index)
	requireNoPersonalPaths(t, stage)
	require.NoError(t, os.MkdirAll(filepath.Dir(export), 0o755))
	require.NoError(t, os.Rename(stage, export))
	if h.diagnostic != nil {
		h.diagnostic.report.Status = "ready"
	}
	t.Logf("real Claude recording and two strict replays passed; export=%s source=%s hashes=%v permission=%+v", export, index.SourceSession, index.Hashes, observed.Permission)
}

func recordedPackageMaterial(t *testing.T, session string) string {
	t.Helper()
	var index struct {
		Executions []struct {
			MaterialID string `json:"materialId"`
		} `json:"executions"`
	}
	readJSON(t, filepath.Join(session, "executions", "index.json"), &index)
	require.Len(t, index.Executions, 1)
	require.NotEmpty(t, index.Executions[0].MaterialID)
	material := filepath.Join(session, "packages", index.Executions[0].MaterialID, "material")
	info, err := os.Stat(material)
	require.NoError(t, err)
	require.True(t, info.IsDir())
	return material
}

func RunClaudeBasicReplay(t *testing.T) {
	h := newHarness(t)
	material, index := loadCase(t, h.root, "claude", "basic", claudeProject, claudePrompt)
	require.Nil(t, index.Facts.Permission)
	require.NotNil(t, index.Facts.Input)
	require.True(t, index.Facts.Input.Sent)
	require.Nil(t, index.Facts.Input.NativeAccepted)
	kinds := eventKinds(index.Facts.Events)
	require.NotEmpty(t, kinds)
	require.Equal(t, "agent.message", kinds[len(kinds)-1])
	requireSubsequence(t, index.Facts.StreamKinds, eventKinds(index.Facts.Events))
	h.replay(material, index.Facts, flowCase{name: "basic", provider: "claude", prompt: index.Prompt, project: index.Project, initialInput: true, legacyClaudeInput: true, ignoreStreamKinds: true})
}

// RunClaudeCapturedUUIDReplay uses a temporary UUID-bearing copy of the
// historical exchange to exercise the fresh-capture route through MASS and Testify.
func RunClaudeCapturedUUIDReplay(t *testing.T) {
	h := newHarness(t)
	source, index := loadCase(t, h.root, "claude", "basic", claudeProject, claudePrompt)
	material := filepath.Join(t.TempDir(), "material")
	require.NoError(t, os.CopyFS(material, os.DirFS(source)))
	addCapturedUUID(t, material)

	h.replay(material, index.Facts, flowCase{
		name: "basic", provider: "claude", prompt: index.Prompt, project: index.Project,
		initialInput: true, ignoreStreamKinds: true,
	})
}

// The committed material remains immutable. This synthetic variation changes
// one user input and one explicit native reference to that input.
func addCapturedUUID(t *testing.T, material string) {
	t.Helper()
	captured := json.RawMessage(`"315f8b4d-081c-4dd6-ae01-db405e0f0d66"`)
	rawPath := filepath.Join(material, "raw", "raw.jsonl")
	data, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	lines := bytes.Split(data, []byte{'\n'})
	input, assistant := false, false
	for i, line := range lines {
		if len(line) == 0 {
			continue
		}
		var frame struct {
			Direction string                     `json:"direction"`
			Wire      map[string]json.RawMessage `json:"wire"`
		}
		require.NoError(t, json.Unmarshal(line, &frame))
		switch {
		case frame.Direction == "c2s" && string(frame.Wire["type"]) == `"user"`:
			require.False(t, input)
			frame.Wire["uuid"] = captured
			input = true
		case input && !assistant && frame.Direction == "s2c" && string(frame.Wire["type"]) == `"assistant"`:
			frame.Wire["user_message_uuid"] = captured
			assistant = true
		default:
			continue
		}
		var envelope map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(line, &envelope))
		envelope["wire"], err = json.Marshal(frame.Wire)
		require.NoError(t, err)
		lines[i], err = json.Marshal(envelope)
		require.NoError(t, err)
	}
	require.True(t, input && assistant)
	require.NoError(t, os.WriteFile(rawPath, bytes.Join(lines, []byte{'\n'}), 0o600))
	rawHash := hashFile(t, rawPath)
	rawManifest := filepath.Join(material, "raw", "manifest.json")
	rewriteJSONFile(t, rawManifest, func(value map[string]any) {
		value["files"].(map[string]any)["raw.jsonl"] = rawHash
	})
	manifest := filepath.Join(material, "manifest.json")
	rewriteJSONFile(t, manifest, func(value map[string]any) {
		files := value["files"].(map[string]any)
		files["raw/raw.jsonl"] = rawHash
		files["raw/manifest.json"] = hashFile(t, rawManifest)
	})
	rewriteJSONFile(t, filepath.Join(material, "material.json"), func(value map[string]any) {
		value["recordingSHA256"] = rawHash
		value["manifestSHA256"] = hashFile(t, manifest)
	})
}

func RunClaudePermissionReplay(t *testing.T) {
	h := newHarness(t)
	material, index := loadCase(t, h.root, "claude", "permission", claudePermissionProject, claudePermissionPrompt)
	permission := index.Facts.Permission
	require.NotNil(t, permission)
	require.NotNil(t, index.Facts.Input)
	require.True(t, index.Facts.Input.Sent)
	require.Nil(t, index.Facts.Input.NativeAccepted)
	require.Equal(t, events.InteractionPermission, permission.Request.Kind)
	require.Equal(t, "Bash", permission.Request.Title)
	require.NotEmpty(t, permission.Request.ToolCallID)
	require.NotEmpty(t, permission.Request.Options)
	require.NotEmpty(t, permission.Request.Content)
	require.Equal(t, events.RequiresAction, permission.StateBeforeAnswer)
	option, ok := offeredOption(permission.Request.Options, permission.Answer.OptionID)
	require.True(t, ok)
	require.True(t, isAllowOnce(option))
	require.True(t, permission.Answer.Sent)
	require.Nil(t, permission.Answer.NativeAccepted)
	require.NotEmpty(t, permission.StateAfterAnswer)
	require.NotEqual(t, events.RequiresAction, permission.StateAfterAnswer)
	require.True(t, permission.Completed)
	require.Contains(t, index.Facts.StreamKinds, "session.interaction_requested")
	kinds := eventKinds(index.Facts.Events)
	require.NotEmpty(t, kinds)
	require.Contains(t, kinds, "agent.tool_call")
	require.Contains(t, kinds, "agent.tool_result")
	require.Equal(t, "agent.message", kinds[len(kinds)-1])
	require.Contains(t, toolCallText(index.Facts.Events), "printf mass-claude-approved")
	require.Contains(t, toolResultText(index.Facts.Events), "mass-claude-approved")
	requireSubsequence(t, index.Facts.StreamKinds, eventKinds(index.Facts.Events))
	session := h.replay(material, index.Facts, flowCase{
		name: "permission", provider: "claude", prompt: index.Prompt, project: index.Project,
		initialInput: true, legacyClaudeInput: true, ignoreStreamKinds: true, expectPermission: true,
		expectedPermission: "Bash", expectedToolOutput: "mass-claude-approved",
		permissionOptions: []events.PermissionOption{
			{OptionID: "allow-once", Name: "Allow once", Kind: events.PermissionAllow, Scope: events.PermissionScopeOnce},
			{OptionID: "reject-once", Name: "Reject once", Kind: events.PermissionReject, Scope: events.PermissionScopeOnce},
			{OptionID: "cancel", Name: "Cancel", Kind: events.PermissionCancel},
		},
	})
	adapterReceipt := filepath.Join(session, claudeReplayReceipt)
	require.NoError(t, os.Remove(adapterReceipt))
	require.ErrorContains(t, verifyExecution(session, material), "claude replay adapter receipt")
	require.NoError(t, os.WriteFile(adapterReceipt, []byte("incomplete\n"), 0o600))
	require.ErrorContains(t, verifyExecution(session, material), "claude replay adapter did not complete")
	require.NoError(t, os.WriteFile(adapterReceipt, []byte("complete\n"), 0o600))
	receipt := executionReceiptPath(t, session)
	var value map[string]any
	readJSON(t, receipt, &value)
	delete(value, "clientEOF")
	writeJSON(t, receipt, value)
	require.ErrorContains(t, verifyExecution(session, material), "clientEOF")
}

func toolCallText(facts []factEvent) string {
	var text strings.Builder
	for _, fact := range facts {
		if fact.Kind == "agent.tool_call" {
			text.WriteString(fact.Text)
		}
	}
	return text.String()
}

func toolResultText(facts []factEvent) string {
	var text strings.Builder
	for _, fact := range facts {
		if fact.Kind == "agent.tool_result" {
			text.WriteString(fact.Text)
		}
	}
	return text.String()
}

func requireSubsequence(t *testing.T, actual, expected []string) {
	t.Helper()
	index := 0
	for _, kind := range actual {
		if strings.HasSuffix(kind, "_chunk") ||
			kind == "session.state_update" ||
			kind == "session.interaction_requested" ||
			kind == "session.interaction_closed" ||
			kind == "session.error" ||
			kind == "session.info_update" ||
			kind == "session.config_option_update" ||
			kind == "session.available_commands_update" ||
			kind == "session.usage_update" {
			continue
		}
		for index < len(expected) && expected[index] != kind {
			index++
		}
		require.Less(t, index, len(expected), "subscription event %q is absent from durable history", kind)
		index++
	}
}

func RunRecordClaudeBasic(t *testing.T) {
	if os.Getenv("MASS_RECORD") != "1" {
		t.Skip("set MASS_RECORD=1 to record a real Claude interaction")
	}
	h := newHarness(t)
	h.recordClaude(flowCase{name: "basic", prompt: claudePrompt, project: claudeProject, initialInput: true, ignoreStreamKinds: true}, "mass-record-claude-", "MASS_RECORD_CLAUDE_BASIC_OUTPUT")
}

func RunRecordClaudePermission(t *testing.T) {
	if os.Getenv("MASS_RECORD") != "1" {
		t.Skip("set MASS_RECORD=1 to record a real Claude permission interaction")
	}
	h := newHarness(t)
	h.recordClaude(flowCase{name: "permission", prompt: claudePermissionPrompt, project: claudePermissionProject, initialInput: true, ignoreStreamKinds: true, expectPermission: true, expectedPermission: "Bash", expectedToolOutput: "mass-claude-approved"}, "mass-record-claude-permission-", "MASS_RECORD_CLAUDE_PERMISSION_OUTPUT")
}
