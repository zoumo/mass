// Command testpeer is MASS's test-only strict protocol peer. It replays an
// explicit authored scenario and never launches a real agent or tool.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	nativeclaude "github.com/zoumo/agent-sdk-go/claude"

	"github.com/zoumo/mass/mass-go/internal/testpeer/replay"
	"github.com/zoumo/mass/mass-go/internal/testpeer/scenario"
)

const (
	exitOK            = 0
	exitFailure       = 1
	exitConfiguration = 2
	exitCanceled      = 3
	exitDisconnect    = 4
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	var native []string
	for i, arg := range args {
		if arg == "--" {
			native = args[i+1:]
			args = args[:i]
			break
		}
	}
	for _, arg := range native {
		if arg == "--version" {
			fmt.Fprintln(os.Stdout, nativeclaude.MinimumCLIVersion+" (Claude Code)")
			return exitOK
		}
	}
	assignedSessionID := ""
	claimSession := false
	for _, arg := range native {
		if value, ok := strings.CutPrefix(arg, "--session-id="); ok {
			assignedSessionID = value
		}
		if arg == "--await-claim" {
			claimSession = true
		}
	}
	if claimSession {
		// The synthetic CLI assigns the identity a real spare would obtain from
		// claim_session; scripted session_id and cwd placeholders follow that
		// binding.
		assignedSessionID = uuid.NewString()
	}

	fs := flag.NewFlagSet("mass-testpeer", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	provider := fs.String("provider", "", "native protocol adapter: codex or claude")
	source := fs.String("scenario", "", "explicit authored scenario file")
	receipt := fs.String("receipt", "", "new success receipt file")
	if err := fs.Parse(args); err != nil {
		return exitConfiguration
	}
	limits, err := scenario.DefaultLimits(*provider)
	if err != nil || *source == "" {
		fmt.Fprintln(os.Stderr, "mass-testpeer: provider codex|claude and --scenario FILE are required")
		return exitConfiguration
	}
	document, err := readScenario(*source, *provider, limits)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mass-testpeer:", err)
		return exitConfiguration
	}
	if *receipt != "" {
		if _, err = os.Lstat(*receipt); err == nil {
			fmt.Fprintln(os.Stderr, "mass-testpeer: receipt already exists")
			return exitConfiguration
		} else if !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(os.Stderr, "mass-testpeer:", err)
			return exitConfiguration
		}
	}

	timeout := document.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
		if *provider == "claude" {
			timeout = 10 * time.Second
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	deadline := time.Now().Add(timeout)
	_ = os.Stdin.SetReadDeadline(deadline)
	_ = os.Stdout.SetWriteDeadline(deadline)

	if claimSession {
		cwd, cwdErr := os.Getwd()
		if cwdErr != nil {
			fmt.Fprintln(os.Stderr, "mass-testpeer:", cwdErr)
			return exitConfiguration
		}
		err = replay.RunClaim(ctx, document, limits, os.Stdin, os.Stdout, assignedSessionID, cwd)
	} else {
		err = replay.Run(ctx, document, limits, os.Stdin, os.Stdout, assignedSessionID)
	}
	if err == nil {
		if *receipt != "" {
			err = writeReceipt(ctx, *receipt, *provider, document)
		}
		if err == nil {
			return exitOK
		}
	}
	fmt.Fprintln(os.Stderr, "mass-testpeer:", err)
	switch {
	case errors.Is(err, replay.ErrDisconnected):
		return exitDisconnect
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled), errors.Is(err, os.ErrDeadlineExceeded):
		return exitCanceled
	default:
		return exitFailure
	}
}

func readScenario(path, provider string, limits scenario.Limits) (*scenario.Document, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	document, readErr := scenario.Read(f, provider, limits)
	return document, errors.Join(readErr, f.Close())
}

type receipt struct {
	Version         int    `json:"version"`
	FullConsumption bool   `json:"fullConsumption"`
	NativeLaunched  bool   `json:"nativeLaunched"`
	Peer            string `json:"peer"`
	Provider        string `json:"provider"`
	Frames          int    `json:"frames"`
	RecordingSHA256 string `json:"recordingSHA256"`
	ClientEOF       bool   `json:"clientEOF"`
}

func writeReceipt(ctx context.Context, path, provider string, document *scenario.Document) error {
	data, err := json.MarshalIndent(receipt{
		Version: 1, FullConsumption: true, NativeLaunched: false,
		Peer: "MASS strict scenario peer", Provider: provider,
		Frames: len(document.Frames), RecordingSHA256: document.SHA256, ClientEOF: true,
	}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	n, writeErr := f.Write(data)
	if writeErr == nil && n != len(data) {
		writeErr = io.ErrShortWrite
	}
	err = errors.Join(writeErr, f.Close(), ctx.Err())
	if err != nil {
		return errors.Join(err, os.Remove(path))
	}
	return nil
}
