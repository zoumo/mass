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

	"github.com/zoumo/mass/superagent/pkg/core"
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
		if os.Getenv("SUPERAGENT_BROWSER_TEST") == "1" {
			tags += ",observerweb"
		}
		cmd := exec.CommandContext(context.Background(), "go", "build", "-race", "-tags", tags, "-o", binary, "../../cmd/superagent")
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOSUMDB=off")
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

type process struct {
	cmd      *exec.Cmd
	endpoint string
	stderr   bytes.Buffer
	done     chan error
	stopped  bool
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
	write(t, filepath.Join(dir, "source.token"), f.token)
	f.configure(0, "account-1")
	return f
}

func (f *fixture) configure(pages int, account string) {
	f.t.Helper()
	write(f.t, f.config, fmt.Sprintf("dataDir: %s\nlisten: 127.0.0.1:0\nstorageMaxPages: %d\nsources:\n  - source: dws\n    accountId: %s\n    tokenFile: %s\n", f.data, pages, account, filepath.Join(f.dir, "source.token")))
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
	p := &process{done: make(chan error, 1)}
	p.cmd = exec.CommandContext(context.Background(), f.coreExecutable(), "serve", "--config", f.config)
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
			Endpoint string `json:"endpoint"`
		}
		if json.Unmarshal(b, &info) != nil || info.Endpoint == "" {
			_ = p.cmd.Process.Kill()
			waitErr := <-p.done
			f.t.Fatalf("startup failed: %v %s %s", waitErr, b, p.stderr.String())
		}
		p.endpoint = info.Endpoint
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
	p.stopped = true
	var err error
	if kill {
		err = p.cmd.Process.Kill()
	} else {
		err = p.cmd.Process.Signal(syscall.SIGTERM)
	}
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-p.done:
		if !kill && err != nil {
			t.Fatalf("shutdown: %v %s", err, p.stderr.String())
		}
	case <-time.After(15 * time.Second):
		_ = p.cmd.Process.Kill()
		t.Fatal("process failed to exit")
	}
	client := http.Client{Timeout: 300 * time.Millisecond}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, p.endpoint+"/v1/observer/health", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := client.Do(req); err == nil {
		_ = r.Body.Close()
		t.Fatal("listener still reachable after exit")
	}
	t.Logf("process released pid=%d forced=%t endpoint=%s", p.cmd.Process.Pid, kill, p.endpoint)
}

func input(id string) core.SubmitRequest {
	return core.SubmitRequest{OperationID: "op-" + id, Message: core.MessageInput{Source: "dws", AccountID: "account-1", SourceMessageID: id, ThreadRef: core.ThreadRef{Source: "dws", ID: "conversation:Case/%原文"}, Actor: "sender-1", Text: "Original input " + id, Addressed: "direct", Channel: &core.Channel{Type: "dws", ConversationID: "conversation:Case/%原文", MessageID: id, ActorID: "sender-1"}}}
}

func request(t *testing.T, method, endpoint, path, token string, body any) (int, []byte) {
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

func submit(t *testing.T, p *process, f *fixture, r core.SubmitRequest) core.Receipt {
	t.Helper()
	status, b := request(t, "POST", p.endpoint, "/v1/channel/messages", f.token, r)
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
	status, b := request(t, "GET", p.endpoint, "/v1/observer/"+kind, "", nil)
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
	r.Message.Meta = map[string]any{"a": 1, "b": "reference"}
	r.Message.QuotedMessage = json.RawMessage(`{"text":"quote"}`)
	accepted := submit(t, p, f, r)
	if accepted.Status != "accepted" || accepted.Duplicate || accepted.MessageID != "mass-m1" || accepted.EventID == "" {
		t.Fatalf("bad receipt %+v", accepted)
	}
	same := submit(t, p, f, r)
	if want := accepted; !same.Duplicate || same.MessageID != want.MessageID || same.EventID != want.EventID || same.AcceptedAt != want.AcceptedAt {
		t.Fatalf("operation replay changed receipt: %+v %+v", accepted, same)
	}
	r.OperationID = "replayed-by-other-instance"
	r.Message.CollectedAt = "later"
	r.Message.CollectorInstance = "another-process"
	r.Message.SourceEventID = "new-event-envelope"
	duplicate := submit(t, p, f, r)
	if !duplicate.Duplicate || duplicate.MessageID != accepted.MessageID || duplicate.AcceptedAt != accepted.AcceptedAt || duplicate.EventID != accepted.EventID {
		t.Fatalf("bad dedup %+v", duplicate)
	}
	for _, change := range []string{"text", "actor", "thread", "reply", "meta"} {
		t.Run(change, func(t *testing.T) {
			conflict := input("message-1")
			conflict.Message.Meta = map[string]any{"a": 1, "b": "reference"}
			conflict.Message.QuotedMessage = json.RawMessage(`{"text":"quote"}`)
			conflict.OperationID = "conflict-" + change
			switch change {
			case "text":
				conflict.Message.Text = "changed"
			case "actor":
				conflict.Message.Actor = "other"
				conflict.Message.Channel.ActorID = "other"
			case "thread":
				conflict.Message.ThreadRef.ID = "other"
				conflict.Message.Channel.ConversationID = "other"
			case "reply":
				conflict.Message.Channel.Type = "other"
			case "meta":
				conflict.Message.Meta["a"] = 2
			}
			status, b := request(t, "POST", p.endpoint, "/v1/channel/messages", f.token, conflict)
			errorCode(t, status, b, 409, "conflict")
		})
	}
	if len(page(t, p, "messages").Items) != 1 || len(page(t, p, "threads").Items) != 1 || len(page(t, p, "routing-deliveries").Items) != 1 {
		t.Fatal("duplicate/failed commands created records")
	}
	event := page(t, p, "routing-deliveries")
	if !bytes.Contains(event.Items[0], []byte(`"status":"pending"`)) || !bytes.Contains(event.Items[0], []byte(`"kind":"route_request"`)) {
		t.Fatal("event was not pending")
	}
	status, b := request(t, "GET", p.endpoint, "/v1/observer/messages/"+accepted.MessageID, "", nil)
	if status != 200 {
		t.Fatalf("detail %s", b)
	}
	var m core.Message
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.At != "" || m.Human != nil || m.URL != "" || m.ReceivedAt != accepted.AcceptedAt || m.Text != r.Message.Text || m.ID != accepted.MessageID || m.SourceMessageID != r.Message.SourceMessageID {
		t.Fatalf("unknown values fabricated: %+v", m)
	}
	ref, err := core.ParseThreadKey(accepted.ThreadKey)
	if err != nil || ref != r.Message.ThreadRef || !strings.HasPrefix(accepted.ThreadKey, "dws:conversation") {
		t.Fatalf("thread identity not reversible: %s %v", accepted.ThreadKey, err)
	}
	// Queries through the shipped CLI require no source credential.
	cmd := exec.CommandContext(context.Background(), build(t), "observe", "messages", accepted.MessageID, "--endpoint", p.endpoint)
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
	if len(page(t, p, "messages").Items) != 2 || len(page(t, p, "routing-deliveries").Items) != 2 {
		t.Fatal("restart lost commit")
	}
	replay := submit(t, p, f, r)
	if replay != duplicate {
		t.Fatal("persisted operation receipt changed after restart")
	}
	t.Logf("durable receipt message=%s event=%s thread=%s", accepted.MessageID, accepted.EventID, accepted.ThreadKey)
}

func TestPublicAuthenticationAndReadOnly(t *testing.T) {
	f := fixtureAt(t)
	p := f.start()
	r := input("one")
	status, b := request(t, "POST", p.endpoint, "/v1/channel/messages", "", r)
	errorCode(t, status, b, 401, "unauthorized")
	for _, field := range []string{"source", "account", "threadSource", "id", "conversation", "actor", "direct", "time", "url"} {
		t.Run(field, func(t *testing.T) {
			bad := input(field)
			wantStatus, code := 400, "invalid_argument"
			switch field {
			case "source":
				bad.Message.Source = "other"
				wantStatus, code = 403, "forbidden"
			case "account":
				bad.Message.AccountID = "other"
				wantStatus, code = 403, "forbidden"
			case "threadSource":
				bad.Message.ThreadRef.Source = "other"
				wantStatus, code = 403, "forbidden"
			case "id":
				bad.Message.SourceMessageID = ""
			case "conversation":
				bad.Message.ThreadRef.ID = ""
			case "actor":
				bad.Message.Actor = ""
			case "direct":
				bad.Message.Addressed = "group"
			case "time":
				bad.Message.At = "yesterday"
			case "url":
				bad.Message.URL = "https://user:secret@example.com"
			}
			gotStatus, gotBody := request(t, "POST", p.endpoint, "/v1/channel/messages", f.token, bad)
			errorCode(t, gotStatus, gotBody, wantStatus, code)
		})
	}
	for _, path := range []string{"/v1/observer/messages", "/v1/observer/threads", "/v1/observer/routing-deliveries", "/v1/observer/health", "/observer/"} {
		for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
			gotStatus, _ := request(t, method, p.endpoint, path, f.token, map[string]string{"write": "no"})
			if gotStatus != 405 {
				t.Fatalf("write allowed: %s %s %d", method, path, gotStatus)
			}
		}
	}
	for _, h := range []string{"Origin", "Sec-Fetch-Site", "Host"} {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, p.endpoint+"/v1/observer/messages", http.NoBody)
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
	if len(page(t, p, "messages").Items) != 0 {
		t.Fatal("rejected requests wrote state")
	}
	r.Message.Text = "Untrusted content: " + f.token + " <script>window.attack=true</script>"
	submit(t, p, f, r)
	_, b = request(t, "GET", p.endpoint, "/v1/observer/messages", "", nil)
	if bytes.Contains(b, []byte(f.token)) || !bytes.Contains(b, []byte("[redacted]")) {
		t.Fatal("query leaked source credential")
	}
	status, b = request(t, "HEAD", p.endpoint, "/v1/observer/messages", "", nil)
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
	status, b := request(t, "GET", p.endpoint, "/v1/observer/messages?limit=2", "", nil)
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
		status, b = request(t, "GET", p.endpoint, "/v1/observer/messages?limit=2&cursor="+result.NextCursor, "", nil)
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
	for _, path := range []string{"messages?limit=201", "messages?limit=0", "messages?cursor=bad", "threads?cursor=" + first.NextCursor, "messages?cursor=" + first.NextCursor + "&threadKey=other"} {
		status, b = request(t, "GET", p.endpoint, "/v1/observer/"+path, "", nil)
		errorCode(t, status, b, 400, "invalid_argument")
	}
}

func TestPublicCrashCommitBoundary(t *testing.T) {
	for _, stage := range []string{"before_commit", "after_commit"} {
		t.Run(stage, func(t *testing.T) {
			f := fixtureAt(t)
			gate := t.TempDir()
			p := f.start("SUPERAGENT_TEST_GATE_STAGE="+stage, "SUPERAGENT_TEST_GATE_DIR="+gate)
			r := input("crash")
			outcome := make(chan error, 1)
			go func() {
				b, _ := json.Marshal(r)
				req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, p.endpoint+"/v1/channel/messages", bytes.NewReader(b))
				if err != nil {
					outcome <- err
					return
				}
				req.Header.Set("Authorization", "Bearer "+f.token)
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
			for _, kind := range []string{"messages", "threads", "routing-deliveries"} {
				if len(page(t, p, kind).Items) != count {
					t.Fatalf("partial transaction after %s in %s", stage, kind)
				}
			}
			receipt := submit(t, p, f, r)
			if receipt.Status != "accepted" {
				t.Fatal("recovery replay failed")
			}
			for _, kind := range []string{"messages", "threads", "routing-deliveries"} {
				if len(page(t, p, kind).Items) != 1 {
					t.Fatal("recovery replay duplicated records")
				}
			}
			t.Logf("%s: after kill all three collections=%d, same-operation recovery leaves one logical message/event", stage, count)
		})
	}
}

func TestPublicOwnershipLegacyAndStorage(t *testing.T) {
	t.Run("exclusive_and_account", func(t *testing.T) {
		f := fixtureAt(t)
		p := f.start()
		f.fails("conflict")
		submit(t, p, f, input("retained"))
		p.stop(t, false)
		f.configure(0, "another-account")
		f.fails("failed_precondition")
		f.configure(0, "account-1")
		p = f.start()
		if len(page(t, p, "messages").Items) != 1 {
			t.Fatal("account failure damaged data")
		}
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
		f.configure(112, "account-1")
		p := f.start()
		r := input("oversize")
		r.Message.Text = strings.Repeat("x", 512*1024)
		status, b := request(t, "POST", p.endpoint, "/v1/channel/messages", f.token, r)
		errorCode(t, status, b, 503, "unavailable")
		for _, kind := range []string{"messages", "threads", "routing-deliveries"} {
			if len(page(t, p, kind).Items) != 0 {
				t.Fatal("failed persistence left partial state")
			}
		}
		p.stop(t, true)
		f.configure(0, "account-1")
		p = f.start()
		if len(page(t, p, "messages").Items) != 0 {
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
	if len(page(t, p, "routing-deliveries").Items) != 1 {
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
	config := strings.Replace(string(b), "127.0.0.1:0", "127.0.0.1:"+port, 1)
	write(t, f.config, config+"    instanceId: browser-collector\n")
	p := f.start()
	r := input("browser-one")
	r.Message.Text = "Browser original body <script>window.attack=true</script>\n" + strings.Repeat("Long original text ", 1500)
	receipt := submit(t, p, f, r)
	for i := range 55 {
		r := input("browser-" + strconv.Itoa(i))
		r.Message.Text = "Browser row " + strconv.Itoa(i)
		submit(t, p, f, r)
	}
	tieBrowserRouterRegistrations(t, f)
	t.Logf("browser fixture: %s message=%s platform=%s/%s", p.endpoint, receipt.MessageID, runtime.GOOS, runtime.GOARCH)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, os.Interrupt)
	defer signal.Stop(signals)
	<-signals
}

func TestPublicOptionalReplyAddress(t *testing.T) {
	f := fixtureAt(t)
	p := f.start()
	r := input("no-reply-address")
	r.Message.Channel = nil
	receipt := submit(t, p, f, r)
	status, b := request(t, "GET", p.endpoint, "/v1/observer/messages/"+receipt.MessageID, "", nil)
	var m core.Message
	if status != 200 || json.Unmarshal(b, &m) != nil || m.Channel != nil {
		t.Fatalf("reply address was required or fabricated: %s", b)
	}
	duplicate := r
	duplicate.OperationID = "another-operation"
	if got := submit(t, p, f, duplicate); got.MessageID != receipt.MessageID || !got.Duplicate {
		t.Fatal("addressless replay changed identity")
	}
	r.OperationID = "conflicting-address"
	r.Message.Channel = &core.Channel{Type: "custom", Address: map[string]any{"resource": "https://example.com/review/3", "replyTokenRef": "external-adapter-reference"}}
	status, b = request(t, "POST", p.endpoint, "/v1/channel/messages", f.token, r)
	errorCode(t, status, b, 409, "conflict")
	r = input("generic-reply-address")
	r.Message.Channel = &core.Channel{Type: "custom", Address: map[string]any{"resource": "https://example.com/review/3"}}
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
	status, b := request(t, "POST", p.endpoint, "/v1/channel/messages", "", input("unauth"))
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
	f.configure(0, "account-1")
	p := f.start()
	submit(t, p, f, input("special-path"))
	cmd := exec.CommandContext(context.Background(), build(t), "observe", "messages", "--endpoint", p.endpoint, "--limit", "201")
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
	if len(page(t, p, "messages").Items) != 1 {
		t.Fatal("special-path database was not reopened")
	}
}
