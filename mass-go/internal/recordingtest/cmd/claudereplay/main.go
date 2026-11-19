// claudereplay binds the generated UUID of a single live Claude user frame to
// the UUID expected by a strict Testify replay of a consumer recording.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync/atomic"

	"github.com/zoumo/mass/mass-go/internal/testpeer/rawjson"
	"github.com/zoumo/mass/mass-go/internal/testpeer/replay"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "Claude consumer replay:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) < 3 {
		return errors.New("expected captured UUID, receipt path and Testify launcher")
	}
	binding := submissionBinding{captured: args[0]}
	if binding.captured != "" && !replay.ValidClaudeUserUUID(binding.captured) {
		return errors.New("capture requires a canonical v4 UUID")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, args[2], args[3:]...)
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
		forwarded <- binding.forwardInput(stdin, input)
		_ = input.Close()
	}()
	outputErr := binding.forwardOutput(output, stdout)
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

	// Testify can see an ordinary EOF when this adapter rejects a client frame.
	// Write separate success evidence only after both directions and the child complete.
	receipt, err := os.OpenFile(args[1], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := receipt.WriteString("complete\n")
	return errors.Join(writeErr, receipt.Close())
}

type submissionBinding struct {
	captured string
	live     atomic.Pointer[rawjson.Value]
}

func (b *submissionBinding) forwardInput(source io.Reader, destination io.Writer) error {
	input := protocolLines(source)
	for input.Scan() {
		frame, err := parseFrame(input.Bytes())
		if err != nil {
			return err
		}
		line := input.Bytes()
		if memberString(frame, "type") == "user" {
			if b.live.Load() != nil {
				return errors.New("consumer recording expects one user input")
			}
			line, err = b.bindUser(frame)
			if err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(destination, "%s\n", line); err != nil {
			return err
		}
	}
	if err := input.Err(); err != nil {
		return err
	}
	if b.live.Load() == nil {
		return errors.New("missing Claude user input")
	}
	return nil
}

func (b *submissionBinding) bindUser(frame rawjson.Value) ([]byte, error) {
	live, _ := frame.Member("uuid")
	value, _ := live.String()
	if !replay.ValidClaudeUserUUID(value) {
		return nil, errors.New("user input requires a canonical v4 UUID")
	}

	var line []byte
	var err error
	if b.captured == "" {
		line, err = withoutUUID(frame)
	} else {
		captured, parseErr := rawjson.Parse([]byte(strconv.Quote(b.captured)))
		if parseErr != nil {
			return nil, parseErr
		}
		var mapped rawjson.Value
		mapped, err = frame.Replace([]string{"uuid"}, captured)
		line = mapped.Raw()
	}
	if err != nil {
		return nil, err
	}
	// Publish the binding before the child can receive and answer the user frame.
	b.live.Store(&live)
	return line, nil
}

func withoutUUID(frame rawjson.Value) ([]byte, error) {
	fields := make(map[string]json.RawMessage, len(frame.Keys())-1)
	for _, key := range frame.Keys() {
		if key != "uuid" {
			member, _ := frame.Member(key)
			fields[key] = member.Raw()
		}
	}
	return json.Marshal(fields)
}

func (b *submissionBinding) forwardOutput(source io.Reader, destination io.Writer) error {
	if b.captured == "" {
		_, err := io.Copy(destination, source)
		return err
	}
	output := protocolLines(source)
	for output.Scan() {
		frame, err := parseFrame(output.Bytes())
		if err != nil {
			return err
		}
		frame, err = b.mapServerUUID(frame, "user_message_uuid")
		if err != nil {
			return err
		}
		// Claude may echo the submitted user message with the same envelope UUID.
		// Assistant and result envelope UUIDs identify their own native events.
		if memberString(frame, "type") == "user" {
			frame, err = b.mapServerUUID(frame, "uuid")
			if err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(destination, "%s\n", frame.Raw()); err != nil {
			return err
		}
	}
	return output.Err()
}

func (b *submissionBinding) mapServerUUID(frame rawjson.Value, field string) (rawjson.Value, error) {
	if memberString(frame, field) != b.captured {
		return frame, nil
	}
	live := b.live.Load()
	if live == nil {
		return rawjson.Value{}, errors.New("server refers to captured user UUID before user input")
	}
	return frame.Replace([]string{field}, *live)
}

func memberString(frame rawjson.Value, field string) string {
	member, _ := frame.Member(field)
	value, _ := member.String()
	return value
}

func parseFrame(line []byte) (rawjson.Value, error) {
	frame, err := rawjson.Parse(line)
	if err != nil || frame.Kind() != rawjson.Object {
		return rawjson.Value{}, errors.New("expected a Claude JSON object")
	}
	return frame, nil
}

func protocolLines(source io.Reader) *bufio.Scanner {
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
