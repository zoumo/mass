package runtime_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestInitialSessionConfigurationCodec(t *testing.T) {
	for _, raw := range []string{`{}`, `{"model":"","effort":""}`, `{"model":" Native Model ","effort":"MAX"}`, `{"model":"\ud83d\ude80"}`} {
		var config runtime.InitialSessionConfig
		require.NoError(t, json.Unmarshal([]byte(raw), &config))
		require.NoError(t, config.Validate())
	}
	for _, value := range []string{"a\x00b", string([]byte{0xff})} {
		require.Error(t, (&runtime.InitialSessionConfig{Model: value}).Validate())
		require.Error(t, (&runtime.InitialSessionConfig{Effort: value}).Validate())
	}
	var legacy runtime.SessionSpec
	require.NoError(t, json.Unmarshal([]byte(`{"cwd":"/old"}`), &legacy))
	require.Nil(t, legacy.Config)
}
