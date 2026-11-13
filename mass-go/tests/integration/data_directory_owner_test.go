package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/resource"
)

func TestDataDirectoryHasOneWriterAcrossPathAliases(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	data := filepath.Join(base, "data")
	alias := filepath.Join(base, "alias")
	require.NoError(t, os.MkdirAll(home, 0o700))
	require.NoError(t, os.MkdirAll(data, 0o700))
	require.NoError(t, os.Symlink(data, alias))

	env := environmentWithHome(home)
	firstCtx, firstCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer firstCancel()
	first := exec.CommandContext(firstCtx, app, "--data", data, "--listen", "127.0.0.1:0", "--local", "--session-idle-timeout", "0")
	first.Env = env
	var firstErr lockedBuffer
	first.Stderr = &firstErr
	stdout, err := first.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, first.Start())
	waited := false
	defer func() {
		if waited || first.Process == nil {
			return
		}
		_ = first.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- first.Wait() }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			_ = first.Process.Kill()
			<-done
		}
	}()

	var ready struct {
		URL string `json:"url"`
	}
	require.NoError(t, json.NewDecoder(stdout).Decode(&ready), firstErr.String())
	require.True(t, strings.HasPrefix(ready.URL, "ws://127.0.0.1:"))
	require.True(t, strings.HasSuffix(ready.URL, "/rpc"))

	c := newHTTPClient(t, ready.URL, "")
	var rows []resource.Runtime
	require.NoError(t, c.ListRuntimes(t.Context(), resource.RuntimeQuery{}, &rows))
	require.Empty(t, rows)
	before := snapshotTree(t, data)

	secondCtx, secondCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer secondCancel()
	second := exec.CommandContext(secondCtx, app, "--data", alias, "--listen", "127.0.0.1:0", "--local", "--session-idle-timeout", "0")
	second.Env = env
	var secondOut, secondErr bytes.Buffer
	second.Stdout = &secondOut
	second.Stderr = &secondErr
	err = second.Run()
	require.Error(t, err, "second writer must fail")
	require.Empty(t, secondOut.String(), "second writer must not announce readiness")
	require.Contains(t, secondErr.String(), "data_directory_in_use")
	require.NotContains(t, secondErr.String(), "operation_unconfirmed")

	after := snapshotTree(t, data)
	require.Equal(t, before, after, "failed startup must not correct or mutate shared data")
	_, err = os.Stat(filepath.Join(data, "private"))
	require.True(t, os.IsNotExist(err), "second writer must not construct a Driver")

	require.NoError(t, c.ListRuntimes(t.Context(), resource.RuntimeQuery{}, &rows))
	require.NoError(t, first.Process.Signal(syscall.SIGTERM))
	done := make(chan error, 1)
	go func() { done <- first.Wait() }()
	select {
	case err = <-done:
		waited = true
		require.NoError(t, err, firstErr.String())
	case <-firstCtx.Done():
		t.Fatal(firstCtx.Err())
	}
}

func environmentWithHome(home string) []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "HOME=") {
			env = append(env, entry)
		}
	}
	return append(env, "HOME="+home)
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			snapshot[relative+"/"] = "directory"
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snapshot[relative] = fmt.Sprintf("%o:%x", entry.Type(), sha256.Sum256(content))
		return nil
	})
	require.NoError(t, err)
	return snapshot
}
