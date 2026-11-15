package sa

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var (
	fakeMulticaBinary string
	massSABinary      string
)

// Exercise the same multi-process CLI boundary used concurrently by Router
// comments and issue creation. Every successful create must survive readback.
func TestMulticaConcurrentCLIReadbacks(t *testing.T) {
	fake := newFakeMultica(t)
	client, err := NewMulticaCLI(fakeMulticaBinary, MulticaTarget{WorkspaceID: fake.workspace, ProjectID: fake.project})
	if err != nil {
		t.Fatal(err)
	}
	const count = 16
	start := make(chan struct{})
	results := make(chan error, count)
	var group sync.WaitGroup
	for i := range count {
		group.Go(func() {
			<-start
			title := fmt.Sprintf("Concurrent Issue %d", i)
			issue, err := client.CreateIssue(t.Context(), CreateIssueInput{Title: title, Description: "Concurrent readback regression"})
			if err == nil {
				var read IssueSnapshot
				read, err = client.GetIssue(t.Context(), issue.ID)
				if err == nil && read.Title != title {
					err = fmt.Errorf("created Issue %s lost its title: %q", issue.ID, read.Title)
				}
			}
			results <- err
		})
	}
	close(start)
	group.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Error(err)
		}
	}
}

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mass-sa-fake-multica-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake multica: temporary directory:", err)
		os.Exit(1)
	}
	binary := filepath.Join(dir, "multica")
	build := exec.Command("go", "build", "-o", binary, "./testdata/fake_multica")
	build.Env = append(os.Environ(), "GOWORK=off")
	if output, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "fake multica: build failed: %v\n%s", err, output)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	fakeMulticaBinary = binary
	massSABinary = filepath.Join(dir, "mass-sa")
	buildMASSCmd := exec.Command("go", "build", "-o", massSABinary, "./cmd/mass-sa")
	buildMASSCmd.Env = append(os.Environ(), "GOWORK=off")
	if output, err := buildMASSCmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "mass-sa test binary: build failed: %v\n%s\n", err, output)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

type fakeMultica struct {
	t         *testing.T
	workspace string
	project   string
	logPath   string
	cliState  string
	saState   string
}

func newFakeMultica(t *testing.T) *fakeMultica {
	t.Helper()
	dir := t.TempDir()
	fake := &fakeMultica{
		t:         t,
		workspace: "11111111-1111-4111-8111-111111111111",
		project:   "22222222-2222-4222-8222-222222222222",
		logPath:   filepath.Join(dir, "multica-calls.jsonl"),
		cliState:  filepath.Join(dir, "multica-state.json"),
		saState:   filepath.Join(dir, "mass-sa-state.json"),
	}
	for name, value := range map[string]string{
		"FAKE_MULTICA_LOG":              fake.logPath,
		"FAKE_MULTICA_STATE":            fake.cliState,
		"FAKE_MULTICA_WORKSPACE":        fake.workspace,
		"FAKE_MULTICA_PROJECT":          fake.project,
		"FAKE_MULTICA_FAIL":             "",
		"FAKE_MULTICA_FAIL_AFTER_WRITE": "",
		"FAKE_MULTICA_HANG":             "",
		"FAKE_MULTICA_IGNORE_STATUS":    "",
		"FAKE_MULTICA_DROP_METADATA":    "",
		"FAKE_MULTICA_OMIT_COMMENT":     "",
	} {
		t.Setenv(name, value)
	}
	return fake
}

func (f *fakeMultica) config() Config {
	f.t.Helper()
	project := newTestProject("mass")
	project.Multica.WorkspaceID = f.workspace
	project.Multica.ProjectID = f.project
	cfg := newTestConfig(f.t, "ws://127.0.0.1:24368/rpc", f.saState, project)
	cfg.Multica.Executable = fakeMulticaBinary
	return cfg
}

func (f *fakeMultica) bridge() *TicketBridge {
	f.t.Helper()
	cfg := f.config()
	store, err := OpenStateStore(cfg.StateFile)
	if err != nil {
		f.t.Fatal(err)
	}
	bridge, err := NewTicketBridge(cfg, store)
	if err != nil {
		f.t.Fatal(err)
	}
	return bridge
}

func (f *fakeMultica) storedState() ProjectState {
	f.t.Helper()
	store, err := OpenStateStore(f.saState)
	if err != nil {
		f.t.Fatal(err)
	}
	return store.Snapshot().Projects["mass"]
}

type fakeInvocation struct {
	Args []string `json:"args"`
	CWD  string   `json:"cwd"`
}

func (f *fakeMultica) invocations() []fakeInvocation {
	f.t.Helper()
	raw, err := os.ReadFile(f.logPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		f.t.Fatal(err)
	}
	var invocations []fakeInvocation
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var invocation fakeInvocation
		if err := json.Unmarshal([]byte(line), &invocation); err != nil {
			f.t.Fatalf("invalid invocation log line %q: %v", line, err)
		}
		invocations = append(invocations, invocation)
	}
	return invocations
}

func (f *fakeMultica) invocationsOf(command ...string) []fakeInvocation {
	f.t.Helper()
	var matched []fakeInvocation
	for _, invocation := range f.invocations() {
		if containsSequence(invocation.Args, command) {
			matched = append(matched, invocation)
		}
	}
	return matched
}

func (f *fakeMultica) issue(id string) fakeCLIIssue {
	f.t.Helper()
	var state fakeCLIState
	raw, err := os.ReadFile(f.cliState)
	if err != nil {
		f.t.Fatalf("fake multica state is unavailable: %v", err)
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		f.t.Fatal(err)
	}
	issue, ok := state.Issues[id]
	if !ok {
		f.t.Fatalf("fake multica issue %s not found", id)
	}
	return issue
}

func (f *fakeMultica) comments(issueID string) []fakeCLIComment {
	f.t.Helper()
	var state fakeCLIState
	raw, err := os.ReadFile(f.cliState)
	if err != nil {
		f.t.Fatalf("fake multica state is unavailable: %v", err)
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		f.t.Fatal(err)
	}
	return state.Comments[issueID]
}

type fakeCLIIssue struct {
	ID            string            `json:"id"`
	Identifier    string            `json:"identifier"`
	Title         string            `json:"title"`
	Description   string            `json:"description"`
	Status        string            `json:"status"`
	ProjectID     string            `json:"project_id"`
	ParentIssueID string            `json:"parent_issue_id"`
	Metadata      map[string]string `json:"metadata"`
}

type fakeCLIComment struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

type fakeCLIState struct {
	Issues   map[string]fakeCLIIssue     `json:"issues"`
	Comments map[string][]fakeCLIComment `json:"comments"`
}

func containsSequence(args, sequence []string) bool {
	if len(sequence) == 0 || len(args) < len(sequence) {
		return false
	}
	for start := 0; start <= len(args)-len(sequence); start++ {
		matched := true
		for offset, value := range sequence {
			if args[start+offset] != value {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func hasArg(invocation fakeInvocation, value string) bool {
	for _, arg := range invocation.Args {
		if arg == value {
			return true
		}
	}
	return false
}

func hasArgPrefix(invocation fakeInvocation, prefix string) bool {
	for _, arg := range invocation.Args {
		if strings.HasPrefix(arg, prefix) {
			return true
		}
	}
	return false
}
