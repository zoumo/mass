package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

func ticket08Daemon(t *testing.T) *entrypointSuite {
	t.Helper()
	s := new(entrypointSuite)
	s.SetT(t)
	s.data = t.TempDir()
	config := filepath.Join(s.data, "config.yaml")
	require.NoError(t, os.WriteFile(config, []byte("listen: 127.0.0.1:0\nlocal: true\n"), 0o600))
	c, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	t.Cleanup(cancel)
	s.daemonCtx = c
	s.daemon = exec.CommandContext(c, app, "--config", config, "--data", s.data)
	s.daemon.Stderr = &s.stderr
	out, err := s.daemon.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, s.daemon.Start())
	t.Cleanup(func() {
		if s.daemon.ProcessState == nil {
			s.NoError(s.daemon.Process.Signal(syscall.SIGTERM))
			done := make(chan error, 1)
			go func() { done <- s.daemon.Wait() }()
			select {
			case err := <-done:
				s.NoError(err, s.stderr.String())
			case <-time.After(15 * time.Second):
				_ = s.daemon.Process.Kill()
				<-done
				t.Error("owned daemon did not stop", s.stderr.String())
			}
		}
		checkCtx, checkCancel := context.WithTimeout(context.Background(), time.Second)
		defer checkCancel()
		conn, err := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(checkCtx, "tcp", strings.TrimPrefix(s.url, "http://"))
		if conn != nil {
			_ = conn.Close()
		}
		s.Error(err, "owned listener released")
		s.NoError(os.RemoveAll(s.data))
		_, err = os.Stat(s.data)
		s.True(os.IsNotExist(err))
		t.Log("PASS owned daemon exit, listener and data cleanup readback")
	})
	var ready struct {
		URL string `json:"url"`
	}
	require.NoError(t, json.NewDecoder(out).Decode(&ready))
	s.url = ready.URL
	return s
}

func TestTicket08CodexLoadApprovalContinuation(t *testing.T) {
	ticket08ApprovalContinuation(t, "codex")
}

func TestTicket08ClaudeLoadApprovalContinuation(t *testing.T) {
	ticket08ApprovalContinuation(t, "claude")
}

func ticket08Loaded(t *testing.T, kind, mode string) (*entrypointSuite, resource.SessionRef, string) {
	t.Helper()
	s := ticket08Daemon(t)
	// TestMain scans this owned binary root on failures as well as success.
	bin := filepath.Join(filepath.Dir(claudeMock), "ticket08-load-peer")
	out, err := exec.CommandContext(ctx(s.T()), "go", "build", "-o", bin, "./testdata/ticket08-load-peer").CombinedOutput()
	s.Require().NoError(err, string(out))
	for _, binary := range []string{app, ctl, bin} {
		data, err := os.ReadFile(binary)
		s.Require().NoError(err)
		t.Logf("artifact %s sha256=%x", filepath.Base(binary), sha256.Sum256(data))
	}
	cwd, err := filepath.EvalSymlinks(s.T().TempDir())
	s.Require().NoError(err)
	evidence := filepath.Join(s.T().TempDir(), "peer.jsonl")
	id := "native-thread"
	env := map[string]any{"LOAD_CWD": cwd, "LOAD_EVIDENCE": evidence, "LOAD_PROVIDER": kind, "LOAD_MODE": mode}
	if kind == "claude" {
		id = claudeLoadID
		config := t.TempDir()
		env["CLAUDE_CONFIG_DIR"] = config
		env["LOAD_ID"] = id
		dir := filepath.Join(config, "projects", regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(cwd, "-"))
		s.Require().NoError(os.MkdirAll(dir, 0o700))
		f, err := os.Create(filepath.Join(dir, id+".jsonl"))
		s.Require().NoError(err)
		for _, row := range ticket07Rows() {
			row["sessionId"] = id
			row["cwd"] = cwd
			s.Require().NoError(json.NewEncoder(f).Encode(row))
		}
		s.Require().NoError(f.Close())
	}
	r := resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: kind, Config: runtime.Config{"executable": bin, "envPolicy": "replace", "env": env}}}
	s.call("runtime.create", workflowParams{Runtime: &r}, nil)
	s.call("runtime.start", workflowParams{Name: "agent"}, nil)
	cmd := exec.CommandContext(ctx(s.T()), ctl, "--url", s.url, "-o", "json", "session", "load", "imported", "-n", "demo", "--runtime", "agent", "--cwd", cwd, "--session-id", id)
	out, err = cmd.CombinedOutput()
	s.Require().NoError(err, string(out))
	var accepted resource.SessionLoadAccepted
	s.Require().NoError(json.Unmarshal(out, &accepted))
	ref := resource.SessionRef{SessionID: id}
	s.Require().NotEmpty(accepted.LoadID)
	s.Eventually(func() bool {
		var v resource.Session
		s.call("session.get", workflowParams{Session: ref}, &v)
		return v.Status.HistoryLoad.Phase == "completed" && v.Status.HistoryLoad.DeliveryCompleted
	}, 5*time.Second, time.Millisecond)
	var loaded resource.Session
	s.call("session.get", workflowParams{Session: ref}, &loaded)
	s.Require().True(loaded.Status.HistoryLoad.NativeVerified)
	s.Require().True(loaded.Status.HistoryLoad.NativeResumed)
	s.Require().True(loaded.Status.HistoryLoad.NativeCompleted)
	s.Require().False(loaded.Status.HistoryLoad.CleanupCompleted)
	s.NotEqual(events.RequiresAction, loaded.Status.State)
	if kind == "claude" {
		s.Equal(events.Unknown, loaded.Status.State)
	}
	page := s.history(ref)
	s.Require().Len(page.Events, 2)
	for _, e := range page.Events {
		s.Require().NotNil(e.Replay)
		s.Equal(accepted.LoadID, e.Replay.LoadID)
	}
	return s, ref, evidence
}

func ticket08ApprovalContinuation(t *testing.T, kind string) {
	s, ref, evidence := ticket08Loaded(t, kind, "")
	_, scan := s.observer(ref)
	var receipts []runtime.InputReceipt
	var live []events.SessionEvent
	for _, text := range []string{"first", "second"} {
		var receipt runtime.InputReceipt
		s.call("session.input", workflowParams{Session: ref, Input: runtime.Input{Content: []events.ContentBlock{events.Text(text)}}}, &receipt)
		s.Equal(runtime.Yes(), receipt.Acceptance.Sent)
		if kind == "claude" {
			s.Nil(receipt.Acceptance.NativeAccepted)
		}
		receipts = append(receipts, receipt)
		answered, received := false, false
		for {
			s.Require().True(scan.Scan())
			var e events.SessionEvent
			s.Require().NoError(json.Unmarshal(scan.Bytes(), &e))
			s.Nil(e.Replay)
			if p, ok := e.Payload().(events.SessionError); ok {
				s.Require().Fail("unexpected native/session failure", "%+v", p)
			}
			if _, ok := e.Payload().(events.AgentMessage); ok {
				received = true
				live = append(live, e)
			}
			if kind == "claude" {
				if _, ok := e.Payload().(events.AgentMessage); ok {
					meta, _ := e.Meta["claude"].(map[string]any)
					s.Require().Equal(receipt.Submission, meta["submission"])
					s.Require().Equal("user_message_uuid", meta["attribution"])
				}
			}
			if request, ok := e.Payload().(events.InteractionRequested); ok {
				s.False(answered)
				s.Contains(request.Request.ToolCallID, "tool-live-")
				var answer runtime.InteractionReceipt
				s.call("session.answer", workflowParams{Session: ref, Interaction: string(request.Interaction), Answer: map[string]any{"outcome": "selected", "optionId": "allow-once"}}, &answer)
				s.Equal(runtime.Yes(), answer.Evidence.Sent)
				answered = true
			}
			if state, ok := e.Payload().(events.StateUpdate); ok && state.State != nil && *state.State == events.Idle {
				if kind == "claude" {
					meta, _ := e.Meta["claude"].(map[string]any)
					s.Equal(receipt.Submission, meta["submission"])
					s.Equal("user_message_uuid", meta["attribution"])
				}
				break
			}
		}
		s.True(received, "current assistant content must arrive before settlement")
		s.True(answered, "each current request must be answered on the retained connection")
	}
	s.NotEqual(receipts[0].Submission, receipts[1].Submission)
	page := s.history(ref)
	s.Require().Len(page.Events, 4, "two saved and two live facts, no duplicate history import")
	for i, e := range page.Events[2:] {
		s.Nil(e.Replay)
		s.Equal(live[i].EventID, e.EventID)
		s.Equal(live[i].ItemID, e.ItemID)
	}
	s.call("session.close", workflowParams{Session: ref}, nil)
	s.call("runtime.stop", workflowParams{Name: "agent"}, nil)
	ticket08Released(t, s, ref, kind, evidence, receipts)
}

func TestTicket08ClaudeLoadUnlinkedResultBlocksReadmission(t *testing.T) {
	s, ref, evidence := ticket08Loaded(t, "claude", "unlinked")
	_, scan := s.observer(ref)
	var receipt runtime.InputReceipt
	s.call("session.input", workflowParams{Session: ref, Input: runtime.Input{Content: []events.ContentBlock{events.Text("unlinked after approval")}}}, &receipt)
	answered, unknownMessage := false, false
	var unknown events.SessionEvent
	for {
		s.Require().True(scan.Scan())
		var e events.SessionEvent
		s.Require().NoError(json.Unmarshal(scan.Bytes(), &e))
		s.Nil(e.Replay)
		if request, ok := e.Payload().(events.InteractionRequested); ok {
			s.call("session.answer", workflowParams{Session: ref, Interaction: string(request.Interaction), Answer: map[string]any{"outcome": "selected", "optionId": "allow-once"}}, nil)
			answered = true
		}
		if _, ok := e.Payload().(events.AgentMessage); ok {
			meta, _ := e.Meta["claude"].(map[string]any)
			s.Require().Equal("unknown", meta["attribution"])
			s.NotContains(meta, "submission")
			unknownMessage = true
			unknown = e
		}
		if state, ok := e.Payload().(events.StateUpdate); ok {
			if state.State != nil && (*state.State == events.Running || *state.State == events.RequiresAction) {
				continue
			}
			s.Require().Nil(state.State, "an unlinked result must not settle current work")
			s.Equal("work_attribution_unknown", state.StopReason)
			break
		}
	}
	s.True(answered)
	s.True(unknownMessage)
	var current resource.Session
	s.call("session.get", workflowParams{Session: ref}, &current)
	s.Equal(events.Unknown, current.Status.State)
	err := workflowHTTP(newHTTPClient(t, s.url, ""), ctx(t), "session.input", workflowParams{Session: ref, Input: runtime.Input{Content: []events.ContentBlock{events.Text("must not dispatch")}}}, nil)
	s.Require().Error(err)
	var public *transport.Error
	s.Require().ErrorAs(err, &public)
	s.Equal("conflict", public.Code)
	page := s.history(ref)
	s.Require().Len(page.Events, 3)
	s.Equal(unknown.EventID, page.Events[2].EventID)
	s.Nil(page.Events[2].Replay)
	meta, _ := page.Events[2].Meta["claude"].(map[string]any)
	s.Equal("unknown", meta["attribution"])
	s.NotContains(meta, "submission")
	s.call("session.cancel", workflowParams{Session: ref}, nil)
	for {
		s.Require().True(scan.Scan())
		var e events.SessionEvent
		s.Require().NoError(json.Unmarshal(scan.Bytes(), &e))
		if state, ok := e.Payload().(events.StateUpdate); ok && state.State != nil && *state.State == events.Idle {
			break
		}
	}
	s.call("session.close", workflowParams{Session: ref}, nil)
	s.call("runtime.stop", workflowParams{Name: "agent"}, nil)
	ticket08Released(t, s, ref, "claude", evidence, []runtime.InputReceipt{receipt})
}

func ticket08Released(t *testing.T, s *entrypointSuite, ref resource.SessionRef, kind, evidence string, receipts []runtime.InputReceipt) {
	t.Helper()
	var current resource.Session
	s.call("session.get", workflowParams{Session: ref}, &current)
	require.Equal(t, events.SessionState("offline"), current.Status.State)
	b, err := os.ReadFile(evidence)
	require.NoError(t, err)
	t.Logf("synthetic native lifecycle: %s", b)
	type record struct {
		Event string   `json:"event"`
		PID   int      `json:"pid"`
		UUID  string   `json:"uuid"`
		Turn  string   `json:"turn"`
		Args  []string `json:"args"`
	}
	var records []record
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		var r record
		require.NoError(t, json.Unmarshal([]byte(line), &r))
		records = append(records, r)
	}

	expected := []string{"opened", "resumed"}
	for range receipts {
		expected = append(expected, "input", "answer")
	}
	if kind == "codex" {
		expected = append(expected, "unsubscribed")
	}
	expected = append(expected, "released")
	require.Len(t, records, len(expected))
	for i, r := range records {
		require.Equal(t, expected[i], r.Event)
		require.Equal(t, records[0].PID, r.PID, "all facts belong to the retained resume process")
	}
	for i, r := range receipts {
		input, answer := records[2+i*2], records[3+i*2]
		if kind == "claude" {
			require.Equal(t, r.Submission, input.UUID)
			require.Equal(t, input.UUID, answer.UUID)
		} else {
			require.Equal(t, r.Submission, input.Turn)
			require.Equal(t, input.Turn, answer.Turn)
		}
	}
	if kind == "claude" {
		require.Contains(t, records[0].Args, "--resume="+ref.SessionID)
	}
	require.ErrorIs(t, syscall.Kill(records[0].PID, 0), syscall.ESRCH, "owned native process must be released and reaped")
}
