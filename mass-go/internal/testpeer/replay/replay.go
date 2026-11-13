package replay

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/zoumo/mass/mass-go/internal/testpeer/rawjson"
	"github.com/zoumo/mass/mass-go/internal/testpeer/scenario"
)

var ErrDisconnected = errors.New("scripted disconnect")

// Run keeps transport mechanics separate from the two native ID contracts.
// Reads and writes are strict: exactly one client frame per recorded c2s frame,
// no extra client byte after the script, and no unfinished request.
func Run(ctx context.Context, document *scenario.Document, limits scenario.Limits, input io.Reader, output io.Writer, sessionIDs ...string) (err error) {
	assignedSessionID := ""
	if len(sessionIDs) > 0 {
		assignedSessionID = sessionIDs[0]
	}
	return run(ctx, document, limits, input, output, assignedSessionID, "")
}

// RunClaim is Run for a synthetic Claude CLI started with --await-claim. The
// peer answers the SDK's claim_session request with claimSessionID before
// handing the next scripted client frame to the strict replay engine, and
// reports claimCWD for the synthetic working-directory placeholder.
func RunClaim(ctx context.Context, document *scenario.Document, limits scenario.Limits, input io.Reader, output io.Writer, claimSessionID, claimCWD string) error {
	return run(ctx, document, limits, input, output, claimSessionID, claimCWD)
}

func run(ctx context.Context, document *scenario.Document, limits scenario.Limits, input io.Reader, output io.Writer, assignedSessionID, claimCWD string) (err error) {
	if ctx == nil || document == nil || input == nil || output == nil {
		return errors.New("context, document and protocol streams required")
	}
	a, err := newAdapter(document.Provider, assignedSessionID)
	if err != nil {
		return err
	}
	limits, err = limits.Resolve(document.Provider)
	if err != nil {
		return err
	}
	p := peerIO{input: bufio.NewReader(input), output: output, limits: limits}
	if claimCWD != "" {
		p.claimSessionID = assignedSessionID
		p.claimCWD = claimCWD
	}
	var ordinal int64
	initializeRequestID := ""
	defer func() {
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = errors.Join(ctxErr, err)
		}
		if err != nil && ordinal != 0 {
			err = fmt.Errorf("ordinal %d: %w", ordinal, err)
		}
	}()
	for _, frame := range document.Frames {
		ordinal = frame.Ordinal
		if err := ctx.Err(); err != nil {
			return err
		}
		if frame.Delay != 0 {
			timer := time.NewTimer(frame.Delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		if frame.Disconnect {
			return ErrDisconnected
		}
		if frame.Direction == scenario.ClientToServer {
			var actual rawjson.Value
			actual, err = p.read()
			if err != nil {
				return err
			}
			if stringMember(member(actual, "request"), "subtype") == "initialize" {
				initializeRequestID = stringMember(actual, "request_id")
			}
			if _, err = a.client(frame.Wire, actual); err != nil {
				return err
			}
		} else {
			wire := frame.Wire
			if p.claimCWD != "" {
				wire, err = replaceSyntheticCWD(wire, p.claimCWD)
				if err != nil {
					return err
				}
			}
			var emitted rawjson.Value
			emitted, err = a.server(wire)
			if err != nil {
				return err
			}
			if err := p.write(emitted.Raw()); err != nil {
				return err
			}
			if p.claimSessionID != "" && initializeRequestID != "" &&
				stringMember(emitted, "type") == "control_response" &&
				stringMember(member(emitted, "response"), "request_id") == initializeRequestID {
				if err := p.claim(); err != nil {
					return err
				}
			}
		}
	}
	if err := a.complete(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err = p.input.ReadByte(); err == nil {
		return errors.New("extra client byte after scenario")
	} else if errors.Is(err, io.EOF) {
		return nil
	} else {
		return err
	}
}

type peerIO struct {
	input                 *bufio.Reader
	output                io.Writer
	limits                scenario.Limits
	claimSessionID        string
	claimCWD              string
	readBytes, writeBytes int64
}

func (p *peerIO) read() (rawjson.Value, error) {
	return p.readFrame()
}

func (p *peerIO) claim() error {
	value, err := p.readFrame()
	if err != nil {
		return err
	}
	reply, claimed, err := claimExchange(value, p.claimSessionID)
	if err != nil {
		return err
	}
	if !claimed {
		return errors.New("expected claim_session after initialize")
	}
	p.claimCWD = stringMember(member(member(reply, "response"), "response"), "cwd")
	p.claimSessionID = ""
	if err := p.write(reply.Raw()); err != nil {
		return err
	}
	return nil
}

func (p *peerIO) readFrame() (rawjson.Value, error) {
	var line []byte
	for {
		part, err := p.input.ReadSlice('\n')
		if int64(len(part)) > p.limits.MaxBytes-p.readBytes {
			return rawjson.Value{}, errors.New("client traffic byte limit exceeded")
		}
		p.readBytes += int64(len(part))
		// Allow the exclusive payload bound plus at most CR and LF. This
		// bounds accumulation even for a client that never sends a newline.
		if int64(len(line))+int64(len(part))-1 > p.limits.MaxFrameBytes {
			return rawjson.Value{}, errors.New("client frame byte limit exceeded")
		}
		line = append(line, part...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return rawjson.Value{}, err
		}
		if len(line) == 0 {
			return rawjson.Value{}, errors.New("early client EOF")
		}
		break
	}
	line = bytes.TrimSuffix(line, []byte{'\n'})
	line = bytes.TrimSuffix(line, []byte{'\r'})
	if int64(len(line)) >= p.limits.MaxFrameBytes {
		return rawjson.Value{}, errors.New("client frame byte limit exceeded")
	}
	value, err := rawjson.Parse(line)
	if err != nil {
		return rawjson.Value{}, err
	}
	if value.Kind() != rawjson.Object {
		return rawjson.Value{}, errors.New("client JSON must be an object")
	}
	return value, nil
}

func (p *peerIO) write(payload []byte) error {
	if int64(len(payload)) >= p.limits.MaxFrameBytes {
		return errors.New("server frame byte limit exceeded")
	}
	data := append(append([]byte(nil), payload...), '\n')
	if int64(len(data)) > p.limits.MaxBytes-p.writeBytes {
		return errors.New("server traffic byte limit exceeded")
	}
	p.writeBytes += int64(len(data))
	n, err := p.output.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return err
}
