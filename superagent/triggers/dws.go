package triggers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	directEvent = "user_im_message_receive_o2o_all"
	selfEvent   = "user_im_message_receive_o2o"
	groupEvent  = "user_im_message_receive_group"
)

type subscription struct {
	eventKey string
	group    *groupConfig
}

func (c Config) subscriptions() []subscription {
	var targets []subscription
	if c.Direct.Enabled {
		targets = append(targets, subscription{eventKey: directEvent})
	} else if c.SelfChat.Enabled {
		targets = append(targets, subscription{eventKey: selfEvent})
	}
	for i := range c.Groups {
		targets = append(targets, subscription{eventKey: groupEvent, group: &c.Groups[i]})
	}
	return targets
}

type input struct {
	kind, reason string
	consumer     int
	line         []byte
}

func preflight(ctx context.Context, c Config) (string, error) {
	run := func(args ...string) (string, error) {
		sub, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(sub, c.DWS, args...)
		cmd.Env = dwsEnvironment(c)
		var out limitedOutput
		cmd.Stdout = &out
		cmd.Stderr = &out
		err := cmd.Run()
		return out.String(), err
	}
	version, err := run("--version")
	if err != nil || !strings.Contains(version, "dws version v1.") {
		return "", errors.New("DWS executable/version check failed")
	}
	help, err := run("event", "consume", "--help")
	if err != nil {
		return "", errors.New("DWS event consume contract unavailable")
	}
	consumeFlags := []string{"--flatten", "--profile", "--format"}
	if c.SelfChat.Enabled && !c.Direct.Enabled {
		consumeFlags = append(consumeFlags, "--open-dingtalk-id")
	}
	if len(c.Groups) > 0 {
		consumeFlags = append(consumeFlags, "--group")
	}
	for _, flag := range consumeFlags {
		if !strings.Contains(help, flag) {
			return "", errors.New("DWS event consume contract drift")
		}
	}
	if !c.repliesEnabled() {
		return strings.Split(version, "\n")[0], nil
	}
	type contract struct {
		command []string
		flags   []string
	}
	checks := []contract{
		{[]string{"chat", "+messages-reply"}, []string{"--message-id", "--ref-sender", "--open-dingtalk-id", "--group", "--content", "--idempotency-key", "--yes", "--profile", "--format"}},
		{[]string{"chat", "+messages-query-send-status"}, []string{"--open-task-id", "--profile", "--format"}},
	}
	if c.Feedback != nil {
		checks = append(checks,
			contract{[]string{"chat", "+messages-add-text-emotion"}, []string{"--conversation-id", "--msg-id", "--emotion-id", "--emotion-name", "--text", "--background-id", "--yes"}},
			contract{[]string{"chat", "message", "update-text-emotion"}, []string{"--conversation-id", "--message-id", "--old-emotion-id", "--emotion-id", "--emotion-name", "--text", "--background-id"}},
			contract{[]string{"chat", "message", "list-emotion-replies"}, []string{"--msg-ids"}},
		)
	}
	for _, check := range checks {
		help, err := run(append(check.command, "--help")...)
		if err != nil {
			return "", errors.New("DWS egress command unavailable")
		}
		for _, flag := range check.flags {
			if !strings.Contains(help, flag) {
				return "", errors.New("DWS egress command contract drift")
			}
		}
	}
	// Only the first version line is retained; no stderr/auth output is forwarded.
	return strings.Split(version, "\n")[0], nil
}

type limitedOutput struct {
	bytes.Buffer
	truncated bool
}

func (b *limitedOutput) Write(p []byte) (int, error) {
	n := len(p)
	b.truncated = b.truncated || n > 32768-b.Len()
	if b.Len() < 32768 {
		remain := 32768 - b.Len()
		if len(p) > remain {
			p = p[:remain]
		}
		_, _ = b.Buffer.Write(p)
	}
	return n, nil
}

type dwsResult struct {
	TaskID         string `json:"openTaskId"`
	SendStatus     string `json:"sendStatus"`
	DeliveryStatus string `json:"deliveryStatus"`
	ConversationID string `json:"openConversationId"`
	MessageID      string `json:"openMessageId"`
	ReplyChatID    string `json:"conversationId"`
	ReplyMessageID string `json:"messageId"`
	ErrorCode      string `json:"errorCode"`
	Retryable      *bool  `json:"retryable"`
	Success        *bool  `json:"success"`
	OK             *bool  `json:"ok"`
	Error          *struct {
		Code   string `json:"code"`
		Reason string `json:"reason"`
	} `json:"error"`
	Result      json.RawMessage `json:"result"`
	Data        json.RawMessage `json:"data"`
	SendReceipt *dwsResult      `json:"sendReceipt"`
	MessageRef  *dwsResult      `json:"messageRef"`
}

// Atomic sends wrap the downstream payload in data. Its result can be an
// array even when the payload already carries an openTaskId.
func nestedDWSResult(raw json.RawMessage) *dwsResult {
	var result *dwsResult
	if len(raw) == 0 || json.Unmarshal(raw, &result) != nil {
		return nil
	}
	return result
}

type channelOutcome struct {
	TaskID         string
	Outcome        string
	ErrorCode      string
	ConversationID string
	MessageID      string
}

func (r *dwsResult) outcome() channelOutcome {
	if r == nil {
		return channelOutcome{}
	}
	status := strings.ToUpper(r.SendStatus)
	if status == "" {
		status = strings.ToUpper(r.DeliveryStatus)
	}
	rejected := (r.Success != nil && !*r.Success) || (r.OK != nil && !*r.OK) || r.Error != nil
	if !rejected && (status == "SUCCESS" || status == "SENT") {
		outcome := channelOutcome{Outcome: "sent", ConversationID: r.ConversationID, MessageID: r.MessageID}
		if outcome.ConversationID == "" {
			outcome.ConversationID = r.ReplyChatID
		}
		if outcome.MessageID == "" {
			outcome.MessageID = r.ReplyMessageID
		}
		return outcome
	}
	for _, code := range []string{r.ErrorCode, r.errorReason(), status} {
		switch strings.ToLower(code) {
		case "permission_denied", "forbidden", "unauthorized", "authentication_required", "confirmation_required",
			"invalid_argument", "invalid_parameter", "target_not_found", "conversation_not_found", "user_not_found":
			return channelOutcome{Outcome: "permanent_failure", ErrorCode: "dws_send_rejected"}
		}
	}
	if status == "FAILED" || status == "FAIL" {
		if r.Retryable != nil && *r.Retryable {
			return channelOutcome{Outcome: "retryable_failure", ErrorCode: "dws_delivery_failed_retryable"}
		}
		return channelOutcome{Outcome: "permanent_failure", ErrorCode: "dws_delivery_failed"}
	}
	if rejected {
		return channelOutcome{}
	}
	if outcome := nestedDWSResult(r.Result).outcome(); outcome.Outcome != "" {
		return outcome
	}
	return nestedDWSResult(r.Data).outcome()
}

func (r *dwsResult) task() string {
	if r == nil || (r.OK != nil && !*r.OK) || (r.Success != nil && !*r.Success) || r.Error != nil {
		return ""
	}
	if strings.TrimSpace(r.TaskID) != "" {
		return r.TaskID
	}
	for _, nested := range []*dwsResult{nestedDWSResult(r.Result), r.SendReceipt, nestedDWSResult(r.Data)} {
		if task := nested.task(); task != "" {
			return task
		}
	}
	return ""
}

func (r *dwsResult) errorReason() string {
	if r.Error == nil {
		return ""
	}
	if r.Error.Reason != "" {
		return r.Error.Reason
	}
	return r.Error.Code
}

func dwsCall(ctx context.Context, c Config, args ...string) (dwsResult, bool) {
	callCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	args = append(args, "--profile", c.Profile, "--format", "json")
	cmd := exec.CommandContext(callCtx, c.DWS, args...)
	cmd.Env = dwsEnvironment(c)
	cmd.WaitDelay = time.Second
	var out, stderr limitedOutput
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	var result dwsResult
	if !out.truncated && json.Unmarshal(out.Bytes(), &result) == nil {
		return result, err == nil
	}
	if err != nil && !stderr.truncated {
		var failure dwsResult
		if json.Unmarshal(stderr.Bytes(), &failure) == nil {
			return failure, false
		}
	}
	return dwsResult{}, false
}

func sendDWS(ctx context.Context, c Config, channel Channel, sendKey, peer, text string) channelOutcome {
	args := []string{"chat", "+messages-reply"}
	if peer != "" {
		args = append(args, "--open-dingtalk-id", peer)
	}
	args = append(args, "--group", channel.ConversationID, "--message-id", channel.MessageID, "--ref-sender", channel.ActorID, "--yes")
	result, ok := dwsCall(ctx, c, append(args, "--content", text, "--idempotency-key", sendKey)...)
	outcome := result.outcome()
	if task := result.task(); ok && task != "" && outcome.ErrorCode == "" {
		outcome.TaskID = task
	}
	if (!ok && outcome.Outcome != "permanent_failure" && outcome.Outcome != "retryable_failure") ||
		(outcome.Outcome == "" && strings.TrimSpace(outcome.TaskID) == "") {
		return channelOutcome{Outcome: "unknown", ErrorCode: "dws_send_outcome_unknown"}
	}
	return result.withMessageRef(outcome)
}

func queryDWS(ctx context.Context, c Config, taskID string) channelOutcome {
	result, ok := dwsCall(ctx, c, "chat", "+messages-query-send-status", "--open-task-id", taskID)
	// An explicit missing/expired task permits a fresh send. Transport and
	// authorization errors leave the original task available for later queries.
	switch strings.ToLower(result.errorReason()) {
	case "task_not_found", "open_task_not_found", "task_expired":
		if result.TaskID == "" || result.TaskID == taskID {
			return channelOutcome{Outcome: "unknown", ErrorCode: "dws_task_unavailable"}
		}
	}
	if !ok || result.TaskID != taskID {
		return channelOutcome{}
	}
	outcome := result.outcome()
	if outcome.ErrorCode == "dws_send_rejected" {
		return channelOutcome{}
	}
	return result.withMessageRef(outcome)
}

func (r *dwsResult) withMessageRef(outcome channelOutcome) channelOutcome {
	if outcome.Outcome == "sent" && r.MessageRef != nil {
		if outcome.ConversationID == "" {
			outcome.ConversationID = r.MessageRef.ConversationID
		}
		if outcome.MessageID == "" {
			outcome.MessageID = r.MessageRef.MessageID
		}
	}
	return outcome
}

func consume(ctx context.Context, c Config, index int, target subscription, out chan<- input, wg *sync.WaitGroup) {
	defer wg.Done()
	send := func(v input) bool {
		select {
		case out <- v:
			return true
		case <-ctx.Done():
			return false
		}
	}
	args := []string{"event", "consume", target.eventKey}
	switch target.eventKey {
	case selfEvent:
		args = append(args, "--open-dingtalk-id", c.SelfID)
	case groupEvent:
		args = append(args, "--group", target.group.ConversationID)
	}
	args = append(args, "--profile", c.Profile, "--flatten", "--format", "ndjson")
	// Cancellation closes stdin and sends SIGTERM so DWS can confirm shutdown.
	cmd := exec.Command(c.DWS, args...) //nolint:noctx
	cmd.Env = dwsEnvironment(c)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		send(input{kind: "exit", consumer: index, reason: "consumer_pipe_failed"})
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		send(input{kind: "exit", consumer: index, reason: "consumer_pipe_failed"})
		return
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		send(input{kind: "exit", consumer: index, reason: "consumer_pipe_failed"})
		return
	}
	if err = cmd.Start(); err != nil {
		send(input{kind: "exit", consumer: index, reason: "consumer_start_failed"})
		return
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = stdin.Close()
			_ = cmd.Process.Signal(syscall.SIGTERM)
		case <-done:
		}
	}()
	var readers sync.WaitGroup
	readers.Add(2)
	ready := make(chan struct{})
	var readyOnce sync.Once
	go func() {
		defer readers.Done()
		scan := bufio.NewScanner(stderr)
		scan.Buffer(make([]byte, 4096), 1<<20)
		for scan.Scan() {
			line := scan.Text()
			if strings.HasPrefix(line, "[event] ready ") && strings.Contains(" "+line+" ", " event_key="+target.eventKey+" ") {
				if !send(input{kind: "ready", consumer: index}) {
					return
				}
				readyOnce.Do(func() { close(ready) })
			} else if strings.Contains(line, "bus_shutdown") || strings.HasPrefix(line, "Error:") {
				if !send(input{kind: "disconnected", consumer: index, reason: "dws_connection_error"}) {
					return
				}
			}
		}
		if scan.Err() != nil {
			send(input{kind: "disconnected", consumer: index, reason: "dws_stderr_invalid"})
		}
	}()
	go func() {
		defer readers.Done()
		scan := bufio.NewScanner(stdout)
		scan.Buffer(make([]byte, 4096), 1<<20)
		for scan.Scan() {
			select {
			case <-ready:
			case <-ctx.Done():
				return
			}
			if !send(input{kind: "message", consumer: index, line: append([]byte(nil), scan.Bytes()...)}) {
				return
			}
		}
		if scan.Err() != nil {
			send(input{kind: "disconnected", consumer: index, reason: "dws_ndjson_too_large_or_unreadable"})
		}
	}()
	readers.Wait()
	err = cmd.Wait()
	close(done)
	_ = stdin.Close()
	if ctx.Err() == nil {
		reason := "consumer_exited"
		if err != nil {
			reason = "consumer_failed"
		}
		send(input{kind: "exit", consumer: index, reason: reason})
	}
}

func dwsEnvironment(c Config) []string {
	var env []string
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, c.TokenEnv+"=") {
			env = append(env, item)
		}
	}
	return env
}
