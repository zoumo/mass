package integration_test

import (
	"encoding/json"
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
	r := resource.Runtime{Metadata: resource.Metadata{Name: "continuous"}, Spec: resource.RuntimeSpec{Type: "claude", Config: runtime.Config{"executable": bin, "envPolicy": "replace", "env": map[string]any{"PEER_EVIDENCE": evidence}}}}
	s.call("runtime.create", workflowParams{Runtime: &r}, nil)
	s.call("runtime.start", workflowParams{Name: "continuous"}, nil)
	work, err := filepath.EvalSymlinks(s.T().TempDir())
	s.Require().NoError(err)
	create := resource.Session{Metadata: resource.Metadata{Namespace: "demo", Name: "continuous"}, Spec: resource.SessionSpec{RuntimeRef: "continuous", SessionSpec: runtime.SessionSpec{Cwd: work}}}
	var opened resource.Session
	s.call("session.create", workflowParams{Create: &create}, &opened)
	return resource.SessionRef{SessionID: opened.Status.SessionID}, evidence
}

func (s *entrypointSuite) TestClaudeContinuousInputSameProcess() {
	ref, evidence := s.openClaudeContinuity()
	unsupported := events.ImageBlock{Data: "AA==", MIMEType: "image/png"}
	err := workflowHTTP(newHTTPClient(s.T(), s.url, ""), ctx(s.T()), "session.input", workflowParams{Session: ref, Input: runtime.Input{Content: []events.ContentBlock{unsupported}}}, nil)
	var public *transport.Error
	s.Require().ErrorAs(err, &public)
	s.Equal("unsupported", public.Code, "rejected input must not consume the first submission")
	// The fixed SDK rejects a text frame over its 4 MiB limit before writing.
	// This exercises actual Submit NotSent through HTTP, not a fake receipt.
	err = workflowHTTP(newHTTPClient(s.T(), s.url, ""), ctx(s.T()), "session.input", workflowParams{Session: ref, Input: runtime.Input{Content: []events.ContentBlock{events.Text(strings.Repeat("x", (4<<20)+1))}}}, nil)
	s.Require().ErrorAs(err, &public)
	s.Equal("conflict", public.Code, "confirmed NotSent is a known rejection, not outcome_unknown")
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
		s.Equal(receipts[i].Submission, e.Meta["claude"].(map[string]any)["submission"])
	}
	data, err := os.ReadFile(evidence)
	s.Require().NoError(err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	s.Require().Len(lines, 2)
	var first, second map[string]any
	s.Require().NoError(json.Unmarshal([]byte(lines[0]), &first))
	s.Require().NoError(json.Unmarshal([]byte(lines[1]), &second))
	s.Equal(first["pid"], second["pid"], "both wire inputs reached the same owned process")
	s.Equal(receipts[1].Submission, second["uuid"])
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
			s.NotEqual("session.interaction_requested", e.Type(), "old interaction cannot become current authority")
			if e.ItemID == "unlinked-late:0" {
				meta := e.Meta["claude"].(map[string]any)
				s.Equal("unknown", meta["attribution"])
				s.NotContains(meta, "submission")
			}
			if text == "hold" && e.ItemID == "message-3:0" {
				var current resource.Session
				s.call("session.get", workflowParams{Session: ref}, &current)
				s.NotEqual(events.Idle, current.Status.State, "old/child results cannot settle current input")
				err := workflowHTTP(newHTTPClient(s.T(), s.url, ""), ctx(s.T()), "session.input", workflowParams{Session: ref, Input: runtime.Input{Content: []events.ContentBlock{events.Text("busy")}}}, nil)
				var public *transport.Error
				s.Require().ErrorAs(err, &public)
				s.Equal("conflict", public.Code)
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

func (s *entrypointSuite) TestClaudeUnlinkedResultBlocksReadmission() {
	s.claudeUnknownResultBlocksReadmission("ambiguous")
}

func (s *entrypointSuite) TestClaudeConflictingResultBlocksReadmission() {
	s.claudeUnknownResultBlocksReadmission("conflicting")
}

func (s *entrypointSuite) claudeUnknownResultBlocksReadmission(prompt string) {
	ref, _ := s.openClaudeContinuity()
	_, scan := s.observer(ref)
	for _, text := range []string{"first", prompt} {
		s.call("session.input", workflowParams{Session: ref, Input: runtime.Input{Content: []events.ContentBlock{events.Text(text)}}}, nil)
		for {
			s.Require().True(scan.Scan())
			var e events.SessionEvent
			s.Require().NoError(json.Unmarshal(scan.Bytes(), &e))
			if state, ok := e.Payload().(events.StateUpdate); ok {
				if text == "first" && state.State != nil && *state.State == events.Idle {
					break
				}
				if text == prompt {
					if state.State != nil && *state.State == events.Running {
						continue
					}
					s.Nil(state.State)
					s.Equal("work_attribution_unknown", state.StopReason)
					break
				}
			}
		}
	}
	err := workflowHTTP(newHTTPClient(s.T(), s.url, ""), ctx(s.T()), "session.input", workflowParams{Session: ref, Input: runtime.Input{Content: []events.ContentBlock{events.Text("must not send")}}}, nil)
	s.Require().Error(err)
	// An interrupt acknowledgment alone is not settlement. The fixture supplies
	// a linked root result so normal close can release this owned process.
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

func (s *entrypointSuite) TestClaudeResumedFirstInputRequiresLinkage() {
	ref, _ := s.openClaudeContinuity()
	s.call("session.close", workflowParams{Session: ref}, nil)
	_, scan := s.observer(ref)
	s.call("session.resume", workflowParams{Session: ref, Input: runtime.Input{Content: []events.ContentBlock{events.Text("resume-unlinked")}}}, nil)
	sawUnknown := false
	for {
		s.Require().True(scan.Scan())
		var e events.SessionEvent
		s.Require().NoError(json.Unmarshal(scan.Bytes(), &e))
		s.NotEqual("session.interaction_requested", e.Type(), "unlinked old request has no answer authority")
		if state, ok := e.Payload().(events.StateUpdate); ok && state.StopReason == "work_attribution_unknown" {
			s.Nil(state.State)
			sawUnknown = true
			break
		}
		if state, ok := e.Payload().(events.StateUpdate); ok && state.State != nil && *state.State == events.Idle {
			break
		}
	}
	s.Require().True(sawUnknown, "unlinked old result must leave resumed work unsettled")
	err := workflowHTTP(newHTTPClient(s.T(), s.url, ""), ctx(s.T()), "session.input", workflowParams{Session: ref, Input: runtime.Input{Content: []events.ContentBlock{events.Text("blocked")}}}, nil)
	s.Require().Error(err)
	s.call("session.cancel", workflowParams{Session: ref}, nil)
	for {
		s.Require().True(scan.Scan())
		var e events.SessionEvent
		s.Require().NoError(json.Unmarshal(scan.Bytes(), &e))
		if state, ok := e.Payload().(events.StateUpdate); ok && state.State != nil && *state.State == events.Idle {
			break
		}
	}
	var receipt runtime.InputReceipt
	s.call("session.input", workflowParams{Session: ref, Input: runtime.Input{Content: []events.ContentBlock{events.Text("resumed-linked")}}}, &receipt)
	for {
		s.Require().True(scan.Scan())
		var e events.SessionEvent
		s.Require().NoError(json.Unmarshal(scan.Bytes(), &e))
		if state, ok := e.Payload().(events.StateUpdate); ok && state.State != nil && *state.State == events.Idle {
			s.Equal(receipt.Submission, e.Meta["claude"].(map[string]any)["submission"])
			s.Equal("user_message_uuid", e.Meta["claude"].(map[string]any)["attribution"])
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
	s.Equal("unknown", meta["attribution"])
	s.NotContains(meta, "submission")
	s.Equal(map[string]any{"session_id": ref.SessionID, "task_id": "child-task", "tool_use_id": "child-tool", "association_id": "task:child-task"}, meta["source"])
	s.Equal(map[string]any{"session_id": ref.SessionID}, meta["parent"])
	s.Equal("live", meta["origin"])
	raw, ok := meta["nativeRaw"].(string)
	s.Require().True(ok, "original received envelope must survive SSE")
	s.JSONEq(`{"type":"assistant","session_id":"44444444-4444-4444-8444-444444444444","parent_tool_use_id":"child-tool","message":{"id":"confirmed-child","role":"assistant","content":[{"type":"text","text":"confirmed child fact","apiBlockIndex":0}]}}`, raw)
	page := s.history(ref)
	s.Require().Len(page.Events, 3)
	s.Equal(child.EventID, page.Events[1].EventID)
	s.Equal(child.Meta, page.Events[1].Meta, "history retains the exact provenance delivered by SSE")
	err := workflowHTTP(newHTTPClient(s.T(), s.url, ""), ctx(s.T()), "session.input", workflowParams{Session: ref, Input: runtime.Input{Content: []events.ContentBlock{events.Text("blocked")}}}, nil)
	s.Require().Error(err)
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
