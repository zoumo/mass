// Package replaybridge runs test-only protocol adapters in front of strict Testify launchers.
package replaybridge

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
)

func Relay(launcher []string, receiptPath string, stderr io.Writer, forwardInput func(io.Writer) error, forwardOutput func(io.Reader) error) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, launcher[0], launcher[1:]...)
	cmd.Stderr = stderr
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	defer output.Close()
	if err := cmd.Start(); err != nil {
		return err
	}

	forwarded := make(chan error, 1)
	go func() {
		forwarded <- forwardInput(input)
		_ = input.Close()
	}()
	outputErr := forwardOutput(output)
	if outputErr != nil {
		cancel()
	}
	waitErr := cmd.Wait()
	select {
	case err := <-forwarded:
		if err := errors.Join(err, outputErr, waitErr); err != nil {
			return err
		}
	default:
		return errors.Join(outputErr, waitErr, errors.New("mock launcher exited before reaching client EOF"))
	}

	// The child can receive an ordinary EOF when this adapter rejects a frame.
	// Write separate proof only when both directions and the child complete.
	receipt, err := os.OpenFile(receiptPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := receipt.WriteString("complete\n")
	return errors.Join(writeErr, receipt.Close())
}

func ProtocolLines(source io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(source)
	scanner.Buffer(make([]byte, 4096), 16<<20)
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if index := bytes.IndexByte(data, '\n'); index >= 0 {
			return index + 1, data[:index], nil
		}
		if atEOF && len(data) != 0 {
			return 0, nil, io.ErrUnexpectedEOF
		}
		return 0, nil, nil
	})
	return scanner
}
