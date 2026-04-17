package daemon

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

// mockClient embeds the Client interface and overrides only what daemon needs.
type mockClient struct {
	pkgariapi.Client
	listFn func(ctx context.Context, list pkgariapi.ObjectList, opts ...pkgariapi.ListOption) error
}

func (m *mockClient) List(ctx context.Context, list pkgariapi.ObjectList, opts ...pkgariapi.ListOption) error {
	if m.listFn != nil {
		return m.listFn(ctx, list, opts...)
	}
	return nil
}

func (m *mockClient) Close() error { return nil }

func TestStatusRunning(t *testing.T) {
	mock := &mockClient{
		listFn: func(_ context.Context, _ pkgariapi.ObjectList, _ ...pkgariapi.ListOption) error {
			return nil
		},
	}

	cmd := NewCommand(func() (pkgariapi.Client, error) { return mock, nil })
	cmd.SetArgs([]string{"status"})

	var stdout bytes.Buffer
	cmd.SetOut(&stdout)

	// Capture the fmt.Println output by redirecting os.Stdout.
	r, w, err := os.Pipe()
	require.NoError(t, err)
	origStdout := os.Stdout
	os.Stdout = w

	err = cmd.Execute()
	require.NoError(t, err)

	w.Close()
	os.Stdout = origStdout

	var captured bytes.Buffer
	_, _ = captured.ReadFrom(r)

	assert.Contains(t, captured.String(), "daemon: running")
}

func TestStatusNotRunning_ClientError(t *testing.T) {
	cmd := NewCommand(func() (pkgariapi.Client, error) {
		return nil, fmt.Errorf("connection refused")
	})
	cmd.SetArgs([]string{"status"})

	r, w, err := os.Pipe()
	require.NoError(t, err)
	origStdout := os.Stdout
	os.Stdout = w

	err = cmd.Execute()
	require.NoError(t, err)

	w.Close()
	os.Stdout = origStdout

	var captured bytes.Buffer
	_, _ = captured.ReadFrom(r)

	assert.Contains(t, captured.String(), "daemon: not running")
}
