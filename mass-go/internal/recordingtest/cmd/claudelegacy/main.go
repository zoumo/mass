// claudelegacy checks the live SDK's UUID before replaying historical Claude
// consumer recordings whose user frame predates that field.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/zoumo/mass/mass-go/internal/testpeer/rawjson"
	"github.com/zoumo/mass/mass-go/internal/testpeer/replay"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "Claude consumer replay:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("missing Testify launcher")
	}
	cmd := exec.CommandContext(context.Background(), args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	forwarded := make(chan error, 1)
	go func() {
		forwarded <- forwardLegacyInput(os.Stdin, input)
		_ = input.Close()
	}()
	waitErr := cmd.Wait()
	select {
	case err := <-forwarded:
		return errors.Join(err, waitErr)
	default:
		return errors.Join(waitErr, errors.New("mock launcher exited before reaching client EOF"))
	}
}

func forwardLegacyInput(source io.Reader, destination io.Writer) error {
	input := bufio.NewScanner(source)
	input.Buffer(make([]byte, 4096), 16<<20)
	userSeen := false
	for input.Scan() {
		frame, err := rawjson.Parse(input.Bytes())
		if err != nil || frame.Kind() != rawjson.Object {
			return errors.New("expected a Claude JSON object")
		}
		kind, _ := frame.Member("type")
		name, _ := kind.String()
		line := input.Bytes()
		if name == "user" {
			if userSeen {
				return errors.New("legacy recording expects one user input")
			}
			var err error
			line, err = removeCheckedUserUUID(frame)
			if err != nil {
				return err
			}
			userSeen = true
		}
		if _, err := fmt.Fprintf(destination, "%s\n", line); err != nil {
			return err
		}
	}
	if err := input.Err(); err != nil {
		return err
	}
	if !userSeen {
		return errors.New("missing Claude user input")
	}
	return nil
}

func removeCheckedUserUUID(frame rawjson.Value) ([]byte, error) {
	member, _ := frame.Member("uuid")
	value, _ := member.String()
	if !replay.ValidClaudeUserUUID(value) {
		return nil, errors.New("user input requires a canonical v4 UUID")
	}
	fields := make(map[string]json.RawMessage, len(frame.Keys())-1)
	for _, key := range frame.Keys() {
		if key != "uuid" {
			member, _ := frame.Member(key)
			fields[key] = member.Raw()
		}
	}
	return json.Marshal(fields)
}
