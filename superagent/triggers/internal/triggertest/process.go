package triggertest

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TriggerBinary returns a caller-supplied superagent binary, when provided.
func TriggerBinary() string {
	if path := os.Getenv("SUPERAGENT_TRIGGER_BINARY"); path != "" {
		return path
	}
	return os.Getenv("SUPERAGENT_CORE")
}

// Binary describes a fixture executable to build when Path is empty.
type Binary struct {
	Path          *string
	Name, Package string
}

// Run builds fixture executables and runs the package's tests from TestMain.
func Run(m *testing.M, binaries ...Binary) int {
	dir, err := os.MkdirTemp("", "superagent-trigger-tests-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	for _, binary := range binaries {
		if *binary.Path != "" {
			continue
		}
		*binary.Path = filepath.Join(dir, binary.Name)
		args := []string{"build"}
		if os.Getenv("TRIGGER_TEST_RACE") == "1" {
			args = append(args, "-race")
		}
		args = append(args, "-o", *binary.Path, binary.Package)
		cmd := exec.CommandContext(context.Background(), "go", args...)
		if output, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintln(os.Stderr, string(output), err)
			return 1
		}
	}
	return m.Run()
}

type Process struct {
	cmd   *exec.Cmd
	mu    sync.Mutex
	lines []string
	Done  chan error
}

func Start(t *testing.T, exe string, args, env []string) *Process {
	t.Helper()
	// Cleanup must be able to give children SIGTERM before a forced kill.
	p := &Process{cmd: exec.CommandContext(context.WithoutCancel(t.Context()), exe, args...), Done: make(chan error, 1)}
	p.cmd.Env = append(os.Environ(), env...)
	out, err := p.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	p.cmd.Stderr = p.cmd.Stdout
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		scan := bufio.NewScanner(out)
		scan.Buffer(make([]byte, 4096), 1<<20)
		for scan.Scan() {
			p.mu.Lock()
			p.lines = append(p.lines, scan.Text())
			p.mu.Unlock()
		}
		p.Done <- p.cmd.Wait()
	}()
	t.Cleanup(func() {
		defer func() {
			if strings.Contains(p.Text(), "DATA RACE") {
				t.Errorf("subprocess race: %s", p.Text())
			}
		}()
		select {
		case <-p.Done:
			return
		default:
		}
		_ = p.Signal(syscall.SIGTERM)
		select {
		case <-p.Done:
		case <-time.After(8 * time.Second):
			_ = p.Kill()
			t.Error("test process did not stop")
		}
	})
	return p
}

func (p *Process) Text() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.lines, "\n")
}

func Wait(t *testing.T, why string, fn func() bool) {
	t.Helper()
	until := time.Now().Add(10 * time.Second)
	for time.Now().Before(until) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", why)
}

func (p *Process) Endpoint(t *testing.T, key string) string {
	t.Helper()
	value := ""
	Wait(t, "endpoint "+key+" "+p.Text(), func() bool {
		for line := range strings.SplitSeq(p.Text(), "\n") {
			var v map[string]any
			if json.Unmarshal([]byte(line), &v) == nil {
				if s, ok := v[key].(string); ok {
					value = s
					return true
				}
			}
		}
		return false
	})
	return value
}

func CorePath(t *testing.T) string {
	t.Helper()
	p := os.Getenv("SUPERAGENT_CORE")
	if p == "" {
		t.Skip("SUPERAGENT_CORE required for real Core process integration")
	}
	return p
}

func FreeAddress(t *testing.T) string {
	t.Helper()
	l, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

const Token = "test-only-channel-token-01234567890123456789"

func Terminate(t *testing.T, p *Process) {
	t.Helper()
	_ = p.Signal(syscall.SIGTERM)
	select {
	case err := <-p.Done:
		if err != nil {
			t.Fatalf("shutdown: %v\n%s", err, p.Text())
		}
		p.Done <- err
	case <-time.After(8 * time.Second):
		t.Fatal("shutdown timeout")
	}
}

func (p *Process) Signal(signal os.Signal) error { return p.cmd.Process.Signal(signal) }

func (p *Process) Kill() error { return p.cmd.Process.Kill() }
