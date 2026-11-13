package integration_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

func (s *entrypointSuite) openClaudeContinuity() (resource.SessionRef, string) {
	bin := filepath.Join(s.T().TempDir(), "claude-continuity-peer")
	out, err := exec.CommandContext(ctx(s.T()), "go", "build", "-o", bin, "./testdata/claude-continuity").CombinedOutput()
	s.Require().NoError(err, string(out))
	evidence := filepath.Join(s.T().TempDir(), "inputs.jsonl")
	config := s.T().TempDir()
	r := resource.Runtime{Metadata: resource.Metadata{Name: "continuous"}, Spec: resource.RuntimeSpec{Type: "claude", Config: runtime.Config{"executable": bin, "envPolicy": "replace", "env": map[string]any{"PEER_EVIDENCE": evidence, "CLAUDE_CONFIG_DIR": config}}}}
	s.call("runtime.create", workflowParams{Runtime: &r}, nil)
	s.call("runtime.start", workflowParams{Name: "continuous"}, nil)
	work, err := filepath.EvalSymlinks(s.T().TempDir())
	s.Require().NoError(err)
	create := resource.Session{Metadata: resource.Metadata{Namespace: "demo", Name: "continuous"}, Spec: resource.SessionSpec{RuntimeRef: "continuous", SessionSpec: runtime.SessionSpec{Cwd: work}}}
	var opened resource.Session
	s.call("session.create", workflowParams{Create: &create}, &opened)
	prepareClaudeSavedSession(s.T(), config, work, opened.Status.SessionID)
	return resource.SessionRef{SessionID: opened.Status.SessionID}, evidence
}

func (s *entrypointSuite) TestClaudeContinuousInputSameProcess() {
	ref, evidence := s.openClaudeContinuity()
	unsupported := events.ContentBlock{Image: &events.ContentBlockImage{Data: "AA==", MIMEType: "image/png"}}
	err := workflowHTTP(newHTTPClient(s.T(), s.url, ""), ctx(s.T()), "session.input", workflowParams{Session: ref, Input: runtime.Input{Content: []events.ContentBlock{unsupported}}}, nil)
	var public *transport.Error
	s.Require().ErrorAs(err, &public)
	s.Equal("unsupported", public.Code, "rejected input must not consume the first submission")
	// The fixed SDK rejects a text frame over its 4 MiB limit before writing.
	// This exercises actual Submit NotSent through HTTP, not a fake receipt.
	err = workflowHTTP(newHTTPClient(s.T(), s.url, ""), ctx(s.T()), "session.input", workflowParams{Session: ref, Input: runtime.Input{Content: []events.ContentBlock{events.Text(strings.Repeat("x", (4<<20)+1))}}}, nil)
	s.Require().ErrorAs(err, &public)
	s.Equal("resource_exhausted", public.Code, "SDK classifies an oversized frame before dispatch")
	_, scan := s.observer(ref)
	var receipts []runtime.InputReceipt
	for _, text := range []string{"first", "second"} {
		var receipt runtime.InputReceipt
		s.call("session.input", workflowParams{Session: ref, Input: runtime.Input{Content: []events.ContentBlock{events.Text(text)}}}, &receipt)
		s.Equal(runtime.Yes(), receipt.Acceptance.Sent)
		s.Nil(receipt.Acceptance.NativeAccepted)
		s.Nil(receipt.Acceptance.NativeRecorded)
		receipts = append(receipts, receipt)
		for {
			s.Require().True(scan.Scan())
			var e events.SessionEvent
			s.Require().NoError(json.Unmarshal(scan.Bytes(), &e))
			if state, ok := e.Payload().(events.StateUpdate); ok && state.State != nil && *state.State == events.Idle {
				break
			}
		}
	}
	s.NotEqual(receipts[0].Submission, receipts[1].Submission)
	page := s.history(ref)
	s.Require().Len(page.Events, 2)
	for i, e := range page.Events {
		s.Equal(ref.SessionID, e.SessionID)
		s.NotContains(e.Meta["claude"].(map[string]any), "submission")
		s.Equal([]string{"first", "second"}[i], events.TextOf(e.Payload().(events.AgentMessage).Content[0]))
	}
	data, err := os.ReadFile(evidence)
	s.Require().NoError(err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	s.Require().Len(lines, 2)
	var first, second map[string]any
	s.Require().NoError(json.Unmarshal([]byte(lines[0]), &first))
	s.Require().NoError(json.Unmarshal([]byte(lines[1]), &second))
	s.Equal(first["pid"], second["pid"], "both wire inputs reached the same owned process")
	s.Empty(second["uuid"], "SDK receipt identity is not a native message binding")
	s.call("session.close", workflowParams{Session: ref}, nil)
}

func (s *entrypointSuite) TestClaudeLateResultsAndCancelReadmission() {
	ref, _ := s.openClaudeContinuity()
	_, scan := s.observer(ref)
	for _, text := range []string{"first", "second", "hold", "fourth"} {
		var receipt runtime.InputReceipt
		s.call("session.input", workflowParams{Session: ref, Input: runtime.Input{Content: []events.ContentBlock{events.Text(text)}}}, &receipt)
		for {
			s.Require().True(scan.Scan())
			var e events.SessionEvent
			s.Require().NoError(json.Unmarshal(scan.Bytes(), &e))
			// A native live request retains SDK authority even without submission linkage.
			if e.ItemID == "unlinked-late:0" {
				meta := e.Meta["claude"].(map[string]any)
				s.NotContains(meta, "attribution")
				s.NotContains(meta, "submission")
			}
			if text == "hold" && e.ItemID == "message-3:0" {
				var current resource.Session
				s.call("session.get", workflowParams{Session: ref}, &current)
				s.NotEqual(events.Idle, current.Status.State, "old/child results cannot settle current input")

				var cancel runtime.CancelReceipt
				s.call("session.cancel", workflowParams{Session: ref}, &cancel)
				s.Equal("controlAcknowledged", cancel.Result)
			}
			if state, ok := e.Payload().(events.StateUpdate); ok {
				s.Require().NotNil(state.State, "known old result must not make current work unknown")
				if *state.State == events.Idle {
					break
				}
			}
		}
	}
	page := s.history(ref)
	s.Require().Len(page.Events, 5, "unlinked content remains in ordered history")
	s.Equal("unlinked-late:0", page.Events[2].ItemID)
	s.call("session.close", workflowParams{Session: ref}, nil)
}

func (s *entrypointSuite) TestClaudeUnlinkedResultUsesNativeState() {
	s.claudeResultUsesNativeState("ambiguous")
}

func (s *entrypointSuite) TestClaudeConflictingResultUsesNativeState() {
	s.claudeResultUsesNativeState("conflicting")
}

func (s *entrypointSuite) claudeResultUsesNativeState(prompt string) {
	ref, _ := s.openClaudeContinuity()
	_, scan := s.observer(ref)
	s.call("session.input", workflowParams{Session: ref, Input: runtime.Input{Content: []events.ContentBlock{events.Text(prompt)}}}, nil)
	for {
		s.Require().True(scan.Scan())
		var e events.SessionEvent
		s.Require().NoError(json.Unmarshal(scan.Bytes(), &e))
		if e.ItemID == "message-1:0" {
			break
		}
	}
	// A result without an explicit native state does not create a terminal state.
	var current resource.Session
	s.call("session.get", workflowParams{Session: ref}, &current)
	s.Equal(events.Running, current.Status.State)
	s.call("session.cancel", workflowParams{Session: ref}, nil)
	for {
		s.Require().True(scan.Scan())
		var e events.SessionEvent
		s.Require().NoError(json.Unmarshal(scan.Bytes(), &e))
		if state, ok := e.Payload().(events.StateUpdate); ok && state.State != nil && *state.State == events.Idle {
			break
		}
	}
	s.call("session.input", workflowParams{Session: ref, Input: runtime.Input{Content: []events.ContentBlock{events.Text("after-native-idle")}}}, nil)
	for {
		s.Require().True(scan.Scan())
		var e events.SessionEvent
		s.Require().NoError(json.Unmarshal(scan.Bytes(), &e))
		if state, ok := e.Payload().(events.StateUpdate); ok && state.State != nil && *state.State == events.Idle {
			break
		}
	}
	s.call("session.close", workflowParams{Session: ref}, nil)
}

func (s *entrypointSuite) TestClaudeResumedFirstInputUsesSDKAuthority() {
	ref, _ := s.openClaudeContinuity()
	s.call("session.close", workflowParams{Session: ref}, nil)
	_, scan := s.observer(ref)
	s.call("session.resume", workflowParams{Session: ref, Input: runtime.Input{Content: []events.ContentBlock{events.Text("resume-unlinked")}}}, nil)
	for {
		s.Require().True(scan.Scan())
		var e events.SessionEvent
		s.Require().NoError(json.Unmarshal(scan.Bytes(), &e))
		if e.Type() == "session.interaction_requested" {
			break // Observe authority before canceling the active native request.
		}
	}
	// Live request authority is SDK-owned; lack of a caller UUID is not a MASS veto.
	var canceled runtime.CancelReceipt
	s.call("session.cancel", workflowParams{Session: ref}, &canceled)
	s.Require().Equal("controlAcknowledged", canceled.Result, "cancel must reach the resumed active native work")
	s.NotEmpty(canceled.Target)
	for {
		s.Require().True(scan.Scan())
		var e events.SessionEvent
		s.Require().NoError(json.Unmarshal(scan.Bytes(), &e))
		if state, ok := e.Payload().(events.StateUpdate); ok && state.State != nil && *state.State == events.Idle {
			break
		}
	}
	s.call("session.close", workflowParams{Session: ref}, nil)
}

func (s *entrypointSuite) TestClaudeConfirmedChildProvenance() {
	ref, _ := s.openClaudeContinuity()
	_, scan := s.observer(ref)
	s.call("session.input", workflowParams{Session: ref, Input: runtime.Input{Content: []events.ContentBlock{events.Text("child-provenance")}}}, nil)
	var child events.SessionEvent
	for {
		s.Require().True(scan.Scan())
		var e events.SessionEvent
		s.Require().NoError(json.Unmarshal(scan.Bytes(), &e))
		if e.ItemID == "child-tool:confirmed-child:0" {
			child = e
		}
		if e.ItemID == "message-1:0" {
			break
		}
		if state, ok := e.Payload().(events.StateUpdate); ok && state.State != nil {
			s.NotEqual(events.Idle, *state.State, "child result must not settle root input")
		}
	}
	s.Require().Equal("child-tool:confirmed-child:0", child.ItemID)
	meta := child.Meta["claude"].(map[string]any)
	s.NotContains(meta, "attribution")
	s.NotContains(meta, "submission")
	s.Equal(map[string]any{"sessionId": ref.SessionID, "taskId": "child-task", "toolUseId": "child-tool", "associationId": "task:child-task"}, meta["source"])
	s.Equal(map[string]any{"sessionId": ref.SessionID}, meta["parent"])
	s.NotContains(meta, "origin")
	raw, ok := meta["raw"].(map[string]any)
	s.Require().True(ok, "original received envelope must survive SSE")
	encoded, err := json.Marshal(raw)
	s.Require().NoError(err)
	s.JSONEq(fmt.Sprintf(`{"type":"assistant","session_id":%q,"parent_tool_use_id":"child-tool","message":{"id":"confirmed-child","role":"assistant","content":[{"type":"text","text":"confirmed child fact","apiBlockIndex":0}]}}`, ref.SessionID), string(encoded))
	page := s.history(ref)
	s.Require().Len(page.Events, 3)
	s.Equal(child.EventID, page.Events[1].EventID)
	s.Equal(child.Meta, page.Events[1].Meta, "history retains the exact provenance delivered by SSE")
	var current resource.Session
	s.call("session.get", workflowParams{Session: ref}, &current)
	s.Equal(events.Running, current.Status.State)
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
}
