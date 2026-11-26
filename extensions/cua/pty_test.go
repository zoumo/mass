package cua_test

import (
	"bytes"
	"context"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/zoumo/mass/extensions/cua/internal/processpb"
)

const rpcProcess = "/cua.env.v1.ProcessService/"

func TestPTYStdio(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "cua-mcp")
	build := exec.CommandContext(t.Context(), "go", "build", "-mod=readonly", "-o", bin, "./cmd/cua-mcp")
	output, err := build.CombinedOutput()
	require.NoError(t, err, "%s", output)

	t.Run("resume reads and writes on the same detached process", func(t *testing.T) {
		relay := newProcessRelay(t)
		ctx, session, _ := startPTYBridge(t, bin, relay, "first-mac", "second-mac")

		tools, err := session.ListTools(ctx, nil)
		require.NoError(t, err)
		var names []string
		for _, tool := range tools.Tools {
			names = append(names, tool.Name)
		}
		assert.Subset(t, names, []string{"pty_start", "pty_read", "pty_write", "pty_stop"})

		args := map[string]any{"operation_id": "same-op", "argv": []string{"/usr/bin/printf", "READY first"}, "cwd": "/the/guest"}
		ptyCallError(t, ctx, session, "pty_start", args, "computer_id is required")
		args["computer_id"] = "first-mac"
		relay.failStart.Store(true)
		ptyCallError(t, ctx, session, "pty_start", args, "start outcome unknown")
		changed := map[string]any{"operation_id": "same-op", "computer_id": "first-mac", "argv": []string{"other"}}
		ptyCallError(t, ctx, session, "pty_start", changed, "different argv or cwd")

		first := ptyCall(t, ctx, session, "pty_start", args)
		assert.Equal(t, "running", first["state"])
		assert.EqualValues(t, 0, first["cursor"])
		assert.Equal(t, first, ptyCall(t, ctx, session, "pty_start", args))
		starts := relay.startRequests()
		require.Len(t, starts, 2)
		assert.Equal(t, starts[0].GetRequestId(), starts[1].GetRequestId())
		assert.Equal(t, starts[0].GetTag(), starts[1].GetTag())
		assert.Equal(t, "/usr/bin/printf", starts[1].GetConfig().GetCommand())
		assert.Equal(t, []string{"READY first"}, starts[1].GetConfig().GetArgs())
		assert.Equal(t, "/the/guest", starts[1].GetConfig().GetCwd())
		firstTag := starts[0].GetTag()

		args["computer_id"] = "second-mac"
		args["argv"] = []string{"/bin/echo", "READY second"}
		second := ptyCall(t, ctx, session, "pty_start", args)
		starts = relay.startRequests()
		secondTag := starts[2].GetTag()
		cleanup := ptyCall(t, ctx, session, "pty_start", map[string]any{
			"computer_id": "first-mac", "operation_id": "cleanup", "argv": []string{"cat", "READY cleanup"},
		})
		starts = relay.startRequests()
		cleanupTag := starts[3].GetTag()
		assert.NotEqual(t, first["handle"], second["handle"])
		assert.NotEqual(t, first["handle"], cleanup["handle"])
		assert.NotEqual(t, firstTag, secondTag)
		assert.NotEqual(t, firstTag, first["handle"])

		ptyCallError(t, ctx, session, "pty_read", map[string]any{"handle": first["handle"]}, "cursor")
		ptyCallError(t, ctx, session, "pty_read", map[string]any{"handle": first["handle"], "cursor": 0, "computer_id": "second-mac"}, "computer_id")
		read := func(cursor, maxBytes int) map[string]any {
			return ptyCall(t, ctx, session, "pty_read", map[string]any{
				"handle": first["handle"], "cursor": cursor, "wait_ms": 0, "max_bytes": maxBytes,
			})
		}
		part := read(0, 5)
		assert.Equal(t, "READY", part["output"])
		assert.EqualValues(t, 5, part["next_cursor"])
		assert.EqualValues(t, 13, part["output_end_cursor"])
		assert.Equal(t, "UkVBRFk=", part["data_base64"])

		relay.offline.Store(true)
		ptyCallError(t, ctx, session, "pty_read", map[string]any{"handle": first["handle"], "cursor": 5, "wait_ms": 0}, "relay or transport")
		relay.offline.Store(false)
		part = read(5, 64)
		assert.Equal(t, "IGZpcnN0/wA=", part["data_base64"])
		assert.NotContains(t, part, "output")
		assert.EqualValues(t, 13, part["next_cursor"])
		assert.Equal(t, attachRecord{firstTag, 0}, <-relay.attachments)
		assert.Equal(t, attachRecord{firstTag, 5}, <-relay.attachments)

		empty := ptyCall(t, ctx, session, "pty_read", map[string]any{"handle": first["handle"], "cursor": 13, "wait_ms": 30})
		assert.Empty(t, empty["data_base64"])
		assert.EqualValues(t, 13, empty["next_cursor"])
		assert.Equal(t, attachRecord{firstTag, 13}, <-relay.attachments)

		canceled, cancel := context.WithCancel(ctx)
		readDone := make(chan error, 1)
		go func() {
			_, err := session.CallTool(canceled, &mcp.CallToolParams{Name: "pty_read", Arguments: map[string]any{
				"handle": first["handle"], "cursor": 13, "wait_ms": 30_000,
			}})
			readDone <- err
		}()
		select {
		case attached := <-relay.attachments:
			assert.Equal(t, attachRecord{firstTag, 13}, attached)
		case <-ctx.Done():
			t.Fatal("cancellable read did not reach the relay")
		}
		cancel()
		require.ErrorIs(t, <-readDone, context.Canceled)
		for range 2 {
			select {
			case <-relay.detached:
			case <-ctx.Done():
				t.Fatal("PTY read did not detach at the relay")
			}
		}

		type liveRead struct {
			result *mcp.CallToolResult
			err    error
		}
		liveReadResult := make(chan liveRead, 1)
		go func() {
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "pty_read", Arguments: map[string]any{
				"handle": first["handle"], "cursor": 13, "wait_ms": 5000,
			}})
			liveReadResult <- liveRead{result, err}
		}()
		select {
		case attachment := <-relay.attachments:
			assert.Equal(t, attachRecord{firstTag, 13}, attachment)
		case <-ctx.Done():
			t.Fatal("waiting read did not reach the relay")
		}
		write := map[string]any{"handle": first["handle"], "sequence": 1, "data_base64": "/wo="}
		relay.failWrite.Store(true)
		ptyCallError(t, ctx, session, "pty_write", write, "input outcome unknown")
		ready := <-liveReadResult
		require.NoError(t, ready.err)
		require.False(t, ready.result.IsError)
		live := ready.result.StructuredContent.(map[string]any)
		assert.Equal(t, "one\r\n", live["output"])
		assert.EqualValues(t, 18, live["next_cursor"])
		ptyCallError(t, ctx, session, "pty_write", map[string]any{"handle": first["handle"], "sequence": 1, "text": "changed"}, "different PTY input")
		ptyCallError(t, ctx, session, "pty_write", map[string]any{"handle": first["handle"], "sequence": 2, "text": "early"}, "expected sequence 1")
		retried := ptyCall(t, ctx, session, "pty_write", write)
		assert.Equal(t, true, retried["duplicate"])
		assert.EqualValues(t, 1, retried["applied_sequence"])
		assert.Equal(t, retried, ptyCall(t, ctx, session, "pty_write", write))
		writes := relay.writeRequests()
		require.Len(t, writes, 2)
		assert.Equal(t, writes[0].GetWriterId(), writes[1].GetWriterId())
		assert.Equal(t, []byte{0xff, '\n'}, writes[1].GetInput().GetPty())
		assert.Equal(t, firstTag, writes[1].GetProcess().GetTag())
		assert.Equal(t, uint64(1), writes[1].GetSequence())

		other := ptyCall(t, ctx, session, "pty_write", map[string]any{"handle": second["handle"], "sequence": 1, "text": "another\n"})
		assert.Equal(t, false, other["duplicate"])
		writes = relay.writeRequests()
		assert.Equal(t, secondTag, writes[2].GetProcess().GetTag())
		assert.NotEqual(t, writes[1].GetWriterId(), writes[2].GetWriterId())
		res := ptyCall(t, ctx, session, "pty_write", map[string]any{"handle": first["handle"], "sequence": 2, "text": "done\n"})
		assert.EqualValues(t, 2, res["applied_sequence"])
		assert.Equal(t, []byte("done\n"), relay.writeRequests()[3].GetInput().GetPty())
		exited := read(18, 64)
		assert.Equal(t, "done\r\n", exited["output"])
		assert.Equal(t, "exited", exited["state"])
		assert.EqualValues(t, 37, exited["exit_code"])

		relay.mu.Lock()
		relay.processes[firstTag].base = 10
		relay.mu.Unlock()
		overwritten := read(2, 3)
		assert.Equal(t, map[string]any{"from": float64(2), "to": float64(10)}, overwritten["lost"])
		assert.EqualValues(t, 13, overwritten["next_cursor"])
		assert.Equal(t, "dP8A", overwritten["data_base64"])

		deliveryError := map[string]any{"handle": second["handle"], "sequence": 2, "text": "delivery-error"}
		for range 2 {
			ptyCallError(t, ctx, session, "pty_write", deliveryError, "outcome unknown after Cua rejected delivery")
		}
		require.Len(t, relay.writeRequests(), 5)

		stopped := ptyCall(t, ctx, session, "pty_stop", map[string]any{"handle": second["handle"]})
		assert.Equal(t, true, stopped["signal_sent"])
		termination := ptyCall(t, ctx, session, "pty_read", map[string]any{"handle": second["handle"], "cursor": 0})
		assert.Equal(t, "exited", termination["state"])
		assert.Equal(t, "TERM", termination["signal"])
		assert.NotContains(t, termination, "exit_code")

		// Evict the first process from the attachable registry, as spacesd does.
		// Its ledger still proves it exited.
		relay.mu.Lock()
		delete(relay.processes, firstTag)
		relay.mu.Unlock()
		ptyCallError(t, ctx, session, "pty_read", map[string]any{"handle": first["handle"], "cursor": 13}, "process has been lost")
		args["computer_id"] = "first-mac"
		args["argv"] = []string{"/usr/bin/printf", "READY first"}
		ptyCallError(t, ctx, session, "pty_start", args, "process has been lost")
		require.Len(t, relay.startRequests(), 4)

		require.NoError(t, session.Close())
		relay.mu.Lock()
		require.Len(t, relay.signals, 2)
		assert.Equal(t, secondTag, relay.signals[0].GetProcess().GetTag())
		assert.Equal(t, pb.Signal_SIGNAL_TERM, relay.signals[0].GetSignal())
		assert.Equal(t, cleanupTag, relay.signals[1].GetProcess().GetTag())
		assert.Equal(t, pb.Signal_SIGNAL_KILL, relay.signals[1].GetSignal())
		assert.NotNil(t, relay.processes[cleanupTag].info.GetEnd())
		assert.Positive(t, relay.processes[cleanupTag].exitReadbacks)
		relay.mu.Unlock()

		freshCtx, fresh, _ := startPTYBridge(t, bin, relay, "first-mac", "second-mac")
		ptyCallError(t, freshCtx, fresh, "pty_stop", map[string]any{"handle": cleanup["handle"]}, "unknown PTY handle")
	})

	t.Run("shutdown reconciles an unacknowledged start", func(t *testing.T) {
		relay := newProcessRelay(t)
		ctx, session, _ := startPTYBridge(t, bin, relay, "first-mac")
		relay.failStart.Store(true)
		ptyCallError(t, ctx, session, "pty_start", map[string]any{
			"operation_id": "uncertain", "argv": []string{"cat", "WAIT"},
		}, "start outcome unknown")
		require.NoError(t, session.Close())
		relay.mu.Lock()
		defer relay.mu.Unlock()
		require.Len(t, relay.starts, 1)
		tag := relay.starts[0].GetTag()
		require.Len(t, relay.signals, 1)
		assert.Equal(t, tag, relay.signals[0].GetProcess().GetTag())
		assert.Equal(t, pb.Signal_SIGNAL_KILL, relay.signals[0].GetSignal())
		assert.Positive(t, relay.processes[tag].exitReadbacks)
	})

	t.Run("shutdown reports unreachable cleanup", func(t *testing.T) {
		relay := newProcessRelay(t)
		ctx, session, stderr := startPTYBridge(t, bin, relay, "first-mac")
		ptyCall(t, ctx, session, "pty_start", map[string]any{
			"operation_id": "offline", "argv": []string{"cat", "WAIT"},
		})
		relay.offline.Store(true)
		_ = session.Close()
		assert.Contains(t, stderr.String(), "PTY cleanup on first-mac")
		assert.NotContains(t, stderr.String(), fixtureTokenPrefix)
	})
}

func startPTYBridge(t *testing.T, bin string, relay *processRelayFixture, ids ...string) (context.Context, *mcp.ClientSession, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.pem")
	der := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: relay.server.Certificate().Raw})
	require.NoError(t, os.WriteFile(caFile, der, 0o600))
	var cfg strings.Builder
	fmt.Fprintf(&cfg, "cuaCLI: /unused-in-pty-test\nrelayURL: %q\ncaFile: ca.pem\ncomputers:\n", relay.server.URL)
	for _, id := range ids {
		tokenFile := id + ".token"
		require.NoError(t, os.WriteFile(filepath.Join(dir, tokenFile), []byte(fixtureTokenPrefix+id), 0o600))
		fmt.Fprintf(&cfg, "  - id: %q\n    tokenFile: %q\n", id, tokenFile)
	}
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(cfg.String()), 0o600))
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	stderr := new(bytes.Buffer)
	cmd := exec.CommandContext(ctx, bin, "--config", path)
	cmd.Stderr = stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "pty-test"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	require.NoError(t, err, "%s", stderr)
	t.Cleanup(func() { _ = session.Close() })
	return ctx, session, stderr
}

func ptyCall(t *testing.T, ctx context.Context, session *mcp.ClientSession, tool string, args map[string]any) map[string]any {
	t.Helper()
	reply, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	require.NoError(t, err)
	require.False(t, reply.IsError, "%s: %v", tool, reply.Content[0].(*mcp.TextContent).Text)
	data, ok := reply.StructuredContent.(map[string]any)
	require.True(t, ok, "%s: %v", tool, reply)
	return data
}

func ptyCallError(t *testing.T, ctx context.Context, session *mcp.ClientSession, tool string, args map[string]any, want string) {
	t.Helper()
	reply, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	require.NoError(t, err)
	require.True(t, reply.IsError, "%s: %v", tool, reply)
	content := reply.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, content, want)
	assert.NotContains(t, content, fixtureTokenPrefix)
}

type relayProcess struct {
	changed       chan struct{}
	target        string
	info          *pb.ProcessInfo
	output        []byte
	base          int
	writers       map[string]uint64
	exitReadbacks int
}

type attachRecord struct {
	tag    string
	cursor uint64
}

type processRelayFixture struct {
	server    *httptest.Server
	offline   atomic.Bool
	failStart atomic.Bool
	failWrite atomic.Bool

	mu          sync.Mutex
	processes   map[string]*relayProcess
	ledger      map[string]*pb.ExecRecord
	starts      []*pb.StartProcessRequest
	writes      []*pb.SendInputRequest
	signals     []*pb.SignalProcessRequest
	attachments chan attachRecord
	detached    chan struct{}
}

func (f *processRelayFixture) startRequests() []*pb.StartProcessRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.starts)
}

func (f *processRelayFixture) writeRequests() []*pb.SendInputRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.writes)
}

func newProcessRelay(t *testing.T) *processRelayFixture {
	f := &processRelayFixture{
		processes:   make(map[string]*relayProcess),
		ledger:      make(map[string]*pb.ExecRecord),
		attachments: make(chan attachRecord, 32),
		detached:    make(chan struct{}, 32),
	}
	root := http.NewServeMux()
	for _, id := range []string{"first-mac", "second-mac"} {
		prefix := "/m/" + id
		mux := http.NewServeMux()
		mux.Handle(rpcProcess+"StartProcess", connect.NewServerStreamHandler(rpcProcess+"StartProcess",
			func(_ context.Context, req *connect.Request[pb.StartProcessRequest], stream *connect.ServerStream[pb.StartProcessResponse]) error {
				return f.start(t, id, req.Msg, stream)
			}))
		mux.Handle(rpcProcess+"ConnectProcess", connect.NewServerStreamHandler(rpcProcess+"ConnectProcess",
			func(ctx context.Context, req *connect.Request[pb.ConnectProcessRequest], stream *connect.ServerStream[pb.ConnectProcessResponse]) error {
				return f.attach(ctx, t, id, req.Msg, stream)
			}))
		mux.Handle(rpcProcess+"ListProcesses", connect.NewUnaryHandler(rpcProcess+"ListProcesses",
			func(_ context.Context, req *connect.Request[pb.ListProcessesRequest]) (*connect.Response[pb.ListProcessesResponse], error) {
				return f.list(t, id, req.Msg)
			}))
		mux.Handle(rpcProcess+"SendInput", connect.NewUnaryHandler(rpcProcess+"SendInput",
			func(_ context.Context, req *connect.Request[pb.SendInputRequest]) (*connect.Response[pb.SendInputResponse], error) {
				return f.write(id, req.Msg)
			}))
		mux.Handle(rpcProcess+"SignalProcess", connect.NewUnaryHandler(rpcProcess+"SignalProcess",
			func(_ context.Context, req *connect.Request[pb.SignalProcessRequest]) (*connect.Response[pb.SignalProcessResponse], error) {
				return f.signal(t, id, req.Msg)
			}))
		mux.Handle(rpcProcess+"GetExecRecord", connect.NewUnaryHandler(rpcProcess+"GetExecRecord",
			func(_ context.Context, req *connect.Request[pb.GetExecRecordRequest]) (*connect.Response[pb.GetExecRecordResponse], error) {
				f.mu.Lock()
				defer f.mu.Unlock()
				return connect.NewResponse(&pb.GetExecRecordResponse{Record: f.ledger[req.Msg.GetRequestId()]}), nil
			}))
		root.Handle(prefix+"/", http.StripPrefix(prefix, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "application/grpc-web+proto", r.Header.Get("Content-Type"))
			assert.Equal(t, "Bearer "+fixtureTokenPrefix+id, r.Header.Get("Authorization"))
			if f.offline.Load() {
				// Simulate losing the relay path without changing process state.
				conn, _, err := http.NewResponseController(w).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close()
				return
			}
			mux.ServeHTTP(w, r)
		})))
	}
	f.server = httptest.NewTLSServer(root)
	t.Cleanup(f.server.Close)
	return f
}

func (f *processRelayFixture) start(t *testing.T, id string, req *pb.StartProcessRequest, stream *connect.ServerStream[pb.StartProcessResponse]) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	assert.NotEmpty(t, req.GetRequestId())
	assert.True(t, req.GetStdin())
	assert.NotNil(t, req.GetPty())
	assert.False(t, req.GetKillOnDisconnect())
	f.starts = append(f.starts, req)
	tag := req.GetTag()
	process := f.processes[tag]
	deduplicated := process != nil
	if process == nil {
		process = &relayProcess{
			changed: make(chan struct{}),
			target:  id,
			info: &pb.ProcessInfo{
				Tag: tag, Pid: uint32(len(f.processes) + 10),
				Pty: true, State: pb.ProcessState_PROCESS_STATE_RUNNING,
			},
			output:  append([]byte(req.GetConfig().GetArgs()[0]), 0xff, 0),
			writers: make(map[string]uint64),
		}
		f.processes[tag] = process
	}
	if f.failStart.Swap(false) {
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("relay dropped start reply containing %s%s", fixtureTokenPrefix, id))
	}
	event := process.startEvent()
	start := event.GetStart()
	start.RequestId = req.GetRequestId()
	start.Deduplicated = deduplicated
	return stream.Send(&pb.StartProcessResponse{Event: event})
}

func (p *relayProcess) startEvent() *pb.ProcessEvent {
	return &pb.ProcessEvent{Event: &pb.ProcessEvent_Start{Start: &pb.ProcessStart{
		Tag: p.info.GetTag(), Pid: p.info.GetPid(), ScrollbackStartOffset: uint64(p.base), OutputEndOffset: uint64(len(p.output)),
	}}}
}

func (f *processRelayFixture) list(t *testing.T, id string, req *pb.ListProcessesRequest) (*connect.Response[pb.ListProcessesResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	assert.True(t, req.GetIncludeExited())
	process := f.processes[req.GetTagPrefix()]
	if process == nil {
		return connect.NewResponse(&pb.ListProcessesResponse{}), nil
	}
	if process.target != id {
		return nil, connect.NewError(connect.CodeNotFound, nil)
	}
	if process.info.GetEnd() != nil {
		process.exitReadbacks++
	}
	return connect.NewResponse(&pb.ListProcessesResponse{Processes: []*pb.ProcessInfo{{
		Pid: process.info.GetPid(), Tag: process.info.GetTag(), Pty: true, State: process.info.GetState(), End: process.info.GetEnd(),
		ScrollbackStartOffset: uint64(process.base), OutputEndOffset: uint64(len(process.output)),
	}}}), nil
}

func (f *processRelayFixture) attach(ctx context.Context, t *testing.T, id string, req *pb.ConnectProcessRequest, stream *connect.ServerStream[pb.ConnectProcessResponse]) error {
	f.mu.Lock()
	process := f.processes[req.GetProcess().GetTag()]
	if process == nil || process.target != id {
		f.mu.Unlock()
		return connect.NewError(connect.CodeNotFound, nil)
	}
	cursor := req.GetReplayFromOffset()
	assert.NotNil(t, req.ReplayFromOffset, "explicit offset is required even for zero")
	startEvent := process.startEvent()
	f.attachments <- attachRecord{process.info.GetTag(), cursor}
	f.mu.Unlock()
	if err := stream.Send(&pb.ConnectProcessResponse{Event: startEvent}); err != nil {
		return err
	}

	for {
		f.mu.Lock()
		offset := max(int(cursor), process.base)
		data := bytes.Clone(process.output[offset:])
		end, changed := process.info.GetEnd(), process.changed
		f.mu.Unlock()
		if len(data) > 0 {
			event := &pb.ProcessEvent{Event: &pb.ProcessEvent_Data{Data: &pb.ProcessData{
				Offset: uint64(offset), Output: &pb.ProcessData_Pty{Pty: data},
			}}}
			if err := stream.Send(&pb.ConnectProcessResponse{Event: event}); err != nil {
				return err
			}
			cursor = uint64(offset + len(data))
		}
		if end != nil {
			event := &pb.ProcessEvent{Event: &pb.ProcessEvent_End{End: end}}
			return stream.Send(&pb.ConnectProcessResponse{Event: event})
		}
		select {
		case <-ctx.Done():
			if startEvent.GetStart().GetOutputEndOffset() == req.GetReplayFromOffset() {
				f.detached <- struct{}{}
			}
			return ctx.Err()
		case <-changed:
		}
	}
}

func (f *processRelayFixture) write(id string, req *pb.SendInputRequest) (*connect.Response[pb.SendInputResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	process := f.processes[req.GetProcess().GetTag()]
	if process == nil || process.target != id {
		return nil, connect.NewError(connect.CodeNotFound, nil)
	}
	f.writes = append(f.writes, req)
	if string(req.GetInput().GetPty()) == "delivery-error" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, nil)
	}
	last, sequence := process.writers[req.GetWriterId()], req.GetSequence()
	reply := &pb.SendInputResponse{AppliedSequence: sequence}
	switch {
	case last == sequence:
		reply.Duplicate = true
	case last+1 != sequence:
		return nil, connect.NewError(connect.CodeFailedPrecondition, nil)
	default:
		process.writers[req.GetWriterId()] = sequence
		if sequence == 1 {
			process.output = append(process.output, "one\r\n"...)
			process.notifyOutput()
		} else {
			process.output = append(process.output, "done\r\n"...)
			code := int32(37)
			f.exit(process, &pb.ProcessEnd{ExitCode: &code})
		}
	}
	if f.failWrite.Swap(false) {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("relay dropped write reply containing %s%s", fixtureTokenPrefix, id))
	}
	return connect.NewResponse(reply), nil
}

func (f *processRelayFixture) signal(t *testing.T, id string, req *pb.SignalProcessRequest) (*connect.Response[pb.SignalProcessResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	process := f.processes[req.GetProcess().GetTag()]
	if process == nil || process.target != id {
		return nil, connect.NewError(connect.CodeNotFound, nil)
	}
	assert.True(t, req.GetProcessGroup())
	f.signals = append(f.signals, req)
	f.exit(process, &pb.ProcessEnd{Signal: req.GetSignal()})
	return connect.NewResponse(&pb.SignalProcessResponse{}), nil
}

func (f *processRelayFixture) exit(process *relayProcess, end *pb.ProcessEnd) {
	process.info.State = pb.ProcessState_PROCESS_STATE_EXITED
	process.info.End = end
	process.notifyOutput()
	tag := process.info.GetTag()
	f.ledger[tag] = &pb.ExecRecord{RequestId: tag, State: pb.ExecState_EXEC_STATE_EXITED, End: end}
}

func (p *relayProcess) notifyOutput() {
	close(p.changed)
	p.changed = make(chan struct{})
}
