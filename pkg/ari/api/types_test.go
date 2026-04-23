package api_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	runapi "github.com/zoumo/mass/pkg/agentrun/api"
)

// ── ContentBlock round-trip (ACP SDK type re-export) ─────────────────────────

func TestContentBlock_TextBlock_RoundTrip(t *testing.T) {
	block := runapi.TextBlock("hello world")
	data, err := json.Marshal(block)
	require.NoError(t, err)

	var decoded runapi.ContentBlock
	require.NoError(t, json.Unmarshal(data, &decoded))

	require.NotNil(t, decoded.Text, "should decode as text content")
	assert.Equal(t, "hello world", decoded.Text.Text)
}

func TestContentBlock_ImageBlock_RoundTrip(t *testing.T) {
	block := runapi.ImageBlock("aGVsbG8=", "image/png")
	data, err := json.Marshal(block)
	require.NoError(t, err)

	var decoded runapi.ContentBlock
	require.NoError(t, json.Unmarshal(data, &decoded))

	require.NotNil(t, decoded.Image, "should decode as image content")
	assert.Equal(t, "aGVsbG8=", decoded.Image.Data)
	assert.Equal(t, "image/png", decoded.Image.MimeType)
}

// ── AgentRunPromptParams round-trip ──────────────────────────────────────────

func TestAgentRunPromptParams_RoundTrip(t *testing.T) {
	params := pkgariapi.AgentRunPromptParams{
		Workspace: "ws1",
		Name:      "agent-1",
		Prompt:    []runapi.ContentBlock{runapi.TextBlock("what is 2+2?")},
	}
	data, err := json.Marshal(params)
	require.NoError(t, err)

	var decoded pkgariapi.AgentRunPromptParams
	require.NoError(t, json.Unmarshal(data, &decoded))

	assert.Equal(t, "ws1", decoded.Workspace)
	assert.Equal(t, "agent-1", decoded.Name)
	require.Len(t, decoded.Prompt, 1)
	require.NotNil(t, decoded.Prompt[0].Text)
	assert.Equal(t, "what is 2+2?", decoded.Prompt[0].Text.Text)
}

// ── WorkspaceSendParams round-trip ───────────────────────────────────────────

func TestWorkspaceSendParams_RoundTrip(t *testing.T) {
	params := pkgariapi.WorkspaceSendParams{
		Workspace:  "ws1",
		From:       "claude",
		To:         "codex",
		Message:    []runapi.ContentBlock{runapi.TextBlock("hello codex")},
		NeedsReply: true,
	}
	data, err := json.Marshal(params)
	require.NoError(t, err)

	var decoded pkgariapi.WorkspaceSendParams
	require.NoError(t, json.Unmarshal(data, &decoded))

	assert.Equal(t, "ws1", decoded.Workspace)
	assert.Equal(t, "claude", decoded.From)
	assert.Equal(t, "codex", decoded.To)
	assert.True(t, decoded.NeedsReply)
	require.Len(t, decoded.Message, 1)
	require.NotNil(t, decoded.Message[0].Text)
	assert.Equal(t, "hello codex", decoded.Message[0].Text.Text)
}

// ── Domain type JSON tests ───────────────────────────────────────────────────

func TestAgentRun_ARIView_StripsInternalFields(t *testing.T) {
	ar := pkgariapi.AgentRun{
		Metadata: pkgariapi.ObjectMeta{Name: "agent-1", Workspace: "ws1"},
		Spec:     pkgariapi.AgentRunSpec{Agent: "default"},
		Status: pkgariapi.AgentRunStatus{
			Status:     "idle",
			SocketPath: "/tmp/test.sock",
			StateDir:   "/tmp/statedir",
			PID:        42,
		},
	}

	view := ar.ARIView()
	assert.Empty(t, view.Status.StateDir, "ARIView should strip StateDir")

	// Verify JSON serialization of the view has no stateDir field.
	data, err := json.Marshal(view)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "stateDir")
}

func TestWorkspace_ARIView_StripsHooks(t *testing.T) {
	ws := pkgariapi.Workspace{
		Metadata: pkgariapi.ObjectMeta{Name: "ws1"},
		Spec: pkgariapi.WorkspaceSpec{
			Source: json.RawMessage(`{"type":"emptyDir"}`),
			Hooks:  json.RawMessage(`[{"phase":"post-create","command":"echo hi"}]`),
		},
		Status: pkgariapi.WorkspaceStatus{Phase: pkgariapi.WorkspacePhaseReady},
	}

	view := ws.ARIView()
	assert.Nil(t, view.Spec.Hooks, "ARIView should strip Hooks")

	data, err := json.Marshal(view)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "hooks")
}

func TestObjectMeta_JSON_RoundTrip(t *testing.T) {
	now := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	meta := pkgariapi.ObjectMeta{
		Name:      "test",
		Workspace: "ws1",
		Labels:    map[string]string{"env": "dev"},
		CreatedAt: now,
		UpdatedAt: now,
	}
	data, err := json.Marshal(meta)
	require.NoError(t, err)

	var decoded pkgariapi.ObjectMeta
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, "test", decoded.Name)
	assert.Equal(t, "ws1", decoded.Workspace)
	assert.Equal(t, "dev", decoded.Labels["env"])
	assert.True(t, now.Equal(decoded.CreatedAt))
}

// ── ListOptions ──────────────────────────────────────────────────────────────

func TestListOptions_FunctionalOptions(t *testing.T) {
	opts := pkgariapi.ApplyListOptions(
		pkgariapi.InWorkspace("ws1"),
		pkgariapi.WithState("idle"),
		pkgariapi.WithLabels(map[string]string{"team": "infra"}),
	)
	assert.Equal(t, "ws1", opts.FieldSelector["workspace"])
	assert.Equal(t, "idle", opts.FieldSelector["state"])
	assert.Equal(t, "infra", opts.Labels["team"])
}

func TestListOptions_WithPhase(t *testing.T) {
	opts := pkgariapi.ApplyListOptions(pkgariapi.WithPhase("ready"))
	assert.Equal(t, "ready", opts.FieldSelector["phase"])
}

// ── AgentSpec.IsDisabled ────────────────────────────────────────────────────

func TestAgentSpec_IsDisabled(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }

	tests := []struct {
		name     string
		disabled *bool
		want     bool
	}{
		{"nil means not disabled", nil, false},
		{"true means disabled", boolPtr(true), true},
		{"false means not disabled", boolPtr(false), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := pkgariapi.AgentSpec{Disabled: tt.disabled, Command: "echo"}
			assert.Equal(t, tt.want, spec.IsDisabled())
		})
	}
}

func TestAgentSpec_Disabled_JSON_OmitsNil(t *testing.T) {
	// nil Disabled should be omitted from JSON (omitempty).
	spec := pkgariapi.AgentSpec{Command: "echo"}
	data, err := json.Marshal(spec)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "disabled")

	// Explicit false should appear in JSON.
	boolFalse := false
	spec2 := pkgariapi.AgentSpec{Disabled: &boolFalse, Command: "echo"}
	data2, err := json.Marshal(spec2)
	require.NoError(t, err)
	assert.Contains(t, string(data2), `"disabled":false`)
}

// ── AgentList type ──────────────────────────────────────────────────────────

func TestAgentList_JSON_RoundTrip(t *testing.T) {
	list := pkgariapi.AgentList{
		Items: []pkgariapi.Agent{
			{Metadata: pkgariapi.ObjectMeta{Name: "a1"}, Spec: pkgariapi.AgentSpec{Command: "echo"}},
			{Metadata: pkgariapi.ObjectMeta{Name: "a2"}, Spec: pkgariapi.AgentSpec{Command: "cat"}},
		},
	}
	data, err := json.Marshal(list)
	require.NoError(t, err)

	var decoded pkgariapi.AgentList
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Len(t, decoded.Items, 2)
	assert.Equal(t, "a1", decoded.Items[0].Metadata.Name)
	assert.Equal(t, "a2", decoded.Items[1].Metadata.Name)
}
