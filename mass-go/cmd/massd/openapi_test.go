package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOfflineOpenAPI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var previous []byte
	for range 2 {
		cmd := newCommand()
		var output bytes.Buffer
		cmd.SetOut(&output)
		cmd.SetArgs([]string{"openapi"})
		require.NoError(t, cmd.Execute())
		var doc map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(output.Bytes(), &doc))
		require.JSONEq(t, `"3.1.0"`, string(doc["openapi"]))
		if previous != nil {
			require.Equal(t, previous, output.Bytes())
		}
		previous = append([]byte(nil), output.Bytes()...)
	}
	_, err := os.Stat(filepath.Join(home, ".mass"))
	require.True(t, os.IsNotExist(err), "export must not open the default Store")
}
