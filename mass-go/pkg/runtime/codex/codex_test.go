package codex_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"github.com/zoumo/mass/mass-go/internal/testutil"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/catalog"
)

type runtimeSuite struct {
	suite.Suite
	executable string
}

func TestRuntime(t *testing.T) { suite.Run(t, new(runtimeSuite)) }
func (s *runtimeSuite) SetupSuite() {
	s.executable = filepath.Join(s.T().TempDir(), "mock-codex")
	command := exec.Command("go", "build", "-o", s.executable, "github.com/zoumo/agent-sdk-go/codex/cmd/mock")
	output, err := command.CombinedOutput()
	s.Require().NoError(err, string(output))
}
func (s *runtimeSuite) connect(frames []string) (rt.Runtime, context.Context) {
	s.T().Helper()
	var native []map[string]any
	for i, frame := range frames {
		if frame == "disconnect" {
			native = append(native, map[string]any{"ordinal": i + 1, "direction": "s2c", "channel": "protocol", "epoch": 1, "disconnect": true})
			continue
		}
		direction, wire, _ := strings.Cut(frame, " ")
		native = append(native, map[string]any{"ordinal": i + 1, "direction": direction, "channel": "protocol", "epoch": 1, "wire": json.RawMessage(wire)})
	}
	data, err := json.Marshal(map[string]any{"version": 1, "provenance": "synthetic", "frames": native})
	s.Require().NoError(err)
	scenario := filepath.Join(s.T().TempDir(), "scenario.json")
	s.Require().NoError(os.WriteFile(scenario, data, 0600))
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	s.T().Cleanup(cancel)
	registry := catalog.New()
	_, err = registry.Register(rt.Registration{Name: "test-runtime", Type: "codex", Config: rt.Config{Executable: s.executable, Env: []string{"CODEX_MOCK_SCENARIO=" + scenario}}})
	s.Require().NoError(err)
	conn, err := registry.Get("test-runtime")
	s.Require().NoError(err)
	err = conn.Start(ctx)
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.NoError(conn.Stop()) })
	return conn, ctx
}
func (s *runtimeSuite) open(frames []string) (rt.Runtime, rt.Session, context.Context) {
	conn, ctx := s.connect(frames)
	creationCtx, creationCancel := context.WithCancel(ctx)
	session := testutil.OpenSession(s.T(), conn, creationCtx, rt.SessionOptions{})
	creationCancel()
	ready, err := session.Next(ctx)
	s.Require().NoError(err)
	s.Equal(rt.SessionReady, ready.Kind)
	return conn, session, ctx
}

func (s *runtimeSuite) TestUnknownNativeErrorAndDisconnect() {
	for _, tc := range []struct {
		name, method, params string
		kind                 rt.EventKind
	}{
		{"unknown object", "future/event", `{"threadId":"native-thread","private":"synthetic-sensitive-marker"}`, rt.Unknown},
		{"unknown array", "future/event", `["synthetic-sensitive-marker"]`, rt.Unknown},
		{"native error", "error", `{"threadId":"native-thread","turnId":"native-turn","error":{"message":"synthetic-sensitive-marker","codexErrorInfo":null,"additionalDetails":null},"willRetry":false}`, rt.Failure},
	} {
		s.Run(tc.name, func() {
			frames := append(startup(), turnStart()...)
			frames = append(frames, `s2c {"method":"`+tc.method+`","params":`+tc.params+`}`, "disconnect")
			_, session, ctx := s.open(frames)
			_, err := session.Submit(ctx, "hello")
			s.Require().NoError(err)
			found := false
			for {
				event, err := session.Next(ctx)
				if err != nil {
					s.NotErrorIs(err, context.DeadlineExceeded)
					s.NotContains(err.Error(), "synthetic-sensitive-marker")
					break
				}
				if event.Diagnostic.NativeKind == tc.method || event.Origin.SourceKind == tc.method {
					found = true
					s.Equal(tc.kind, event.Kind)
					if event.Kind == rt.Unknown {
						s.Len(event.Diagnostic.SHA256, 64)
					}
					s.NotContains(testutil.MessageText(event), "synthetic-sensitive-marker")
				}
			}
			s.True(found, "queued native fact must survive disconnect")
		})
	}
}
func startup() []string {
	return []string{
		`c2s {"id":1,"method":"initialize","params":{"clientInfo":{"name":"mass-go","version":"experiment"}}}`,
		`s2c {"id":1,"result":{"userAgent":"synthetic","codexHome":"/synthetic","platformFamily":"unix","platformOs":"linux"}}`,
		`c2s {"method":"initialized"}`,
		`c2s {"id":2,"method":"thread/start","params":{"ephemeral":true}}`,
		`s2c {"id":2,"result":{"thread":{"id":"native-thread","sessionId":"native-session","cliVersion":"synthetic","createdAt":1,"updatedAt":1,"cwd":"/synthetic","ephemeral":true,"modelProvider":"mock","preview":"","projectId":null,"source":"appServer","status":{"type":"idle"},"turns":[]},"approvalPolicy":"never","approvalsReviewer":"user","cwd":"/synthetic","model":"fake","modelProvider":"mock","sandbox":{"type":"readOnly"}}}`,
	}
}
func turnStart() []string {
	return []string{
		`c2s {"id":3,"method":"turn/start","params":{"threadId":"native-thread","input":[{"type":"text","text":"hello","text_elements":[]}]}}`,
		`s2c {"id":3,"result":{"turn":{"id":"native-turn","status":"inProgress","items":[],"error":null}}}`,
	}
}
func (s *runtimeSuite) TestMessagesToolsAndCompletion() {
	frames := append(startup(), turnStart()...)
	frames = append(frames,
		`s2c {"method":"turn/started","params":{"threadId":"native-thread","turn":{"id":"native-turn","status":"inProgress","items":[],"error":null}}}`,
		`s2c {"method":"item/agentMessage/delta","params":{"threadId":"native-thread","turnId":"native-turn","itemId":"message-1","delta":"hello back"}}`,
		`s2c {"method":"item/started","params":{"threadId":"native-thread","turnId":"native-turn","startedAtMs":1,"completedAtMs":2,"item":{"type":"commandExecution","id":"tool-1","command":"echo synthetic","cwd":"/synthetic","processId":null,"status":"inProgress","commandActions":[],"aggregatedOutput":null,"exitCode":null,"durationMs":null}}}`,
		`s2c {"method":"item/completed","params":{"threadId":"native-thread","turnId":"native-turn","startedAtMs":1,"completedAtMs":2,"item":{"type":"commandExecution","id":"tool-1","command":"echo synthetic","cwd":"/synthetic","processId":null,"status":"completed","commandActions":[],"aggregatedOutput":"synthetic output","exitCode":0,"durationMs":1}}}`,
		`s2c {"method":"turn/completed","params":{"threadId":"native-thread","turn":{"id":"native-turn","status":"completed","items":[],"error":null}}}`)
	conn, session, ctx := s.open(frames)
	s.Equal(rt.SessionKey{RuntimeName: "test-runtime", SessionID: "native-thread"}, session.Key())
	submission, err := session.Submit(ctx, "hello")
	s.Require().NoError(err)
	s.Equal(rt.NativeAcknowledged, submission.Evidence)
	s.Equal("native-turn", submission.TurnID)
	var events []rt.Event
	for len(events) < 5 {
		event, err := session.Next(ctx)
		s.Require().NoError(err)
		if event.Kind != rt.Unknown {
			events = append(events, event)
		}
	}
	s.Equal(rt.TurnStarted, events[0].Kind)
	s.Equal("in_progress", events[0].Work.Status)
	s.Equal(rt.MessageDelta, events[1].Kind)
	s.Equal("hello back", testutil.MessageText(events[1]))
	s.Equal("message-1", events[1].Message.MessageRef)
	s.Equal(rt.ToolAnnounced, events[2].Kind)
	s.Equal("commandExecution", events[2].Tool.Name)
	s.Equal("echo synthetic", *events[2].Tool.DisplayLabel)
	s.Equal("synthetic output", *events[3].Tool.Output.Blocks[0].Text)
	s.Equal(rt.TurnCompleted, events[4].Kind)
	s.Equal("completed", events[4].Work.Status)
	s.NoError(conn.Stop())
}
func (s *runtimeSuite) TestInteractionAndInterruption() {
	for _, tc := range []struct {
		name, method, params, answer string
		response                     rt.Response
	}{
		{"approval", "item/commandExecution/requestApproval", `{"threadId":"native-thread","turnId":"native-turn","itemId":"tool-1","startedAtMs":1,"command":"synthetic-only"}`, `{"decision":"accept"}`, rt.Response{Allow: true}},
		{"question", "item/tool/requestUserInput", `{"threadId":"native-thread","turnId":"native-turn","itemId":"question-1","isBlocking":true,"questions":[{"id":"q","header":"Choice","question":"Pick?","isOther":false,"isSecret":false,"options":[]}]}`, `{"answers":{"q":{"answers":["blue"]}}}`, rt.Response{Answers: map[string][]string{"q": {"blue"}}}},
	} {
		s.Run(tc.name, func() {
			frames := append(startup(), turnStart()...)
			frames = append(frames, `s2c {"id":"original-request","method":"`+tc.method+`","params":`+tc.params+`}`, `c2s {"id":"original-request","result":`+tc.answer+`}`,
				`c2s {"id":4,"method":"turn/interrupt","params":{"threadId":"native-thread","turnId":"native-turn"}}`,
				`s2c {"id":4,"result":{}}`,
				`s2c {"method":"turn/completed","params":{"threadId":"native-thread","turn":{"id":"native-turn","status":"interrupted","items":[],"error":null}}}`)
			_, session, ctx := s.open(frames)
			_, err := session.Submit(ctx, "hello")
			s.Require().NoError(err)
			event := s.nextMapped(session, ctx)
			s.Require().Equal(rt.InteractionRequested, event.Kind)
			handle := session.ResponseHandle(event.Interaction.Ref)
			s.Equal(`"original-request"`, handle.ID())
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			s.ErrorIs(handle.Respond(canceled, tc.response), context.Canceled)
			s.NoError(handle.Err())
			s.Require().NoError(handle.Respond(ctx, tc.response))
			s.Error(handle.Respond(ctx, tc.response))
			s.Error(handle.Err())
			s.Require().NoError(session.Cancel(ctx, "native-turn"))
			event = s.nextMapped(session, ctx)
			s.Equal(rt.TurnCompleted, event.Kind)
			s.Equal("interrupted", event.Work.Status)
		})
	}
}
func (s *runtimeSuite) TestCloseWithoutConsuming() {
	original := s.executable
	wrapper, exited := testutil.TrackProcess(s.T(), original)
	s.executable = wrapper
	defer func() { s.executable = original }()
	conn, session, ctx := s.open(append(startup(), turnStart()...))
	_, err := session.Submit(ctx, "hello")
	s.Require().NoError(err)
	s.NoError(conn.Stop())
	s.NoError(session.Close())
	exited()
}

func (s *runtimeSuite) TestPendingHandleExpiresOnClose() {
	frames := append(startup(), turnStart()...)
	frames = append(frames, `s2c {"id":"pending","method":"item/commandExecution/requestApproval","params":{"threadId":"native-thread","turnId":"native-turn","itemId":"tool-1","startedAtMs":1,"command":"synthetic-only"}}`, `c2s {"id":"pending","result":{"decision":"decline"}}`)
	conn, session, ctx := s.open(frames)
	_, err := session.Submit(ctx, "hello")
	s.Require().NoError(err)
	event := s.nextMapped(session, ctx)
	s.Require().Equal(rt.InteractionRequested, event.Kind)
	handle := session.ResponseHandle(event.Interaction.Ref)
	s.NoError(conn.Stop())
	select {
	case <-handle.Done():
	case <-ctx.Done():
		s.FailNow("closed connection left interaction live")
	}
	s.Error(handle.Err())
	s.Error(handle.Respond(ctx, rt.Response{Allow: true}))
}

func (s *runtimeSuite) nextMapped(session rt.Session, ctx context.Context) rt.Event {
	s.T().Helper()
	for {
		event, err := session.Next(ctx)
		s.Require().NoError(err)
		if event.Kind != rt.Unknown {
			return event
		}
	}
}

func (s *runtimeSuite) TestCloseInterruptsPendingCreation() {
	conn, ctx := s.connect(startup()[:4])
	op, err := conn.CreateSession(ctx, rt.SessionOptions{})
	s.Require().NoError(err)
	closed := make(chan error, 1)
	go func() { closed <- conn.Stop() }()
	select {
	case err := <-closed:
		s.NoError(err)
	case <-time.After(time.Second):
		s.FailNow("Stop blocked by creation")
	}
	result, err := conn.GetOperation(op.OperationRef)
	s.Require().NoError(err)
	s.Equal("outcome_unknown", result.Phase)
	s.Nil(result.Key)

}

func (s *runtimeSuite) TestNativeResolutionInvalidatesHandle() {
	frames := append(startup(), turnStart()...)
	frames = append(frames, `s2c {"id":"resolved","method":"item/commandExecution/requestApproval","params":{"threadId":"native-thread","turnId":"native-turn","itemId":"tool-1","startedAtMs":1,"command":"synthetic-only"}}`,
		`c2s {"id":4,"method":"turn/interrupt","params":{"threadId":"native-thread","turnId":"native-turn"}}`,
		`s2c {"id":4,"result":{}}`,
		`s2c {"method":"serverRequest/resolved","params":{"threadId":"native-thread","requestId":"resolved"}}`)
	_, session, ctx := s.open(frames)
	_, err := session.Submit(ctx, "hello")
	s.Require().NoError(err)
	event := s.nextMapped(session, ctx)
	s.Require().Equal(rt.InteractionRequested, event.Kind)
	handle := session.ResponseHandle(event.Interaction.Ref)
	s.Require().NoError(session.Cancel(ctx, "native-turn"))
	select {
	case <-handle.Done():
	case <-ctx.Done():
		s.FailNow("native resolution left handle live")
	}
	s.Error(handle.Err())
	s.Error(handle.Respond(ctx, rt.Response{Allow: true}))
}

func (s *runtimeSuite) TestNativeItemKinds() {
	for _, tc := range []struct {
		name, item, tool string
		kind             rt.EventKind
	}{
		{"MCP", `{"type":"mcpToolCall","id":"item","server":"synthetic","tool":"lookup","arguments":{},"status":"inProgress"}`, "lookup", rt.ToolAnnounced},
		{"dynamic", `{"type":"dynamicToolCall","id":"item","tool":"lookup","arguments":{},"status":"inProgress"}`, "lookup", rt.ToolAnnounced},
		{"reasoning", `{"type":"reasoning","id":"item","summary":[],"content":[]}`, "", rt.ReasoningDelta},
	} {
		s.Run(tc.name, func() {
			frames := append(startup(), turnStart()...)
			frames = append(frames, `s2c {"method":"item/started","params":{"threadId":"native-thread","turnId":"native-turn","startedAtMs":1,"completedAtMs":null,"item":`+tc.item+`}}`)
			_, session, ctx := s.open(frames)
			_, err := session.Submit(ctx, "hello")
			s.Require().NoError(err)
			for {
				event, err := session.Next(ctx)
				s.Require().NoError(err)
				if event.Origin.SourceKind == "item/started" {
					s.Equal(tc.kind, event.Kind)
					if event.Tool != nil {
						s.Equal(tc.tool, event.Tool.Name)
					} else {
						s.Require().NotNil(event.Reasoning)
					}
					break
				}
			}
		})
	}
}

func (s *runtimeSuite) TestSharedRequestsStayWithTheirNativeSession() {
	frames := startup()
	// A second native thread shares the same app-server, with a different tree ID.
	second := strings.ReplaceAll(startup()[4], `"id":2`, `"id":3`)
	second = strings.ReplaceAll(second, "native-thread", "other-thread")
	second = strings.ReplaceAll(second, "native-session", "other-tree")
	frames = append(frames, `c2s {"id":3,"method":"thread/start","params":{"ephemeral":true}}`, second)
	frames = append(frames,
		strings.ReplaceAll(turnStart()[0], `"id":3`, `"id":4`), strings.ReplaceAll(turnStart()[1], `"id":3`, `"id":4`),
		`s2c {"id":"permission-A","method":"item/commandExecution/requestApproval","params":{"threadId":"native-thread","turnId":"native-turn","itemId":"tool-A","startedAtMs":1,"command":"synthetic-A"}}`,
		`c2s {"id":5,"method":"turn/start","params":{"threadId":"other-thread","input":[{"type":"text","text":"hello","text_elements":[]}]}}`,
		`s2c {"id":5,"result":{"turn":{"id":"other-turn","status":"inProgress","items":[],"error":null}}}`,
		`s2c {"id":"permission-B","method":"item/commandExecution/requestApproval","params":{"threadId":"other-thread","turnId":"other-turn","itemId":"tool-B","startedAtMs":1,"command":"synthetic-B"}}`,
		`c2s {"id":"permission-B","result":{"decision":"accept"}}`,
		`c2s {"id":"permission-A","result":{"decision":"decline"}}`)
	worker, ctx := s.connect(frames)
	a := testutil.OpenSession(s.T(), worker, ctx, rt.SessionOptions{})
	b := testutil.OpenSession(s.T(), worker, ctx, rt.SessionOptions{})
	for _, session := range []rt.Session{a, b} {
		_, err := session.Submit(ctx, "hello")
		s.Require().NoError(err)
		event, err := session.Next(ctx)
		s.Require().NoError(err)
		s.Equal(rt.SessionReady, event.Kind)
	}
	// Consume B first, while A's request remains unread.
	eventB := s.nextMapped(b, ctx)
	eventA := s.nextMapped(a, ctx)
	for _, tc := range []struct {
		session rt.Session
		event   rt.Event
		id      string
		allow   bool
	}{
		{b, eventB, `"permission-B"`, true}, {a, eventA, `"permission-A"`, false},
	} {
		s.Equal(rt.InteractionRequested, tc.event.Kind)
		s.Equal(tc.session.Key().RuntimeName, tc.event.RuntimeName)
		s.Equal(tc.session.Key().SessionID, tc.event.SessionID)
		s.Require().NotNil(tc.event.Interaction)
		s.Equal(tc.id, tc.session.ResponseHandle(tc.event.Interaction.Ref).ID())
		s.NoError(tc.session.ResponseHandle(tc.event.Interaction.Ref).Respond(ctx, rt.Response{Allow: tc.allow}))
	}
}
