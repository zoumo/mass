package cua

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"

	pb "github.com/zoumo/mass/extensions/cua/internal/processpb"
)

var errPTYIdle = errors.New("PTY read wait elapsed")

func (p *ptyProcess) read(ctx context.Context, cursor uint64, wait time.Duration, limit int) (ptyReadResult, error) {
	lookupCtx, finishLookup := context.WithTimeout(ctx, ptyRPCTimeout)
	info, err := p.lookup(lookupCtx)
	finishLookup()
	if err != nil {
		return ptyReadResult{}, err
	}
	status, err := p.status(info)
	if err != nil {
		return ptyReadResult{}, err
	}
	result := ptyReadResult{OutputEndCursor: info.GetOutputEndOffset(), NextCursor: cursor, ptyStatus: status}
	switch {
	case cursor > info.GetOutputEndOffset():
		return ptyReadResult{}, errors.New("cursor is beyond the PTY output")
	case cursor == info.GetOutputEndOffset() && status.State == "running" && wait == 0:
		return result, nil
	}

	data, err := p.readOutput(ctx, &result, wait, limit)
	if err != nil {
		return ptyReadResult{}, err
	}
	if bytes.Contains(data, []byte(p.client.token)) {
		return ptyReadResult{}, errors.New("received unsafe PTY output")
	}
	result.DataBase64 = base64.StdEncoding.EncodeToString(data)
	if utf8.Valid(data) {
		result.Output = string(data)
	}
	return result, nil
}

func (p *ptyProcess) readOutput(ctx context.Context, result *ptyReadResult, wait time.Duration, limit int) ([]byte, error) {
	ctx, finish := context.WithTimeout(ctx, ptyRPCTimeout+wait)
	defer finish()
	streamCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	cursor := result.NextCursor
	req := processRequest(p.client, &pb.ConnectProcessRequest{Process: p.selector(), ReplayFromOffset: &cursor})
	stream, err := p.client.attach.CallServerStream(streamCtx, req)
	if err != nil {
		return nil, processRPCError(ctx, "ConnectProcess", err)
	}
	defer stream.Close()
	if !stream.Receive() {
		if err := stream.Err(); connect.IsWireError(err) && connect.CodeOf(err) == connect.CodeNotFound {
			return nil, errPTYLost
		}
		return nil, missingProcessEvent(ctx, "ConnectProcess", stream.Err())
	}
	start := stream.Msg().GetEvent().GetStart()
	if start.GetTag() != p.tag || start.GetOutputEndOffset() < cursor ||
		start.GetScrollbackStartOffset() > start.GetOutputEndOffset() {
		return nil, errors.New("inconsistent PTY output cursor or identity from Cua")
	}
	result.OutputEndCursor = start.GetOutputEndOffset()
	result.skipTo(start.GetScrollbackStartOffset())
	if result.NextCursor >= start.GetOutputEndOffset() {
		if wait == 0 || result.Lost != nil || result.State == "exited" {
			return nil, nil
		}
		timer := time.AfterFunc(wait, func() { cancel(errPTYIdle) })
		defer timer.Stop()
	}

	data := make([]byte, 0, limit)
	for stream.Receive() {
		event := stream.Msg().GetEvent()
		switch payload := event.GetEvent().(type) {
		case *pb.ProcessEvent_Data:
			chunk, ok := payload.Data.GetOutput().(*pb.ProcessData_Pty)
			if !ok {
				return nil, errors.New("received non-PTY output from Cua")
			}
			offset := payload.Data.GetOffset()
			if offset > result.NextCursor {
				// Return a contiguous run. The next call can report the next gap.
				if len(data) > 0 {
					return data, nil
				}
				result.skipTo(offset)
			}
			skip := result.NextCursor - offset
			if skip >= uint64(len(chunk.Pty)) {
				continue
			}
			count := min(len(chunk.Pty)-int(skip), limit-len(data))
			data = append(data, chunk.Pty[skip:][:count]...)
			result.NextCursor += uint64(count)
			result.OutputEndCursor = max(result.OutputEndCursor, offset+uint64(len(chunk.Pty)))
			if len(data) == limit || result.NextCursor >= start.GetOutputEndOffset() {
				return data, nil
			}
		case *pb.ProcessEvent_End:
			if err := p.processEndError(payload.End); err != nil {
				return nil, err
			}
			result.ptyStatus = exitedPTYStatus(payload.End)
			return data, nil
		}
	}
	if errors.Is(context.Cause(streamCtx), errPTYIdle) && ctx.Err() == nil {
		return data, nil
	}
	return nil, missingProcessEvent(ctx, "ConnectProcess", stream.Err())
}

func (result *ptyReadResult) skipTo(offset uint64) {
	if offset <= result.NextCursor {
		return
	}
	if result.Lost == nil {
		result.Lost = &ptyOutputGap{From: result.NextCursor}
	}
	result.Lost.To = offset
	result.NextCursor = offset
}

func (p *ptyProcess) status(info *pb.ProcessInfo) (ptyStatus, error) {
	switch info.GetState() {
	case pb.ProcessState_PROCESS_STATE_RUNNING:
		return ptyStatus{State: "running"}, nil
	case pb.ProcessState_PROCESS_STATE_EXITED:
		if info.GetEnd() == nil {
			return ptyStatus{}, errors.New("missing PTY exit status from Cua")
		}
		if err := p.processEndError(info.GetEnd()); err != nil {
			return ptyStatus{}, err
		}
		return exitedPTYStatus(info.GetEnd()), nil
	default:
		return ptyStatus{}, errors.New("unknown PTY state from Cua")
	}
}

func (p *ptyProcess) processEndError(end *pb.ProcessEnd) error {
	if end.GetLost() {
		return errPTYLost
	}
	if end.GetError() != "" {
		if strings.Contains(end.GetError(), p.client.token) {
			return errors.New("received an unsafe PTY process error from Cua")
		}
		return errors.New("PTY process failed: " + end.GetError())
	}
	return nil
}

func exitedPTYStatus(end *pb.ProcessEnd) ptyStatus {
	status := ptyStatus{State: "exited", Signal: end.GetSignalName(), TimedOut: end.GetTimedOut()}
	if end.ExitCode != nil {
		code := end.GetExitCode()
		status.ExitCode = &code
	}
	if status.Signal == "" && end.GetSignal() != pb.Signal_SIGNAL_UNSPECIFIED {
		status.Signal = strings.TrimPrefix(end.GetSignal().String(), "SIGNAL_")
	}
	status.SignalNumber = end.GetSignalNumber()
	return status
}
