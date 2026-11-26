package cua

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	pb "github.com/zoumo/mass/extensions/cua/internal/processpb"
)

const (
	ptyRPCTimeout     = 10 * time.Second
	ptyCleanupTimeout = 5 * time.Second
)

var errPTYLost = errors.New("PTY process has been lost or its retained output has expired")

type ptySession struct {
	config   *config
	lifetime context.Context
	cancel   context.CancelFunc

	mu         sync.Mutex
	closed     bool
	httpClient *http.Client
	clients    map[string]*processClient
	starts     map[ptyStartKey]*ptyProcess
	handles    map[string]*ptyProcess
}

type ptyStartKey struct {
	computerID  string
	operationID string
}

type ptyProcess struct {
	client *processClient
	handle string
	tag    string
	argv   []string
	cwd    string

	startMu        sync.Mutex
	startAttempted bool
	startConfirmed bool
	startFailure   error

	inputMu sync.Mutex
	input   ptyInputState
}

func newPTYSession(cfg *config) *ptySession {
	lifetime, cancel := context.WithCancel(context.Background())
	return &ptySession{
		config:   cfg,
		lifetime: lifetime,
		cancel:   cancel,
		starts:   make(map[ptyStartKey]*ptyProcess),
		handles:  make(map[string]*ptyProcess),
	}
}

// Open the relay client on the first PTY call. Other capabilities manage their
// own transport configuration.
func (s *ptySession) openClient() error {
	if s.httpClient != nil {
		return nil
	}
	client, err := newProcessHTTPClient(s.config.caFile)
	if err != nil {
		return err
	}
	s.httpClient = client
	s.clients = make(map[string]*processClient, len(s.config.computers))
	for id, target := range s.config.computers {
		s.clients[id] = newProcessClient(client, target)
	}
	return nil
}

func (s *ptySession) start(ctx context.Context, target *computer, args ptyStartInput) (ptyStartResult, error) {
	key := ptyStartKey{target.id, args.OperationID}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ptyStartResult{}, errors.New("PTY session is closing")
	}
	if err := s.openClient(); err != nil {
		s.mu.Unlock()
		return ptyStartResult{}, err
	}
	process := s.starts[key]
	if process == nil {
		handle := uuid.NewString()
		process = &ptyProcess{
			client: s.clients[target.id],
			handle: handle,
			tag:    "mass-pty-" + handle,
			argv:   args.Argv,
			cwd:    args.CWD,
		}
		s.starts[key] = process
	}
	s.mu.Unlock()

	if !slices.Equal(process.argv, args.Argv) || process.cwd != args.CWD {
		return ptyStartResult{}, errors.New("operation_id already identifies a start with different argv or cwd")
	}
	process.startMu.Lock()
	defer process.startMu.Unlock()
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return ptyStartResult{}, errors.New("PTY session is closing")
	}

	ctx, cancel := context.WithTimeout(ctx, ptyRPCTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return ptyStartResult{}, err
	}
	if process.startFailure != nil {
		return ptyStartResult{}, process.startFailure
	}
	if !process.startConfirmed {
		if err := process.start(ctx); err != nil {
			return ptyStartResult{}, err
		}
	}
	info, err := process.lookup(ctx)
	if err != nil {
		return ptyStartResult{}, err
	}
	status, err := process.status(info)
	if err != nil {
		return ptyStartResult{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ptyStartResult{}, errors.New("PTY session is closing")
	}
	s.handles[process.handle] = process
	return ptyStartResult{Handle: process.handle, Cursor: 0, ptyStatus: status}, nil
}

func (p *ptyProcess) start(ctx context.Context) error {
	// Each attempt uses the same tag and durable exec-ledger request ID.
	req := processRequest(p.client, &pb.StartProcessRequest{
		Config:    &pb.ProcessConfig{Command: p.argv[0], Args: p.argv[1:], Cwd: p.cwd},
		Pty:       &pb.PtyConfig{Size: &pb.PtySize{Cols: 100, Rows: 24}},
		Tag:       p.tag,
		Stdin:     true,
		RequestId: p.tag,
	})
	p.startAttempted = true
	if err := p.receiveStart(ctx, req); err != nil {
		if p.startFailure != nil {
			return p.startFailure
		}
		return fmt.Errorf("PTY start outcome unknown; retry pty_start with the same operation_id, argv and cwd: %w", err)
	}
	return nil
}

func (p *ptyProcess) receiveStart(ctx context.Context, req *connect.Request[pb.StartProcessRequest]) error {
	stream, err := p.client.start.CallServerStream(ctx, req)
	if err != nil {
		return processRPCError(ctx, "StartProcess", err)
	}
	defer stream.Close()
	if !stream.Receive() {
		return missingProcessEvent(ctx, "StartProcess", stream.Err())
	}
	start := stream.Msg().GetEvent().GetStart()
	if start.GetRequestId() != p.tag {
		return errors.New("unexpected PTY start identity from Cua")
	}
	if start.GetDeduplicated() && start.GetTag() == "" && start.GetPid() != 0 {
		// A ledger-only replay has no attachable process tag.
		p.startFailure = errPTYLost
		return p.startFailure
	}
	if start.GetPid() == 0 {
		if start.GetTag() != p.tag && !start.GetDeduplicated() {
			return errors.New("unexpected PTY tag from Cua")
		}
		if !stream.Receive() {
			return missingProcessEvent(ctx, "StartProcess", stream.Err())
		}
		if end := stream.Msg().GetEvent().GetEnd(); end != nil {
			p.startFailure = p.processEndError(end)
			if p.startFailure == nil {
				p.startFailure = errors.New("PTY process did not start")
			}
			return p.startFailure
		}
		return errors.New("missing PTY start failure details from Cua")
	}
	if start.GetTag() != p.tag {
		return errors.New("unexpected PTY tag from Cua")
	}
	p.startConfirmed = true
	return nil
}

func (s *ptySession) process(handle string) (*ptyProcess, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	process := s.handles[handle]
	if process == nil || s.closed {
		return nil, errors.New("unknown PTY handle for this MCP connection")
	}
	return process, nil
}

func (p *ptyProcess) selector() *pb.ProcessSelector {
	return &pb.ProcessSelector{Selector: &pb.ProcessSelector_Tag{Tag: p.tag}}
}

func (p *ptyProcess) lookup(ctx context.Context) (*pb.ProcessInfo, error) {
	req := processRequest(p.client, &pb.ListProcessesRequest{TagPrefix: p.tag, IncludeExited: true})
	res, err := p.client.list.CallUnary(ctx, req)
	if err != nil {
		return nil, processRPCError(ctx, "ListProcesses", err)
	}
	for _, info := range res.Msg.GetProcesses() {
		if info.GetTag() == p.tag && info.GetPty() {
			return info, nil
		}
	}
	return nil, errPTYLost
}

func (p *ptyProcess) signal(ctx context.Context, signal pb.Signal) error {
	req := processRequest(p.client, &pb.SignalProcessRequest{
		Process: p.selector(), Signal: signal, ProcessGroup: true,
	})
	_, err := p.client.signal.CallUnary(ctx, req)
	if err != nil {
		if connect.IsWireError(err) && connect.CodeOf(err) == connect.CodeNotFound {
			return errPTYLost
		}
		return processRPCError(ctx, "SignalProcess", err)
	}
	return nil
}

func (p *ptyProcess) stop(ctx context.Context) (ptyStopResult, error) {
	ctx, cancel := context.WithTimeout(ctx, ptyRPCTimeout)
	defer cancel()
	info, err := p.lookup(ctx)
	if err != nil {
		return ptyStopResult{}, err
	}
	status, err := p.status(info)
	if err != nil || status.State == "exited" {
		return ptyStopResult{ptyStatus: status}, err
	}
	if err := p.signal(ctx, pb.Signal_SIGNAL_TERM); err != nil {
		// The process may have exited between the lookup and the signal.
		if info, lookupErr := p.lookup(ctx); lookupErr == nil && info.GetEnd() != nil {
			status, lookupErr = p.status(info)
			return ptyStopResult{ptyStatus: status}, lookupErr
		}
		return ptyStopResult{}, err
	}
	return ptyStopResult{SignalSent: true}, nil
}

func (s *ptySession) close() error {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	processes := s.starts
	s.mu.Unlock()
	if s.httpClient != nil {
		defer s.httpClient.CloseIdleConnections()
	}

	ctx, cancel := context.WithTimeout(context.Background(), ptyCleanupTimeout)
	defer cancel()
	results := make(chan error, len(processes))
	for _, process := range processes {
		go func() {
			err := process.cleanup(ctx)
			if err != nil {
				err = fmt.Errorf("PTY cleanup on %s for handle %s: %w", process.client.id, process.handle, err)
			}
			results <- err
		}()
	}
	var err error
	for range processes {
		err = errors.Join(err, <-results)
	}
	return err
}

func (p *ptyProcess) cleanup(ctx context.Context) error {
	// Wait for any dispatched start before querying or signaling its tag.
	p.startMu.Lock()
	defer p.startMu.Unlock()
	if !p.startAttempted {
		return nil
	}
	info, err := p.lookup(ctx)
	if errors.Is(err, errPTYLost) {
		return p.cleanupRecord(ctx)
	}
	if err != nil {
		return err
	}
	if info.GetState() == pb.ProcessState_PROCESS_STATE_EXITED {
		return nil
	}
	signalErr := p.signal(ctx, pb.Signal_SIGNAL_KILL)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		info, err := p.lookup(ctx)
		switch {
		case errors.Is(err, errPTYLost):
			return p.cleanupRecord(ctx)
		case err != nil:
			return errors.Join(signalErr, err)
		case info.GetState() == pb.ProcessState_PROCESS_STATE_EXITED:
			return nil
		case signalErr != nil:
			return signalErr
		}
		select {
		case <-ctx.Done():
			return errors.New("PTY exit could not be confirmed")
		case <-ticker.C:
		}
	}
}

func (p *ptyProcess) cleanupRecord(ctx context.Context) error {
	req := processRequest(p.client, &pb.GetExecRecordRequest{RequestId: p.tag})
	reply, err := p.client.record.CallUnary(ctx, req)
	if err != nil {
		return processRPCError(ctx, "GetExecRecord", err)
	}
	if reply.Msg.GetRecord().GetState() == pb.ExecState_EXEC_STATE_EXITED {
		return nil
	}
	return errors.New("PTY process is missing and its exit could not be confirmed")
}

func missingProcessEvent(ctx context.Context, method string, err error) error {
	if err != nil {
		return processRPCError(ctx, method, err)
	}
	return fmt.Errorf("ProcessService %s stream ended before the expected event", method)
}
