package subcommand

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestInjectCLIInvalidInputDoesNotBecomeUnknown(t *testing.T) {
	var out, stderr bytes.Buffer
	exit := Run(context.Background(), []string{"--url", "invalid-url", "-o", "json", "session", "inject", "native", "--input-file", "-"}, strings.NewReader(`{"content":[]}`), &out, &stderr)
	require.Equal(t, 2, exit)
	require.Empty(t, out.String())
	var p runtime.OperationError
	require.NoError(t, json.Unmarshal(stderr.Bytes(), &p))
	require.Equal(t, "invalid_argument", p.Code)
}
