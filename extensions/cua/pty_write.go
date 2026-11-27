package cua

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	pb "github.com/zoumo/mass/extensions/cua/internal/processpb"
)

type ptyInputState struct {
	applied uint64
	digests map[uint64][sha256.Size]byte
	unknown bool
	blocked error
}

func (p *ptyProcess) write(ctx context.Context, sequence uint64, data []byte) (ptyWriteResult, error) {
	digest := sha256.Sum256(data)
	p.inputMu.Lock()
	defer p.inputMu.Unlock()

	state := &p.input
	if previous, exists := state.digests[sequence]; exists && previous != digest {
		return ptyWriteResult{}, errors.New("sequence already identifies different PTY input")
	}
	switch {
	case sequence <= state.applied:
		return ptyWriteResult{AppliedSequence: state.applied, Duplicate: true}, nil
	case sequence != state.applied+1:
		return ptyWriteResult{}, fmt.Errorf("out-of-order PTY input: expected sequence %d", state.applied+1)
	}

	if state.blocked != nil {
		return ptyWriteResult{}, state.blocked
	}

	ctx, cancel := context.WithTimeout(ctx, ptyRPCTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return ptyWriteResult{}, err
	}
	if state.digests == nil {
		state.digests = make(map[uint64][sha256.Size]byte)
	}
	state.digests[sequence] = digest

	req := processRequest(p.client, &pb.SendInputRequest{
		Process: p.selector(), Sequence: sequence, WriterId: p.handle,
		Input: &pb.ProcessInput{Input: &pb.ProcessInput_Pty{Pty: data}},
	})
	res, err := p.client.input.CallUnary(ctx, req)
	if err != nil {
		code := connect.CodeOf(err)
		if connect.IsWireError(err) && code == connect.CodeNotFound {
			if state.unknown {
				return ptyWriteResult{}, fmt.Errorf("PTY input outcome unknown: %w", errPTYLost)
			}
			return ptyWriteResult{}, errPTYLost
		}
		safeErr := processRPCError(ctx, "SendInput", err)
		if connect.IsWireError(err) && code == connect.CodeFailedPrecondition {
			// A delivery failure can follow a partial write. Cua only records
			// the sequence on success, so replay could write the prefix twice.
			state.blocked = fmt.Errorf("PTY input outcome unknown after Cua rejected delivery: %w", safeErr)
			return ptyWriteResult{}, state.blocked
		}
		if connect.IsWireError(err) && (code == connect.CodeInvalidArgument ||
			code == connect.CodePermissionDenied || code == connect.CodeUnauthenticated || code == connect.CodeUnimplemented) && !state.unknown {
			return ptyWriteResult{}, safeErr
		}
		state.unknown = true
		return ptyWriteResult{}, fmt.Errorf("PTY input outcome unknown; retry with the same sequence and bytes: %w", safeErr)
	}
	if res.Msg.GetAppliedSequence() != sequence {
		state.unknown = true
		return ptyWriteResult{}, errors.New("PTY input outcome unknown: Cua returned an unexpected applied sequence")
	}
	state.applied = sequence
	state.unknown = false
	return ptyWriteResult{AppliedSequence: sequence, Duplicate: res.Msg.GetDuplicate()}, nil
}
