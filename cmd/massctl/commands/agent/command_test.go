package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

// mockClient embeds the Client interface and overrides only what agent commands need.
type mockClient struct {
	pkgariapi.Client
	createFn func(ctx context.Context, obj pkgariapi.Object) error
	getFn    func(ctx context.Context, key pkgariapi.ObjectKey, obj pkgariapi.Object) error
	updateFn func(ctx context.Context, obj pkgariapi.Object) error
	listFn   func(ctx context.Context, list pkgariapi.ObjectList, opts ...pkgariapi.ListOption) error
	deleteFn func(ctx context.Context, key pkgariapi.ObjectKey, obj pkgariapi.Object) error
}

func (m *mockClient) Create(ctx context.Context, obj pkgariapi.Object) error {
	if m.createFn != nil {
		return m.createFn(ctx, obj)
	}
	return nil
}

func (m *mockClient) Get(ctx context.Context, key pkgariapi.ObjectKey, obj pkgariapi.Object) error {
	if m.getFn != nil {
		return m.getFn(ctx, key, obj)
	}
	return nil
}

func (m *mockClient) Update(ctx context.Context, obj pkgariapi.Object) error {
	if m.updateFn != nil {
		return m.updateFn(ctx, obj)
	}
	return nil
}

func (m *mockClient) List(ctx context.Context, list pkgariapi.ObjectList, opts ...pkgariapi.ListOption) error {
	if m.listFn != nil {
		return m.listFn(ctx, list, opts...)
	}
	return nil
}

func (m *mockClient) Delete(ctx context.Context, key pkgariapi.ObjectKey, obj pkgariapi.Object) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, key, obj)
	}
	return nil
}

func (m *mockClient) Close() error { return nil }

// newMockClientFn returns a ClientFn that always returns the given mock.
func newMockClientFn(mock *mockClient) func() (pkgariapi.Client, error) {
	return func() (pkgariapi.Client, error) { return mock, nil }
}

// writeYAML creates a temp file with the given content and returns its path.
func writeYAML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	err := os.WriteFile(path, []byte(content), 0o644)
	require.NoError(t, err)
	return path
}

// ────────────────────────────────────────────────────────────────────────────
// apply sub-command tests
// ────────────────────────────────────────────────────────────────────────────

func TestApplyMissingFile(t *testing.T) {
	mock := &mockClient{}
	cmd := NewCommand(newMockClientFn(mock))
	cmd.SetArgs([]string{"apply", "--file", "/nonexistent/path/agent.yaml"})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading agent file")
}

func TestApplyInvalidYAML(t *testing.T) {
	path := writeYAML(t, "not: valid: yaml: {{{")
	mock := &mockClient{}
	cmd := NewCommand(newMockClientFn(mock))
	cmd.SetArgs([]string{"apply", "--file", path})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing agent YAML")
}

func TestApplyMissingName(t *testing.T) {
	path := writeYAML(t, `
metadata:
  name: ""
spec:
  command: /usr/bin/test
`)
	mock := &mockClient{}
	cmd := NewCommand(newMockClientFn(mock))
	cmd.SetArgs([]string{"apply", "--file", path})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "metadata.name")
}

func TestApplyMissingCommand(t *testing.T) {
	path := writeYAML(t, `
metadata:
  name: test-agent
spec:
  command: ""
`)
	mock := &mockClient{}
	cmd := NewCommand(newMockClientFn(mock))
	cmd.SetArgs([]string{"apply", "--file", path})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.command")
}

func TestApplySuccess(t *testing.T) {
	path := writeYAML(t, `
metadata:
  name: test-agent
spec:
  command: /usr/bin/test
`)
	mock := &mockClient{
		createFn: func(_ context.Context, _ pkgariapi.Object) error {
			return nil
		},
	}

	// Redirect stdout to discard OutputJSON output.
	r, w, pipeErr := os.Pipe()
	require.NoError(t, pipeErr)
	origStdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	cmd := NewCommand(newMockClientFn(mock))
	cmd.SetArgs([]string{"apply", "--file", path})

	err := cmd.Execute()

	w.Close()
	// Drain pipe to prevent blocking.
	buf := make([]byte, 4096)
	_, _ = r.Read(buf)

	require.NoError(t, err)
}

// ────────────────────────────────────────────────────────────────────────────
// get sub-command tests
// ────────────────────────────────────────────────────────────────────────────

func TestGetSuccess(t *testing.T) {
	mock := &mockClient{
		getFn: func(_ context.Context, key pkgariapi.ObjectKey, obj pkgariapi.Object) error {
			ag := obj.(*pkgariapi.Agent)
			ag.Metadata.Name = key.Name
			ag.Spec.Command = "/usr/bin/test"
			return nil
		},
	}

	r, w, pipeErr := os.Pipe()
	require.NoError(t, pipeErr)
	origStdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	cmd := NewCommand(newMockClientFn(mock))
	cmd.SetArgs([]string{"get", "--name", "my-agent"})

	err := cmd.Execute()

	w.Close()
	buf := make([]byte, 4096)
	_, _ = r.Read(buf)

	require.NoError(t, err)
}

// ────────────────────────────────────────────────────────────────────────────
// list sub-command tests
// ────────────────────────────────────────────────────────────────────────────

func TestListSuccess(t *testing.T) {
	mock := &mockClient{
		listFn: func(_ context.Context, list pkgariapi.ObjectList, _ ...pkgariapi.ListOption) error {
			// No need to populate; empty list is fine for success path.
			return nil
		},
	}

	r, w, pipeErr := os.Pipe()
	require.NoError(t, pipeErr)
	origStdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	cmd := NewCommand(newMockClientFn(mock))
	cmd.SetArgs([]string{"list"})

	err := cmd.Execute()

	w.Close()
	buf := make([]byte, 4096)
	_, _ = r.Read(buf)

	require.NoError(t, err)
}

// ────────────────────────────────────────────────────────────────────────────
// delete sub-command tests
// ────────────────────────────────────────────────────────────────────────────

func TestDeleteSuccess(t *testing.T) {
	mock := &mockClient{
		deleteFn: func(_ context.Context, _ pkgariapi.ObjectKey, _ pkgariapi.Object) error {
			return nil
		},
	}

	r, w, pipeErr := os.Pipe()
	require.NoError(t, pipeErr)
	origStdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	cmd := NewCommand(newMockClientFn(mock))
	cmd.SetArgs([]string{"delete", "--name", "my-agent"})

	err := cmd.Execute()

	w.Close()
	buf := make([]byte, 4096)
	_, _ = r.Read(buf)

	require.NoError(t, err)
}
