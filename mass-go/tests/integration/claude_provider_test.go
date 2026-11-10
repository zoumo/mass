package integration_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/claude"
)

// Synthetic process evidence verifies the SDK bridge, not real Claude/model behavior.
func TestClaudeProviderZeroInputResumeAndExplicitInitialInput(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "claude-provider-peer")
	out, err := exec.CommandContext(ctx(t), "go", "build", "-o", bin, "./testdata/claude-load-peer").CombinedOutput()
	require.NoError(t, err, "%s", out)
	work, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	config, evidence := t.TempDir(), filepath.Join(t.TempDir(), "peer.jsonl")
	d, err := (claude.Factory{RecoveryDir: t.TempDir()}).New("provider", runtime.Config{
		"executable": bin, "envPolicy": "replace",
		"env": map[string]any{"CLAUDE_CONFIG_DIR": config, "LOAD_ID": "33333333-3333-4333-8333-333333333333", "LOAD_EVIDENCE": evidence},
	})
	require.NoError(t, err)
	stream, err := d.Events()
	require.NoError(t, err)
	read, cancel := context.WithCancel(context.Background())
	observed, drained := make(chan events.SessionEvent, 128), make(chan struct{})
	go func() {
		defer close(drained)
		for {
			e, nextErr := stream.Next(read)
			if nextErr != nil {
				return
			}
			select {
			case observed <- e:
			case <-read.Done():
				return
			}
		}
	}()
	var active string
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		if active != "" {
			if closeErr := d.CloseSession(cleanup, active); closeErr != nil {
				t.Errorf("close synthetic session: %v", closeErr)
			}
		}
		if stopErr := d.Stop(cleanup); stopErr != nil {
			t.Errorf("stop synthetic driver: %v", stopErr)
		}
		cancel()
		select {
		case <-drained:
		case <-cleanup.Done():
			t.Error("event reader did not exit")
		}
	})
	_, err = d.Start(ctx(t))
	require.NoError(t, err)
	address, spec := runtime.Address{Namespace: "provider", SessionName: "resume"}, runtime.SessionSpec{Cwd: work}
	opened, err := d.OpenSession(ctx(t), address, spec)
	require.NoError(t, err)
	active = opened.SessionID
	require.Equal(t, "33333333-3333-4333-8333-333333333333", active)
	require.NoError(t, d.CloseSession(ctx(t), active))
	active = ""

	// The native peer does not write transcripts. Supply an owned saved source for
	// the SDK's identity/project preflight rather than bypassing that check.
	key := regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(work, "-")
	dir := filepath.Join(config, "projects", key)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	row, err := json.Marshal(map[string]any{
		"type": "user", "sessionId": opened.SessionID, "cwd": work, "uuid": "saved-user",
		"message": map[string]any{"role": "user", "content": "saved"},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, opened.SessionID+".jsonl"), append(row, '\n'), 0o600))
	resumed, err := d.ResumeSession(ctx(t), opened.SessionID, address, spec)
	require.NoError(t, err)
	active = resumed.SessionID
	require.Equal(t, opened.SessionID, resumed.SessionID)
	require.Nil(t, resumed.InitialInputReceipt)
	require.NoError(t, d.CloseSession(ctx(t), active))
	active = ""
	data, err := os.ReadFile(evidence)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"event":"input"`, "zero-input resume must not dispatch a hidden prompt")

	resumed, err = d.(runtime.ResumptionDriver).ResumeSessionWithInput(ctx(t), opened.SessionID, address, spec, runtime.Input{Content: []events.ContentBlock{events.Text("continue")}})
	require.NoError(t, err)
	active = resumed.SessionID
	require.Equal(t, opened.SessionID, active)
	require.NotNil(t, resumed.InitialInputReceipt)
	require.EqualValues(t, "applied_best_effort", resumed.InitialInputReceipt.Result)
	require.NotEmpty(t, resumed.InitialInputReceipt.Submission)
	messageSeen := false
	for !messageSeen {
		select {
		case e := <-observed:
			if message, ok := e.Payload().(events.AgentMessage); ok {
				require.Equal(t, "live-1:0", message.MessageID)
				require.Equal(t, message.MessageID, e.ItemID)
				require.Equal(t, "same retained process", events.TextOf(message.Content[0]))
				messageSeen = true
			}
		case <-ctx(t).Done():
			t.Fatal("resumed input produced no agent message")
		}
	}
	data, err = os.ReadFile(evidence)
	require.NoError(t, err)
	inputs := 0
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		var record map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &record))
		if record["event"] == "input" {
			inputs++
			require.Empty(t, record["uuid"])
			require.Equal(t, "next", record["priority"])
			require.Equal(t, true, record["shouldQuery"])
		}
	}
	require.Equal(t, 1, inputs, "only the caller's explicit initial input is dispatched")
}
