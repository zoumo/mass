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
	adapter "github.com/zoumo/mass/mass-go/pkg/runtime/codex"
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
func (s *runtimeSuite) connect(frames []string) (rt.Connection, context.Context) {
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
	conn, err := adapter.Open(ctx, adapter.Options{Executable: s.executable, Env: []string{"CODEX_MOCK_SCENARIO=" + scenario}})
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.NoError(conn.Close()) })
	return conn, ctx
}
func (s *runtimeSuite) open(frames []string) (rt.Connection, rt.Session, context.Context) {
	conn, ctx := s.connect(frames)
	creationCtx, creationCancel := context.WithCancel(ctx)
	session, err := conn.NewSession(creationCtx)
	creationCancel()
	s.Require().NoError(err)
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
				if event.Diagnostic.NativeKind == tc.method {
					found = true
					s.Equal(tc.kind, event.Kind)
					s.Len(event.Diagnostic.SHA256, 64)
					s.NotContains(event.Text, "synthetic-sensitive-marker")
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
	s.Equal(rt.Identity{Provider: "codex", SessionID: "native-thread"}, session.Identity())
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
	s.Equal("in_progress", events[0].Status)
	s.Equal(rt.Message, events[1].Kind)
	s.Equal("hello back", events[1].Text)
	s.Equal("message-1", events[1].ItemID)
	s.Equal(rt.Tool, events[2].Kind)
	s.Equal("echo synthetic", events[2].Tool)
	s.Equal("synthetic output", events[3].Text)
	s.Equal(rt.TurnCompleted, events[4].Kind)
	s.Equal("completed", events[4].Status)
	s.NoError(conn.Close())
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
			handle := event.Interaction.Handle
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
			s.Equal("interrupted", event.Status)
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
	s.NoError(conn.Close())
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
	handle := event.Interaction.Handle
	s.NoError(conn.Close())
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
	creation := make(chan error, 1)
	go func() { _, err := conn.NewSession(ctx); creation <- err }()
	// Give the mock time to enter its deliberately unanswered thread/start.
	time.Sleep(20 * time.Millisecond)
	closed := make(chan error, 1)
	go func() { closed <- conn.Close() }()
	select {
	case err := <-closed:
		s.NoError(err)
	case <-time.After(time.Second):
		s.FailNow("Close blocked by creation RPC")
	}
	select {
	case err := <-creation:
		s.Error(err)
	case <-time.After(time.Second):
		s.FailNow("creation RPC survived Close")
	}
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
	handle := event.Interaction.Handle
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
		{"MCP", `{"type":"mcpToolCall","id":"item","server":"synthetic","tool":"lookup","arguments":{},"status":"inProgress"}`, "lookup", rt.Tool},
		{"dynamic", `{"type":"dynamicToolCall","id":"item","tool":"lookup","arguments":{},"status":"inProgress"}`, "lookup", rt.Tool},
		{"reasoning", `{"type":"reasoning","id":"item","summary":[],"content":[]}`, "", rt.Unknown},
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
				if event.Diagnostic.NativeKind == "item/started" {
					s.Equal(tc.kind, event.Kind)
					s.Equal(tc.tool, event.Tool)
					break
				}
			}
		})
	}
}
