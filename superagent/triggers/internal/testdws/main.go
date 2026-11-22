// testdws is an executable-boundary fixture. It never connects to DingTalk.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func main() {
	args := strings.Join(os.Args[1:], " ")
	if args == "--version" {
		fmt.Println("dws version v1.0.64 (test executable)")
		return
	}
	if args == "event consume --help" {
		fmt.Println("--flatten --profile --open-dingtalk-id --group --format")
		return
	}
	if len(os.Args) > 2 && os.Args[1] == "chat" {
		egress(os.Args[2:])
		return
	}
	if len(os.Args) < 4 || os.Args[1] != "event" || os.Args[2] != "consume" {
		fmt.Fprintln(os.Stderr, "Error: unexpected command")
		os.Exit(2)
	}
	eventKey := os.Args[3]
	command := os.Args[4:]
	path := os.Getenv("DWS_FIXTURE_INPUT")
	mode := os.Getenv("DWS_FIXTURE_MODE")
	selector := ""
	switch eventKey {
	case "user_im_message_receive_o2o_all":
		selector = "direct"
	case "user_im_message_receive_o2o":
		if flag(command, "--open-dingtalk-id") == "self" || mode == "shared" {
			selector = "self"
		}
	case "user_im_message_receive_group":
		selector = flag(command, "--group")
	}
	if selector == "" || flag(command, "--profile") != "corp:account" || flag(command, "--format") != "ndjson" || !strings.Contains(args, " --flatten ") {
		fmt.Fprintln(os.Stderr, "Error: unexpected arguments")
		os.Exit(2)
	}
	if mode == "multi" {
		path = filepath.Join(path, selector+".ndjson")
	}
	if pidPath := os.Getenv("DWS_FIXTURE_PID"); pidPath != "" {
		_ = os.WriteFile(pidPath, fmt.Append(nil, os.Getpid()), 0o600)
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	eof := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, os.Stdin); close(eof) }()
	if mode == "shared" {
		<-stop
		return
	}
	if mode != "not-ready" {
		fmt.Fprintf(os.Stderr, "[event] ready event_key=%s bus_pid=999999 subscribe_id=fixture\n", eventKey)
	}
	if mode == "feed" {
		feed(os.Getenv("DWS_FIXTURE_INPUT"), stop, eof)
		return
	}
	if path != "" {
		for mode == "await-file" {
			if _, err := os.Stat(path); err == nil {
				break
			}
			select {
			case <-stop:
				return
			case <-eof:
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
		f, err := os.Open(path)
		if err != nil {
			os.Exit(2)
		}
		scan := bufio.NewScanner(f)
		for scan.Scan() {
			select {
			case <-stop:
				_ = f.Close()
				return
			case <-eof:
				_ = f.Close()
				return
			case <-time.After(10 * time.Millisecond):
			}
			fmt.Println(scan.Text())
		}
		_ = f.Close()
	}
	if mode == "disconnect" {
		fmt.Fprintln(os.Stderr, "[event] exited — received 0 event(s) (reason: bus_shutdown)")
		return
	}
	select {
	case <-stop:
	case <-eof:
	}
}

func feed(dir string, stop <-chan os.Signal, eof <-chan struct{}) {
	for next := 1; ; {
		body, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("event-%03d.json", next)))
		if err == nil {
			fmt.Println(string(body))
			next++
		}
		select {
		case <-stop:
			return
		case <-eof:
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func flag(args []string, name string) string {
	for i, arg := range args {
		if arg == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func egress(args []string) {
	if args[len(args)-1] == "--help" {
		fmt.Println("--open-dingtalk-id --content --idempotency-key --profile --format --open-task-id --message-id --ref-sender --group --yes --conversation-id --msg-id --msg-ids --emotion-id --old-emotion-id --emotion-name --text --background-id")
		return
	}
	dir := os.Getenv("DWS_FIXTURE_EGRESS")
	if dir == "" || flag(args, "--profile") != "corp:account" || flag(args, "--format") != "json" {
		os.Exit(2)
	}
	if args[0] == "+messages-add-text-emotion" || args[0] == "message" && (args[1] == "update-text-emotion" || args[1] == "list-emotion-replies") {
		reaction(dir, args)
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "calls.ndjson"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		os.Exit(2)
	}
	err = json.NewEncoder(f).Encode(args)
	_ = f.Close()
	if err != nil {
		os.Exit(2)
	}
	switch args[0] {
	case "+messages-query-send-status":
		task := flag(args, "--open-task-id")
		if !strings.HasPrefix(task, "task-send_") || filepath.Base(task) != task {
			os.Exit(2)
		}
		if body, err := os.ReadFile(filepath.Join(dir, task+".json")); err == nil {
			fmt.Println(string(body))
		} else {
			writeResult(map[string]any{"success": true, "openTaskId": task, "result": map[string]string{"sendStatus": "PENDING"}})
		}
	case "+messages-reply":
		attempt := flag(args, "--idempotency-key")
		if !strings.HasPrefix(attempt, "send_") || flag(args, "--group") == "" || flag(args, "--message-id") == "" ||
			flag(args, "--ref-sender") == "" || flag(args, "--content") == "" {
			os.Exit(2)
		}
		if body, err := os.ReadFile(filepath.Join(dir, "send-result.json")); err == nil {
			fmt.Println(string(body))
			return
		}
		writeResult(map[string]any{
			"ok": true, "result": map[string]any{"success": true, "result": map[string]string{"openTaskId": "task-" + attempt}},
			"sendReceipt": map[string]string{"openTaskId": "task-" + attempt},
		})
	default:
		os.Exit(2)
	}
}

func reaction(dir string, args []string) {
	read := flag(args, "--msg-ids")
	message := read
	if message == "" {
		message = flag(args, "--msg-id")
	}
	if message == "" {
		message = flag(args, "--message-id")
	}
	if message == "" || filepath.Base(message) != message {
		os.Exit(2)
	}
	path := filepath.Join(dir, "reaction-"+message+".json")
	if read != "" {
		emotions := []map[string]any{}
		if raw, err := os.ReadFile(path); err == nil {
			if json.Unmarshal(raw, &emotions) != nil {
				os.Exit(2)
			}
		}
		writeResult(map[string]any{"success": true, "result": map[string]any{
			"messages": []map[string]any{{"openMessageId": message, "emotionReplyList": emotions}},
		}})
		return
	}
	if args[0] == "message" && flag(args, "--old-emotion-id") != "thinking-id" {
		os.Exit(2)
	}
	if _, err := os.Stat(filepath.Join(dir, "block-reaction-"+message)); err == nil {
		os.Exit(1)
	}
	emotions := []map[string]any{{"emotionId": flag(args, "--emotion-id"), "backgroundId": flag(args, "--background-id")}}
	payload, err := json.Marshal(emotions)
	if err != nil || os.WriteFile(path+".next", payload, 0o600) != nil {
		os.Exit(2)
	}
	if err := os.Rename(path+".next", path); err != nil {
		os.Exit(2)
	}
	// The write applied, but the CLI acknowledgement was lost.
	os.Exit(1)
}

func writeResult(result any) {
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		os.Exit(2)
	}
}
