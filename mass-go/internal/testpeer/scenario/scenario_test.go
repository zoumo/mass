package scenario_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/internal/testpeer/rawjson"
	"github.com/zoumo/mass/mass-go/internal/testpeer/scenario"
)

const (
	native       = `{ "future":{"z":1e+09,"huge":9007199254740993,"fraction":0.123456789012345678901234567890},"type":"notice" }`
	unifiedFrame = `{"ordinal":1,"direction":"s2c","channel":"protocol","epoch":1,"elapsedMs":12,"wire":` + native + `}`
	legacyFrame  = `{"ordinal":1,"direction":"server","channel":"control","data":` + native + `}`
)

func TestReadPreservesAuthoredWire(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		for name, input := range map[string]string{
			"unified JSONL":      unifiedFrame + "\n",
			"legacy JSONL":       legacyFrame + "\r\n",
			"unified document":   `{"version":1,"provenance":"synthetic","timeoutMs":125,"frames":[` + unifiedFrame + `]}`,
			"recording document": `{"version":1,"provenance":"recording","source":{"path":"owned/fixture","sha256":"unchanged"},"frames":[` + legacyFrame + `]}`,
		} {
			t.Run(provider+"/"+name, func(t *testing.T) {
				document, err := scenario.Read(strings.NewReader(input), provider, scenario.Limits{})
				require.NoError(t, err)
				require.Len(t, document.Frames, 1)
				assert.True(t, bytes.Equal([]byte(native), document.Frames[0].Wire.Raw()), "authored wire bytes must remain unchanged")
				assert.Equal(t, []byte(input), document.Original)
				assert.Equal(t, scenario.Hash([]byte(input)), document.SHA256)
				assert.Equal(t, scenario.ServerToClient, document.Frames[0].Direction)
				if name == "unified document" {
					assert.Equal(t, 125*time.Millisecond, document.Timeout)
				}
			})
		}
	}
}

func TestSyntheticDelayAndDisconnectAreExplicit(t *testing.T) {
	input := `{"version":1,"provenance":"synthetic","frames":[` +
		strings.Replace(unifiedFrame, `"wire":`, `"delayMs":1,"wire":`, 1) + `,` +
		`{"ordinal":2,"direction":"disconnect","channel":"control"}]}`
	document, err := scenario.Read(strings.NewReader(input), "codex", scenario.Limits{})
	require.NoError(t, err)
	assert.Equal(t, time.Millisecond, document.Frames[0].Delay)
	assert.True(t, document.Frames[1].Disconnect)
	assert.Equal(t, rawjson.Invalid, document.Frames[1].Wire.Kind())

	recording := strings.Replace(input, `"provenance":"synthetic"`, `"provenance":"recording","source":"owned/fixture"`, 1)
	_, err = scenario.Read(strings.NewReader(recording), "codex", scenario.Limits{})
	require.ErrorContains(t, err, "injected faults require synthetic provenance")
}

func TestReadRejectsUnknownOrUnsafeEnvelope(t *testing.T) {
	document := func(frames string) string {
		return `{"version":1,"provenance":"synthetic","frames":[` + frames + `]}`
	}
	for name, input := range map[string]string{
		"empty":                   "",
		"blank line":              unifiedFrame + "\n\n",
		"truncated":               unifiedFrame[:len(unifiedFrame)-2],
		"trailing":                unifiedFrame + " {}",
		"bad version":             `{"version":2,"provenance":"synthetic","frames":[` + unifiedFrame + `]}`,
		"missing version":         `{"provenance":"synthetic","frames":[` + unifiedFrame + `]}`,
		"duplicate version":       `{"version":1,"version":1,"frames":[` + unifiedFrame + `]}`,
		"unknown wrapper field":   `{"version":1,"provenance":"synthetic","extra":1,"frames":[` + unifiedFrame + `]}`,
		"bad provenance":          `{"version":1,"provenance":"real","frames":[` + unifiedFrame + `]}`,
		"source required":         `{"version":1,"provenance":"recording","frames":[` + unifiedFrame + `]}`,
		"wrong frames":            `{"version":1,"provenance":"synthetic","frames":{}}`,
		"negative timeout":        `{"version":1,"provenance":"synthetic","timeoutMs":-1,"frames":[` + unifiedFrame + `]}`,
		"unknown frame field":     document(strings.Replace(unifiedFrame, `"epoch":1`, `"epoch":1,"new":true`, 1)),
		"bad epoch":               document(strings.Replace(unifiedFrame, `"epoch":1`, `"epoch":2`, 1)),
		"missing epoch":           document(strings.Replace(unifiedFrame, `"epoch":1,`, ``, 1)),
		"fraction ordinal":        document(strings.Replace(unifiedFrame, `"ordinal":1`, `"ordinal":1.0`, 1)),
		"negative elapsed":        document(strings.Replace(unifiedFrame, `"elapsedMs":12`, `"elapsedMs":-1`, 1)),
		"bad channel":             document(strings.Replace(unifiedFrame, `"protocol"`, `"stdout"`, 1)),
		"bad direction":           document(strings.Replace(unifiedFrame, `"s2c"`, `"unknown"`, 1)),
		"scalar wire":             document(strings.Replace(unifiedFrame, native, `true`, 1)),
		"negative delay":          document(strings.Replace(unifiedFrame, `"wire":`, `"delayMs":-1,"wire":`, 1)),
		"disconnect with payload": document(strings.Replace(unifiedFrame, `"wire":`, `"disconnect":true,"wire":`, 1)),
		"disconnect client":       document(`{"ordinal":1,"direction":"c2s","channel":"protocol","epoch":1,"wire":null,"disconnect":true}`),
		"disconnect not final":    document(`{"ordinal":1,"direction":"disconnect","channel":"protocol"},` + strings.Replace(unifiedFrame, `"ordinal":1`, `"ordinal":2`, 1)),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := scenario.Read(strings.NewReader(input), "codex", scenario.Limits{})
			require.Error(t, err)
		})
	}
}

func TestBoundsAndProviderOrdering(t *testing.T) {
	for _, test := range []struct {
		name, provider, input string
		limits                scenario.Limits
		fail                  bool
	}{
		{"unknown provider", "unknown", unifiedFrame, scenario.Limits{}, true},
		{"negative limit", "codex", unifiedFrame, scenario.Limits{MaxFrames: -1}, true},
		{"source limit", "codex", unifiedFrame, scenario.Limits{MaxBytes: 8}, true},
		{"source exact", "codex", unifiedFrame, scenario.Limits{MaxBytes: int64(len(unifiedFrame))}, false},
		{"frame exclusive", "codex", unifiedFrame, scenario.Limits{MaxFrameBytes: int64(len(native))}, true},
		{"frame below bound", "codex", unifiedFrame, scenario.Limits{MaxFrameBytes: int64(len(native) + 1)}, false},
		{"codex gap accepted", "codex", strings.Replace(unifiedFrame, `"ordinal":1`, `"ordinal":7`, 1), scenario.Limits{}, false},
		{"claude gap rejected", "claude", strings.Replace(unifiedFrame, `"ordinal":1`, `"ordinal":7`, 1), scenario.Limits{}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := scenario.Read(strings.NewReader(test.input), test.provider, test.limits)
			if test.fail {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
