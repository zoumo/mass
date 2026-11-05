package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/internal/testpeer/scenario"
)

const codexRoundTrip = `{
  "version": 1,
  "provenance": "synthetic",
  "frames": [
    {
      "ordinal": 1,
      "direction": "c2s",
      "channel": "protocol",
      "epoch": 1,
      "wire": {"id": 1, "method": "initialize", "params": {}}
    },
    {
      "ordinal": 2,
      "direction": "s2c",
      "channel": "protocol",
      "epoch": 1,
      "wire": {"id": 1, "result": {}}
    }
  ]
}`

type invocation struct {
	code           int
	stdout, stderr string
}

func invoke(t *testing.T, args []string, input io.Reader) invocation {
	t.Helper()
	stdout, err := os.CreateTemp(t.TempDir(), "stdout-")
	require.NoError(t, err)
	stderr, err := os.CreateTemp(t.TempDir(), "stderr-")
	require.NoError(t, err)
	stdin := input
	if stdin == nil {
		file, err := os.Open(os.DevNull)
		require.NoError(t, err)
		defer file.Close()
		stdin = file
	}

	oldIn, oldOut, oldErr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = stdin.(*os.File), stdout, stderr
	defer func() {
		os.Stdin, os.Stdout, os.Stderr = oldIn, oldOut, oldErr
	}()

	code := run(args)
	require.NoError(t, stdout.Sync())
	require.NoError(t, stderr.Sync())
	_, err = stdout.Seek(0, io.SeekStart)
	require.NoError(t, err)
	_, err = stderr.Seek(0, io.SeekStart)
	require.NoError(t, err)
	out, err := io.ReadAll(stdout)
	require.NoError(t, err)
	errOut, err := io.ReadAll(stderr)
	require.NoError(t, err)
	require.NoError(t, stdout.Close())
	require.NoError(t, stderr.Close())
	return invocation{code: code, stdout: string(out), stderr: string(errOut)}
}

func scenarioFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "scenario.json")
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}

func TestRunWritesReceiptOnlyAfterCompleteConsumption(t *testing.T) {
	source := scenarioFile(t, codexRoundTrip)
	receipt := filepath.Join(t.TempDir(), "receipt.json")
	input, err := os.CreateTemp(t.TempDir(), "stdin-")
	require.NoError(t, err)
	_, err = input.WriteString(`{"id":"live","method":"initialize","params":{}}` + "\n")
	require.NoError(t, err)
	_, err = input.Seek(0, io.SeekStart)
	require.NoError(t, err)

	result := invoke(t, []string{"--provider", "codex", "--scenario", source, "--receipt", receipt, "--", "app-server"}, input)
	require.NoError(t, input.Close())
	assert.Equal(t, exitOK, result.code, result.stderr)
	assert.Empty(t, result.stderr)
	assert.JSONEq(t, `{"id":"live","result":{}}`, result.stdout)

	data, err := os.ReadFile(receipt)
	require.NoError(t, err)
	var decoded struct {
		Version         int    `json:"version"`
		FullConsumption bool   `json:"fullConsumption"`
		NativeLaunched  bool   `json:"nativeLaunched"`
		Peer            string `json:"peer"`
		Provider        string `json:"provider"`
		Frames          int    `json:"frames"`
		RecordingSHA256 string `json:"recordingSHA256"`
		ClientEOF       bool   `json:"clientEOF"`
	}
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, 1, decoded.Version)
	assert.True(t, decoded.FullConsumption)
	assert.False(t, decoded.NativeLaunched)
	assert.Equal(t, "MASS strict scenario peer", decoded.Peer)
	assert.Equal(t, "codex", decoded.Provider)
	assert.Equal(t, 2, decoded.Frames)
	original, err := os.ReadFile(source)
	require.NoError(t, err)
	assert.Equal(t, scenario.Hash(original), decoded.RecordingSHA256)
	assert.True(t, decoded.ClientEOF)
}

func TestRunRejectsIncompleteMismatchAndDisconnectWithoutReceipt(t *testing.T) {
	source := scenarioFile(t, codexRoundTrip)
	for _, test := range []struct {
		name     string
		source   string
		input    string
		exitCode int
	}{
		{"mismatch", source, `{"id":"live","method":"other","params":{}}` + "\n", exitFailure},
		{"incomplete", source, "", exitFailure},
		{"disconnect", scenarioFile(t, `{"version":1,"provenance":"synthetic","frames":[{"ordinal":1,"direction":"disconnect","channel":"protocol"}]}`), "", exitDisconnect},
	} {
		t.Run(test.name, func(t *testing.T) {
			receipt := filepath.Join(t.TempDir(), "receipt.json")
			input, err := os.CreateTemp(t.TempDir(), "stdin-")
			require.NoError(t, err)
			_, err = input.WriteString(test.input)
			require.NoError(t, err)
			_, err = input.Seek(0, io.SeekStart)
			require.NoError(t, err)

			result := invoke(t, []string{"--provider", "codex", "--scenario", test.source, "--receipt", receipt}, input)
			require.NoError(t, input.Close())
			assert.Equal(t, test.exitCode, result.code)
			assert.NotEmpty(t, result.stderr)
			_, err = os.Stat(receipt)
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestRunConfigurationAndExistingReceiptExitTwo(t *testing.T) {
	source := scenarioFile(t, codexRoundTrip)
	result := invoke(t, []string{"--provider", "codex", "--scenario", filepath.Join(t.TempDir(), "missing.json")}, nil)
	assert.Equal(t, exitConfiguration, result.code)

	receipt := filepath.Join(t.TempDir(), "receipt.json")
	require.NoError(t, os.WriteFile(receipt, []byte("existing"), 0o600))
	result = invoke(t, []string{"--provider", "codex", "--scenario", source, "--receipt", receipt}, nil)
	assert.Equal(t, exitConfiguration, result.code)
	contents, err := os.ReadFile(receipt)
	require.NoError(t, err)
	assert.Equal(t, "existing", string(contents))
}

func TestRunTimeoutExitsCanceled(t *testing.T) {
	source := scenarioFile(t, `{
	  "version": 1,
	  "provenance": "synthetic",
	  "timeoutMs": 25,
	  "frames": [{
	    "ordinal": 1,
	    "direction": "c2s",
	    "channel": "protocol",
	    "epoch": 1,
	    "wire": {"id": 1, "method": "initialize", "params": {}}
	  }]
	}`)
	in, writer, err := os.Pipe()
	require.NoError(t, err)
	defer in.Close()
	defer writer.Close()
	start := time.Now()
	result := invoke(t, []string{"--provider", "codex", "--scenario", source}, in)
	assert.Equal(t, exitCanceled, result.code)
	assert.Less(t, time.Since(start), time.Second)
}
