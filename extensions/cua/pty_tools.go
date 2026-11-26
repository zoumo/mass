package cua

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxPTYReadBytes  = 64 * 1024
	maxPTYWaitMillis = 30_000
)

type ptyStartInput struct {
	ComputerID  *string  `json:"computer_id,omitempty" jsonschema:"Configured computer ID. May be omitted when only one computer is configured."`
	OperationID string   `json:"operation_id" jsonschema:"Caller-chosen start ID. Reuse this ID, argv and cwd after an uncertain start."`
	Argv        []string `json:"argv" jsonschema:"Command and arguments. Use a shell explicitly to interpret shell syntax."`
	CWD         string   `json:"cwd,omitempty" jsonschema:"Working directory on the configured computer."`
}

type ptyHandleInput struct {
	Handle string `json:"handle" jsonschema:"Handle returned by pty_start in this MCP connection."`
}

type ptyReadInput struct {
	ptyHandleInput
	Cursor   uint64 `json:"cursor" jsonschema:"Byte cursor returned by pty_start or pty_read."`
	WaitMS   *int   `json:"wait_ms,omitempty" jsonschema:"Wait for new output in milliseconds. Default 1000. Use 0 to poll. Maximum 30000."`
	MaxBytes *int   `json:"max_bytes,omitempty" jsonschema:"Maximum bytes to return. Default 16384. Range 1 to 65536."`
}

type ptyWriteInput struct {
	ptyHandleInput
	Sequence   uint64  `json:"sequence" jsonschema:"Stable input sequence starting at 1. Increase by one per confirmed write."`
	Text       *string `json:"text,omitempty" jsonschema:"UTF-8 input. Specify either text or data_base64. Include a newline when the program needs one."`
	DataBase64 *string `json:"data_base64,omitempty" jsonschema:"Raw input bytes encoded as standard Base64. Specify either text or data_base64."`
}

type ptyStatus struct {
	State        string `json:"state,omitempty" jsonschema:"State observed on the computer: running or exited."`
	ExitCode     *int32 `json:"exit_code,omitempty"`
	Signal       string `json:"signal,omitempty"`
	SignalNumber int32  `json:"signal_number,omitempty"`
	TimedOut     bool   `json:"timed_out,omitempty"`
}

type ptyStartResult struct {
	Handle string `json:"handle"`
	Cursor uint64 `json:"cursor" jsonschema:"Initial read cursor. Use this as the first pty_read cursor."`
	ptyStatus
}

type ptyReadResult struct {
	OutputEndCursor uint64        `json:"output_end_cursor" jsonschema:"Latest observed output cursor. Keep reading while next_cursor is smaller."`
	NextCursor      uint64        `json:"next_cursor"`
	Output          string        `json:"output,omitempty" jsonschema:"Output when all returned bytes are valid UTF-8."`
	DataBase64      string        `json:"data_base64,omitempty" jsonschema:"All returned output bytes in standard Base64."`
	Lost            *ptyOutputGap `json:"lost,omitempty" jsonschema:"Output overwritten before this read."`
	ptyStatus
}

type ptyOutputGap struct {
	From uint64 `json:"from" jsonschema:"First unavailable offset."`
	To   uint64 `json:"to" jsonschema:"First available offset, exclusive end of the lost range."`
}

type ptyWriteResult struct {
	AppliedSequence uint64 `json:"applied_sequence"`
	Duplicate       bool   `json:"duplicate"`
}

type ptyStopResult struct {
	SignalSent bool `json:"signal_sent" jsonschema:"Cua accepted the termination signal. Use pty_read for exit status."`
	ptyStatus
}

func registerPTY(server *mcp.Server, cfg *config) func() error {
	session := newPTYSession(cfg)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "pty_start",
		Description: "Start a detached PTY with stdin on a configured computer. Retry an uncertain start with the same operation_id, argv and cwd.",
	}, ptyTool(session, func(ctx context.Context, args ptyStartInput) (ptyStartResult, error) {
		target, err := cfg.resolveComputer(args.ComputerID)
		if err != nil {
			return ptyStartResult{}, err
		}
		if strings.TrimSpace(args.OperationID) == "" || len(args.OperationID) > 256 {
			return ptyStartResult{}, errors.New("operation_id must be non-blank and at most 256 bytes")
		}
		if len(args.Argv) == 0 || args.Argv[0] == "" {
			return ptyStartResult{}, errors.New("argv must begin with a command")
		}
		return session.start(ctx, target, args)
	}))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "pty_read",
		Description: "Read PTY output and process status from a byte cursor. Reuse the same cursor on a failed call and use next_cursor after success.",
	}, ptyTool(session, func(ctx context.Context, args ptyReadInput) (ptyReadResult, error) {
		process, err := session.process(args.Handle)
		if err != nil {
			return ptyReadResult{}, err
		}
		wait, limit := 1000, 16*1024
		if args.WaitMS != nil {
			wait = *args.WaitMS
		}
		if args.MaxBytes != nil {
			limit = *args.MaxBytes
		}
		if wait < 0 || wait > maxPTYWaitMillis || limit < 1 || limit > maxPTYReadBytes {
			return ptyReadResult{}, errors.New("wait_ms must be 0 to 30000 and max_bytes must be 1 to 65536")
		}
		return process.read(ctx, args.Cursor, time.Duration(wait)*time.Millisecond, limit)
	}))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "pty_write",
		Description: "Write bytes to the PTY. Start at sequence 1 and retry an uncertain write with the same sequence and bytes.",
	}, ptyTool(session, func(ctx context.Context, args ptyWriteInput) (ptyWriteResult, error) {
		process, err := session.process(args.Handle)
		if err != nil {
			return ptyWriteResult{}, err
		}
		if args.Sequence == 0 {
			return ptyWriteResult{}, errors.New("sequence must be positive")
		}
		var data []byte
		switch {
		case args.Text != nil && args.DataBase64 == nil:
			data = []byte(*args.Text)
		case args.Text == nil && args.DataBase64 != nil:
			data, err = base64.StdEncoding.DecodeString(*args.DataBase64)
			if err != nil {
				return ptyWriteResult{}, errors.New("data_base64 must contain valid standard Base64")
			}
		default:
			return ptyWriteResult{}, errors.New("specify either text or data_base64")
		}
		if len(data) == 0 {
			return ptyWriteResult{}, errors.New("PTY input must contain at least one byte")
		}
		return process.write(ctx, args.Sequence, data)
	}))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "pty_stop",
		Description: "Request termination of the PTY process group. Use pty_read to observe the exit status.",
	}, ptyTool(session, func(ctx context.Context, args ptyHandleInput) (ptyStopResult, error) {
		process, err := session.process(args.Handle)
		if err != nil {
			return ptyStopResult{}, err
		}
		return process.stop(ctx)
	}))
	return session.close
}

func ptyTool[In, Out any](session *ptySession, call func(context.Context, In) (Out, error)) mcp.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args In) (*mcp.CallToolResult, Out, error) {
		ctx, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(session.lifetime, cancel)
		defer cancel()
		defer stop()
		out, err := call(ctx, args)
		return nil, out, err
	}
}
