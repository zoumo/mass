package core_test

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/superagent/internal/triggeridentity"
	"github.com/zoumo/mass/superagent/pkg/core"
	"github.com/zoumo/mass/superagent/web"
)

var (
	binary    string
	buildErr  error
	buildOnce sync.Once
)

func build(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		tags := "coretest"
		if web.Assets() != nil {
			tags += ",superagentweb"
		}
		cmd := exec.CommandContext(context.Background(), "go", "build", "-race", "-tags", tags, "-o", binary, "../../../cmd/superagent")
		cmd.Env = append(os.Environ(), "GOPROXY=off", "GOSUMDB=off")
		if b, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("build: %w: %s", err, b)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return binary
}

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "superagent-build-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binary = filepath.Join(dir, "superagent")
	code := m.Run()
	if binary != "" {
		_ = os.RemoveAll(filepath.Dir(binary))
	}
	os.Exit(code)
}

func TestObserveCoreResourceCLI(t *testing.T) {
	f := fixtureAt(t)
	p := f.start()
	message := input("cli-resource")
	submit(t, p, f, message)

	run := func(args ...string) []byte {
		t.Helper()
		args = append([]string{"observe", "--endpoint", p.endpoint}, args...)
		out, err := exec.CommandContext(t.Context(), build(t), args...).CombinedOutput()
		require.NoError(t, err, "%s", out)
		return out
	}
	var status struct {
		Core     core.CoreStatus      `json:"core"`
		Massd    core.ComponentStatus `json:"massd"`
		Triggers core.TriggerStatuses `json:"triggers"`
	}
	require.NoError(t, json.Unmarshal(run("status"), &status))
	require.Equal(t, "ready", status.Core.Status)
	require.Equal(t, "ready", status.Massd.Status)
	require.Equal(t, "not_managed", status.Triggers.GitHub.Status)
	var messages core.Page
	require.NoError(t, json.Unmarshal(run("trigger-messages", "--thread-id", message.Message.ThreadID, "--limit", "1"), &messages))
	require.Len(t, messages.Items, 1)
	require.Contains(t, string(messages.Items[0]), "cli-resource")
	require.NoError(t, json.Unmarshal(run("trigger-messages", "--thread-id", "dws:unrelated"), &messages))
	require.Empty(t, messages.Items)

	help := string(run("--help"))
	for _, removed := range []string{"pull-requests", "subscriptions", "relations", "routing-events"} {
		require.NotContains(t, help, removed)
	}
}

type process struct {
	cmd           *exec.Cmd
	endpoint      string
	massdEndpoint string
	stderr        bytes.Buffer
	done          chan error
	stopped       bool
}
type fixture struct {
	t                        *testing.T
	dir, config, data, token string
	executable               string
}

func fixtureAt(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{t: t, dir: dir, config: filepath.Join(dir, "config.yaml"), data: filepath.Join(dir, "data"), token: strings.Repeat("a", 48)}
	f.configure(0)
	return f
}

const fixtureStableMCP = "externalURL: http://127.0.0.1:24370\n"

func (f *fixture) configure(pages int) {
	f.t.Helper()
	write(f.t, f.config, fmt.Sprintf("dataDir: %s\nlisten: 127.0.0.1:0\nstorageMaxPages: %d\n", f.data, pages)+fixtureStableMCP)
}

func (f *fixture) seedToken(name, token string) string {
	f.t.Helper()
	dir := filepath.Join(f.data, "credentials")
	require.NoError(f.t, os.MkdirAll(dir, 0o700))
	path := filepath.Join(dir, name)
	write(f.t, path, token)
	return path
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) coreExecutable() string {
	if f.executable != "" {
		return f.executable
	}
	return build(f.t)
}

func (f *fixture) start(env ...string) *process {
	f.t.Helper()
	if _, err := os.Stat(filepath.Join(f.data, "credentials", "triggers.token")); errors.Is(err, os.ErrNotExist) {
		f.seedToken("triggers.token", f.token)
	} else {
		require.NoError(f.t, err)
	}
	return f.startConfigured(env...)
}

func (f *fixture) startConfigured(env ...string) *process {
	f.t.Helper()
	return f.startCommand("serve", f.config, env...)
}

func (f *fixture) startCommand(command, config string, env ...string) *process {
	f.t.Helper()
	p := &process{done: make(chan error, 1)}
	p.cmd = exec.CommandContext(context.Background(), f.coreExecutable(), command, "--config", config)
	home := filepath.Join(f.dir, "empty-home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		f.t.Fatal(err)
	}
	p.cmd.Env = append([]string{"HOME=" + home, "PATH=" + home}, env...)
	p.cmd.Stderr = &p.stderr
	stdout, err := p.cmd.StdoutPipe()
	if err != nil {
		f.t.Fatal(err)
	}
	if err = p.cmd.Start(); err != nil {
		f.t.Fatal(err)
	}
	go func() { p.done <- p.cmd.Wait() }()
	line := make(chan []byte, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			line <- append([]byte(nil), scanner.Bytes()...)
		} else {
			line <- nil
		}
	}()
	select {
	case b := <-line:
		var info struct {
			Endpoint      string `json:"endpoint"`
			MassdEndpoint string `json:"massdEndpoint"`
		}
		if json.Unmarshal(b, &info) != nil || info.Endpoint == "" {
			_ = p.cmd.Process.Kill()
			waitErr := <-p.done
			f.t.Fatalf("startup failed: %v %s %s", waitErr, b, p.stderr.String())
		}
		p.endpoint = info.Endpoint
		p.massdEndpoint = info.MassdEndpoint
	case <-time.After(20 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
		f.t.Fatal("startup timeout")
	}
	f.t.Cleanup(func() {
		if !p.stopped {
			p.stop(f.t, false)
		}
	})
	return p
}

func (p *process) stop(t *testing.T, kill bool) {
	t.Helper()
	if p.stopped {
		return
	}
	var err error
	if kill {
		err = p.cmd.Process.Kill()
	} else {
		err = p.cmd.Process.Signal(syscall.SIGTERM)
	}
	if err != nil {
		t.Fatal(err)
	}
	p.waitStopped(t, kill)
}

func (p *process) waitStopped(t *testing.T, kill bool) {
	t.Helper()
	p.stopped = true
	select {
	case err := <-p.done:
		if !kill && err != nil {
			t.Fatalf("shutdown: %v %s", err, p.stderr.String())
		}
	case <-time.After(15 * time.Second):
		_ = p.cmd.Process.Kill()
		t.Fatal("process failed to exit")
	}
	client := http.Client{Timeout: 300 * time.Millisecond, Transport: &http.Transport{}}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, p.endpoint+"/status", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := client.Do(req); err == nil {
		_ = r.Body.Close()
		t.Fatal("listener still reachable after exit")
	}
	t.Logf("process released pid=%d forced=%t endpoint=%s", p.cmd.Process.Pid, kill, p.endpoint)
}

const fixtureConversation = "conversation:Case/%原文"

func fixtureThreadID(conversation string) string {
	return "dws:" + strings.ReplaceAll(url.PathEscape(conversation), ":", "%3A")
}

func moveInput(req *core.SubmitTriggerMessageRequest, conversation string) {
	req.Message.ThreadID = fixtureThreadID(conversation)
	req.ReplyTarget = json.RawMessage(fmt.Sprintf(`{"type":"dws","conversationId":%q,"messageId":%q,"actorId":"sender-1"}`, conversation, req.Message.SourceMessageID))
}

func input(id string) core.SubmitTriggerMessageRequest {
	req := core.SubmitTriggerMessageRequest{OperationID: "op-" + id, Message: core.TriggerMessage{
		SourceMessageID: id, Content: []events.ContentBlock{events.Text("Original input " + id)},
	}}
	moveInput(&req, fixtureConversation)
	return req
}

var fixtureTriggerSource = core.Source{Source: "dws", AccountID: "account-1"}

func setTriggerHeaders(req *http.Request, source core.Source) {
	if source.Source != "" {
		req.Header.Set(triggeridentity.SourceHeader, source.Source)
	}
	if source.AccountID != "" {
		req.Header.Set(triggeridentity.AccountHeader, source.AccountID)
	}
}

func request(t *testing.T, method, endpoint, path, token string, body any, sources ...core.Source) (int, []byte) {
	t.Helper()
	var b []byte
	if body != nil {
		var err error
		b, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequestWithContext(context.Background(), method, endpoint+path, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if len(sources) != 0 {
		setTriggerHeaders(req, sources[0])
	} else if token != "" && (req.URL.Path == "/apis/core/v1/trigger-messages" || strings.HasPrefix(req.URL.Path, channelOutbox)) {
		setTriggerHeaders(req, fixtureTriggerSource)
	}
	c := http.Client{Timeout: 10 * time.Second}
	r, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Body.Close() }()
	b, err = io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return r.StatusCode, b
}

func submit(t *testing.T, p *process, f *fixture, r core.SubmitTriggerMessageRequest) core.Receipt {
	t.Helper()
	status, b := request(t, "POST", p.endpoint, "/apis/core/v1/trigger-messages", f.token, r)
	if status != 200 {
		t.Fatalf("submit %d: %s", status, b)
	}
	var receipt core.Receipt
	if err := json.Unmarshal(b, &receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}

func page(t *testing.T, p *process, kind string) core.Page {
	t.Helper()
	status, b := request(t, "GET", p.endpoint, "/apis/core/v1/"+kind, "", nil)
	if status != 200 {
		t.Fatalf("query %d %s", status, b)
	}
	var result core.Page
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func errorCode(t *testing.T, status int, b []byte, wantStatus int, code string) {
	t.Helper()
	var result struct {
		Error core.Error `json:"error"`
	}
	if json.Unmarshal(b, &result) != nil || status != wantStatus || result.Error.Code != code {
		t.Fatalf("want %d/%s, got %d %s", wantStatus, code, status, b)
	}
}

func (f *fixture) fails(code string) {
	f.t.Helper()
	cmd := exec.CommandContext(context.Background(), build(f.t), "serve", "--config", f.config)
	cmd.Env = []string{"HOME=" + f.dir, "PATH=" + f.dir}
	out, err := cmd.CombinedOutput()
	if err == nil || !bytes.Contains(out, []byte(code)) {
		f.t.Fatalf("expected startup %s: %v %s", code, err, out)
	}
	f.t.Logf("startup refused with %s", code)
}

func TestPublicIngressDurability(t *testing.T) {
	f := fixtureAt(t)
	p := f.start()
	r := input("message-1")
	r.Message.Meta = json.RawMessage(`{"a":1,"b":"reference","quotedMessage":{"text":"quote"}}`)
	accepted := submit(t, p, f, r)
	if accepted.Status != "accepted" || accepted.Duplicate || accepted.MessageID != "mass-m1" || accepted.EventID == "" {
		t.Fatalf("bad receipt %+v", accepted)
	}
	same := submit(t, p, f, r)
	if want := accepted; !same.Duplicate || same.MessageID != want.MessageID || same.EventID != want.EventID || same.AcceptedAt != want.AcceptedAt {
		t.Fatalf("operation replay changed receipt: %+v %+v", accepted, same)
	}
	r.OperationID = "replayed-by-other-instance"
	r.Message.Meta = json.RawMessage(`{"collectorInstance":"another-process"}`)
	duplicate := submit(t, p, f, r)
	if !duplicate.Duplicate || duplicate.MessageID != accepted.MessageID || duplicate.AcceptedAt != accepted.AcceptedAt || duplicate.EventID != accepted.EventID {
		t.Fatalf("bad dedup %+v", duplicate)
	}
	for _, change := range []string{"content", "thread", "reply"} {
		t.Run(change, func(t *testing.T) {
			conflict := input("message-1")
			conflict.OperationID = "conflict-" + change
			switch change {
			case "content":
				conflict.Message.Content[0].Text.Text = "changed"
			case "thread":
				conflict.Message.ThreadID = fixtureThreadID("other")
			case "reply":
				conflict.ReplyTarget = json.RawMessage(`{"anotherAddress":"opaque"}`)
			}
			status, b := request(t, "POST", p.endpoint, "/apis/core/v1/trigger-messages", f.token, conflict)
			errorCode(t, status, b, 409, "conflict")
		})
	}
	if len(page(t, p, "trigger-messages").Items) != 1 || len(page(t, p, "threads").Items) != 1 || len(page(t, p, "internal-events").Items) != 1 {
		t.Fatal("duplicate/failed commands created records")
	}
	event := page(t, p, "internal-events")
	if !bytes.Contains(event.Items[0], []byte(`"status":"pending"`)) || !bytes.Contains(event.Items[0], []byte(`"kind":"route_request"`)) {
		t.Fatal("event was not pending")
	}
	status, b := request(t, "GET", p.endpoint, "/apis/core/v1/trigger-messages/"+accepted.MessageID, "", nil)
	if status != 200 {
		t.Fatalf("detail %s", b)
	}
	var m core.Message
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	require.Equal(t, accepted.AcceptedAt, m.ReceivedAt)
	require.Equal(t, accepted.MessageID, m.ID)
	require.Equal(t, r.Message.SourceMessageID, m.SourceMessageID)
	require.Equal(t, r.Message.Content, m.Content)
	require.JSONEq(t, `{"a":1,"b":"reference","quotedMessage":{"text":"quote"}}`, string(m.TriggerMeta))
	require.Equal(t, r.Message.ThreadID, accepted.ThreadID)
	// Queries through the shipped CLI require no source credential.
	cmd := exec.CommandContext(context.Background(), build(t), "observe", "trigger-messages", accepted.MessageID, "--endpoint", p.endpoint)
	cmd.Env = []string{"HOME=" + f.dir, "PATH=" + f.dir}
	out, err := cmd.CombinedOutput()
	if err != nil || !bytes.Contains(out, []byte(accepted.MessageID)) {
		t.Fatalf("CLI: %v %s", err, out)
	}
	p.stop(t, true)
	p = f.start()
	if got := submit(t, p, f, input("message-2")); got.MessageID != "mass-m2" {
		t.Fatalf("numbering did not resume after restart: %+v", got)
	}
	if len(page(t, p, "trigger-messages").Items) != 2 || len(page(t, p, "internal-events").Items) != 2 {
		t.Fatal("restart lost commit")
	}
	replay := submit(t, p, f, r)
	if replay != duplicate {
		t.Fatal("persisted operation receipt changed after restart")
	}
	t.Logf("durable receipt message=%s event=%s thread=%s", accepted.MessageID, accepted.EventID, accepted.ThreadID)
}

func TestPublicAdmissionKeepsTriggerMetadataOpaque(t *testing.T) {
	f := fixtureAt(t)
	p := f.start()
	r := input("group-message-1")
	r.ReplyTarget = nil
	r.Message.Meta = json.RawMessage(`{"addressed":"broadcast","sender":"source-specific"}`)
	r.Message.Content[0].Text.Text = "Original group message\n\nGroup instructions"
	accepted := submit(t, p, f, r)
	message := readRecord[core.Message](t, p, "trigger-messages/"+accepted.MessageID)
	require.Equal(t, r.Message.SourceMessageID, message.SourceMessageID)
	require.Equal(t, r.Message.Content, message.Content)
	require.JSONEq(t, string(r.Message.Meta), string(message.TriggerMeta))
	require.Empty(t, message.ReplyTarget)
}

func TestPublicAuthenticationAndReadOnly(t *testing.T) {
	f := fixtureAt(t)
	p := f.start()
	r := input("one")
	for _, path := range []string{
		"/v1/channel/messages", "/v1/channel/outbox/result", "/v1/observer/health", "/v1/operator/input-recovery",
		"/v1/core/topics", "/v1/core/trigger-messages", "/v1/core/outbox-messages", "/v1/core/scheduled-tasks", "/v1/mcp",
		"/v1/telemetry/performance", "/v1/telemetry/traces", "/v1/telemetry/metrics",
		"/massd/livez", "/massd/readyz", "/massd/jsonrpc", "/superagent/", "/apis/unknown",
		"/apis/core/v1/pull-requests", "/apis/core/v1/subscriptions", "/apis/core/v1/relations", "/apis/core/v1/routing-events", "/apis/core/v1/trigger-message-receipts",
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			status, body := request(t, method, p.endpoint, path, f.token, nil)
			errorCode(t, status, body, http.StatusNotFound, "not_found")
		}
	}
	status, b := request(t, "POST", p.endpoint, "/apis/core/v1/trigger-messages", "", r)
	errorCode(t, status, b, 401, "unauthorized")
	for _, field := range []string{"source", "accountId", "threadRef", "text", "actor", "addressed", "channel", "quotedMessage"} {
		t.Run(field, func(t *testing.T) {
			bad := map[string]any{"operationId": "legacy-" + field, "message": map[string]any{
				"sourceMessageId": field, "threadId": r.Message.ThreadID, "content": r.Message.Content, field: "legacy",
			}}
			gotStatus, gotBody := request(t, "POST", p.endpoint, "/apis/core/v1/trigger-messages", f.token, bad)
			errorCode(t, gotStatus, gotBody, 400, "invalid_argument")
		})
	}
	reservedThread := r
	reservedThread.Message.ThreadID = "core%3Ascheduled-task:mass-st1"
	status, b = request(t, "POST", p.endpoint, "/apis/core/v1/trigger-messages", f.token, reservedThread)
	errorCode(t, status, b, 403, "forbidden")
	for _, path := range []string{"/apis/core/v1/threads", "/apis/core/v1/internal-events", "/status"} {
		for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
			gotStatus, _ := request(t, method, p.endpoint, path, f.token, map[string]string{"write": "no"})
			if gotStatus != 405 {
				t.Fatalf("write allowed: %s %s %d", method, path, gotStatus)
			}
		}
	}
	for _, h := range []string{"Origin", "Sec-Fetch-Site", "Host"} {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, p.endpoint+"/apis/core/v1/trigger-messages", http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		switch h {
		case "Host":
			req.Host = "evil.example"
		case "Origin":
			req.Header.Set(h, "https://evil.example")
		default:
			req.Header.Set(h, "cross-site")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatal("foreign browser request accepted")
		}
	}
	if len(page(t, p, "trigger-messages").Items) != 0 {
		t.Fatal("rejected requests wrote state")
	}
	r.Message.Content[0].Text.Text = "Untrusted content: " + f.token + " <script>window.attack=true</script>"
	submit(t, p, f, r)
	_, b = request(t, "GET", p.endpoint, "/apis/core/v1/trigger-messages", "", nil)
	if bytes.Contains(b, []byte(f.token)) || !bytes.Contains(b, []byte("[redacted]")) {
		t.Fatal("query leaked source credential")
	}
	status, b = request(t, "HEAD", p.endpoint, "/apis/core/v1/trigger-messages", "", nil)
	if status != 200 || len(b) != 0 {
		t.Fatal("HEAD returned body")
	}
}

func TestPublicStablePagination(t *testing.T) {
	f := fixtureAt(t)
	p := f.start()
	for i := range 6 {
		submit(t, p, f, input(strconv.Itoa(i)))
	}
	status, b := request(t, "GET", p.endpoint, "/apis/core/v1/trigger-messages?limit=2", "", nil)
	if status != 200 {
		t.Fatalf("list %s", b)
	}
	var first core.Page
	if err := json.Unmarshal(b, &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatal("missing cursor")
	}
	submit(t, p, f, input("after-snapshot"))
	seen := map[string]bool{}
	result := first
	for {
		for _, b := range result.Items {
			var m core.Message
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatal(err)
			}
			if seen[m.ID] {
				t.Fatal("duplicate pagination row")
			}
			seen[m.ID] = true
			if m.SourceMessageID == "after-snapshot" {
				t.Fatal("cursor drifted into new snapshot")
			}
		}
		if result.NextCursor == "" {
			break
		}
		status, b = request(t, "GET", p.endpoint, "/apis/core/v1/trigger-messages?limit=2&cursor="+result.NextCursor, "", nil)
		if status != 200 {
			t.Fatalf("cursor %s", b)
		}
		result = core.Page{}
		if err := json.Unmarshal(b, &result); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 6 {
		t.Fatalf("pagination lost rows %d", len(seen))
	}
	for _, path := range []string{"trigger-messages?limit=201", "trigger-messages?limit=0", "trigger-messages?cursor=bad", "threads?cursor=" + first.NextCursor, "trigger-messages?cursor=" + first.NextCursor + "&threadId=other"} {
		status, b = request(t, "GET", p.endpoint, "/apis/core/v1/"+path, "", nil)
		errorCode(t, status, b, 400, "invalid_argument")
	}
}

func TestPublicCrashCommitBoundary(t *testing.T) {
	for _, stage := range []string{"before_commit", "after_commit"} {
		t.Run(stage, func(t *testing.T) {
			f := fixtureAt(t)
			gate := t.TempDir()
			p := f.start("SUPERAGENT_TEST_GATE_STAGE="+stage, "SUPERAGENT_TEST_GATE_DIR="+gate,
				"OTEL_TRACES_EXPORTER=otlp", "OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:0")
			r := input("crash")
			outcome := make(chan error, 1)
			go func() {
				b, _ := json.Marshal(r)
				req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, p.endpoint+"/apis/core/v1/trigger-messages", bytes.NewReader(b))
				if err != nil {
					outcome <- err
					return
				}
				req.Header.Set("Authorization", "Bearer "+f.token)
				setTriggerHeaders(req, fixtureTriggerSource)
				req.Header.Set("Content-Type", "application/json")
				c := http.Client{Timeout: 15 * time.Second}
				resp, err := c.Do(req)
				if resp != nil {
					_ = resp.Body.Close()
					if resp.StatusCode == http.StatusOK {
						err = errors.New("unexpected receipt before gate release")
					}
				}
				outcome <- err
			}()
			deadline := time.Now().Add(10 * time.Second)
			for {
				if _, err := os.Stat(filepath.Join(gate, stage)); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("commit gate not reached")
				}
				time.Sleep(10 * time.Millisecond)
			}
			p.stop(t, true)
			if err := <-outcome; err == nil {
				t.Fatal("killed request unexpectedly succeeded")
			}
			p = f.start()
			count := 0
			if stage == "after_commit" {
				count = 1
			}
			for _, kind := range []string{"trigger-messages", "threads", "internal-events"} {
				if len(page(t, p, kind).Items) != count {
					t.Fatalf("partial transaction after %s in %s", stage, kind)
				}
			}
			db, err := sql.Open("sqlite", "file:"+filepath.Join(f.data, "core.sqlite")+"?mode=ro")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			var associations int
			require.NoError(t, db.QueryRowContext(t.Context(), "SELECT count(*) FROM trace_associations").Scan(&associations))
			require.Equal(t, 2*count, associations, "Message and delivery associations commit with their objects")
			receipt := submit(t, p, f, r)
			if receipt.Status != "accepted" {
				t.Fatal("recovery replay failed")
			}
			for _, kind := range []string{"trigger-messages", "threads", "internal-events"} {
				if len(page(t, p, kind).Items) != 1 {
					t.Fatal("recovery replay duplicated records")
				}
			}
			t.Logf("%s: after kill all three collections=%d, same-operation recovery leaves one logical message/event", stage, count)
		})
	}
}

func TestPublicOwnershipLegacyAndStorage(t *testing.T) {
	t.Run("exclusive_and_restart", func(t *testing.T) {
		f := fixtureAt(t)
		p := f.start()
		f.fails("conflict")
		accepted := submit(t, p, f, input("retained"))
		p.stop(t, false)
		p = f.start()
		require.Len(t, page(t, p, "trigger-messages").Items, 1)
		replay := submit(t, p, f, input("retained"))
		require.True(t, replay.Duplicate)
		require.Equal(t, accepted.MessageID, replay.MessageID)
	})
	t.Run("legacy_untouched", func(t *testing.T) {
		f := fixtureAt(t)
		if err := os.MkdirAll(f.data, 0o700); err != nil {
			t.Fatal(err)
		}
		legacy := `{"version":4,"tickets":[{"id":"old"}]}`
		path := filepath.Join(f.data, "state.json")
		write(t, path, legacy)
		f.fails("failed_precondition")
		b, err := os.ReadFile(path)
		if err != nil || string(b) != legacy {
			t.Fatal("old data changed")
		}
		entries, err := os.ReadDir(f.data)
		if err != nil || len(entries) != 1 {
			t.Fatal("old directory mutated")
		}
	})
	t.Run("storage_quota", func(t *testing.T) {
		f := fixtureAt(t)
		// Leave room for the nine-table schema. The 512 KiB body
		// alone still exceeds the entire 448 KiB database budget.
		f.configure(112)
		p := f.start()
		r := input("oversize")
		r.Message.Content[0].Text.Text = strings.Repeat("x", 512*1024)
		status, b := request(t, "POST", p.endpoint, "/apis/core/v1/trigger-messages", f.token, r)
		errorCode(t, status, b, 503, "unavailable")
		for _, kind := range []string{"trigger-messages", "threads", "internal-events"} {
			if len(page(t, p, kind).Items) != 0 {
				t.Fatal("failed persistence left partial state")
			}
		}
		p.stop(t, true)
		f.configure(0)
		p = f.start()
		if len(page(t, p, "trigger-messages").Items) != 0 {
			t.Fatal("storage failure was committed")
		}
		submit(t, p, f, input("oversize"))
	})
	t.Run("invalid_database", func(t *testing.T) {
		f := fixtureAt(t)
		if err := os.MkdirAll(f.data, 0o700); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(f.data, "core.sqlite"), "not sqlite")
		f.fails("failed_precondition")
	})
}

func TestPublicConcurrentDuplicate(t *testing.T) {
	f := fixtureAt(t)
	p := f.start()
	var wg sync.WaitGroup
	ids := make(chan string, 12)
	for i := range 12 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := input("concurrent")
			r.OperationID = "instance-" + strconv.Itoa(i)
			ids <- submit(t, p, f, r).MessageID
		}(i)
	}
	wg.Wait()
	close(ids)
	id := ""
	for value := range ids {
		if id != "" && value != id {
			t.Fatal("concurrent duplicates differ")
		}
		id = value
	}
	if len(page(t, p, "internal-events").Items) != 1 {
		t.Fatal("concurrent duplicate event")
	}
}

func TestObserverBrowserFixture(t *testing.T) {
	if os.Getenv("SUPERAGENT_BROWSER_TEST") != "1" {
		t.Skip("explicit browser fixture only")
	}
	f := fixtureAt(t)
	// Keep a fixed explicit local port only for the browser runner's isolated fixture.
	b, err := os.ReadFile(f.config)
	if err != nil {
		t.Fatal(err)
	}
	port := os.Getenv("SUPERAGENT_BROWSER_PORT")
	if port == "" {
		port = "18793"
	}
	write(t, f.config, strings.Replace(string(b), "127.0.0.1:0", "127.0.0.1:"+port, 1))
	receiver, _ := coreOTLPReceiver(t, "")
	p := f.start(traceTestEnv(receiver.URL)...)
	r := input("browser-one")
	r.Message.Content[0].Text.Text = "Browser original body <script>window.attack=true</script>\n" + strings.Repeat("Long original text ", 1500)
	receipt := submit(t, p, f, r)
	for i := range 55 {
		r := input("browser-" + strconv.Itoa(i))
		r.Message.Content[0].Text.Text = "Browser row " + strconv.Itoa(i)
		submit(t, p, f, r)
	}
	tieBrowserRouterRegistrations(t, f)
	t.Logf("observer browser fixture ready at %s message=%s platform=%s/%s", p.endpoint, receipt.MessageID, runtime.GOOS, runtime.GOARCH)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, os.Interrupt)
	defer signal.Stop(signals)
	<-signals
}

func TestPublicOptionalReplyAddress(t *testing.T) {
	f := fixtureAt(t)
	p := f.start()
	r := input("no-reply-address")
	r.ReplyTarget = nil
	receipt := submit(t, p, f, r)
	status, b := request(t, "GET", p.endpoint, "/apis/core/v1/trigger-messages/"+receipt.MessageID, "", nil)
	var m core.Message
	if status != 200 || json.Unmarshal(b, &m) != nil || len(m.ReplyTarget) != 0 {
		t.Fatalf("reply address was required or fabricated: %s", b)
	}
	duplicate := r
	duplicate.OperationID = "another-operation"
	if got := submit(t, p, f, duplicate); got.MessageID != receipt.MessageID || !got.Duplicate {
		t.Fatal("addressless replay changed identity")
	}
	r.OperationID = "conflicting-address"
	r.ReplyTarget = json.RawMessage(`{"resource":"https://example.com/review/3","replyTokenRef":"external-adapter-reference"}`)
	status, b = request(t, "POST", p.endpoint, "/apis/core/v1/trigger-messages", f.token, r)
	errorCode(t, status, b, 409, "conflict")
	r = input("generic-reply-address")
	r.ReplyTarget = json.RawMessage(`{"resource":"https://example.com/review/3"}`)
	submit(t, p, f, r)
}

func TestConfigDefaultAndExplicitListen(t *testing.T) {
	f := fixtureAt(t)
	b, err := os.ReadFile(f.config)
	if err != nil {
		t.Fatal(err)
	}
	write(t, f.config, strings.Replace(string(b), "listen: 127.0.0.1:0\n", "", 1))
	c, err := core.LoadConfig(f.config)
	if err != nil || c.Listen != "127.0.0.1:24370" {
		t.Fatalf("default listen %s %v", c.Listen, err)
	}
	write(t, f.config, strings.Replace(string(b), "listen: 127.0.0.1:0", "listen: 0.0.0.0:0", 1))
	c, err = core.LoadConfig(f.config)
	if err != nil || c.Listen != "0.0.0.0:0" {
		t.Fatalf("explicit bind refused %s %v", c.Listen, err)
	}
	// Exercise explicit wildcard binding through an actual loopback connection.
	p := f.start()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(p.endpoint, "http://"))
	p.endpoint = "http://127.0.0.1:" + port
	status, b := request(t, "POST", p.endpoint, "/apis/core/v1/trigger-messages", "", input("unauth"))
	errorCode(t, status, b, 401, "unauthorized")
	submit(t, p, f, input("explicit-bind"))
}

func TestUnknownSchemaCompatibilityFixture(t *testing.T) {
	for _, version := range []int{19, 20, 999} {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			assertUnsupportedSchemaPreserved(t, version)
		})
	}
}

func assertUnsupportedSchemaPreserved(t *testing.T, version int) {
	t.Helper()
	f := fixtureAt(t)
	if err := os.MkdirAll(f.data, 0o700); err != nil {
		t.Fatal(err)
	}
	// Only schema metadata is synthesized, never business rows or ingress inputs.
	path := filepath.Join(f.data, "core.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(context.Background(), fmt.Sprintf("PRAGMA application_id=1398096210; PRAGMA user_version=%d;", version)); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f.fails("unsupported")
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("unsupported schema file changed")
	}
}

func TestPublicSpecialDataPathAndCLIError(t *testing.T) {
	f := fixtureAt(t)
	f.data = filepath.Join(f.dir, "data?#%原文")
	f.configure(0)
	p := f.start()
	submit(t, p, f, input("special-path"))
	cmd := exec.CommandContext(context.Background(), build(t), "observe", "trigger-messages", "--endpoint", p.endpoint, "--limit", "201")
	cmd.Env = []string{"HOME=" + f.dir, "PATH=" + f.dir}
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("invalid read CLI succeeded")
	}
	var result struct {
		Error core.Error `json:"error"`
	}
	if json.Unmarshal(out, &result) != nil || result.Error.Code != "invalid_argument" {
		t.Fatalf("CLI lost structured error: %s", out)
	}
	p.stop(t, true)
	p = f.start()
	if len(page(t, p, "trigger-messages").Items) != 1 {
		t.Fatal("special-path database was not reopened")
	}
}
