package replay

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/internal/testpeer/rawjson"
	"github.com/zoumo/mass/mass-go/internal/testpeer/scenario"
)

func scripted(provider string, directions []scenario.Direction, wires []string) *scenario.Document {
	frames := make([]scenario.Frame, len(wires))
	for i, wire := range wires {
		frames[i] = scenario.Frame{Ordinal: int64(i + 1), Direction: directions[i], Channel: "protocol", Epoch: 1, Wire: mustParse(wire)}
	}
	return &scenario.Document{Provider: provider, Provenance: "synthetic", Frames: frames}
}

func mustParse(wire string) rawjson.Value {
	value, err := rawjson.Parse([]byte(wire))
	if err != nil {
		panic(err)
	}
	return value
}

func runScript(t *testing.T, provider string, directions []scenario.Direction, wires []string, input string) (string, error) {
	t.Helper()
	var output bytes.Buffer
	err := Run(context.Background(), scripted(provider, directions, wires), scenario.Limits{}, strings.NewReader(input), &output)
	return output.String(), err
}

func TestCodexRebindsLiveIDsAndTracksReverseRequests(t *testing.T) {
	directions := []scenario.Direction{
		scenario.ClientToServer,
		scenario.ClientToServer,
		scenario.ServerToClient,
		scenario.ServerToClient,
		scenario.ServerToClient,
		scenario.ClientToServer,
		scenario.ServerToClient,
	}
	wires := []string{
		`{"id":1,"method":"one","params":{"array":[1,"x",null]}}`,
		`{"id":2,"method":"two"}`,
		`{"method":"early","params":{"opaqueId":9007199254740993}}`,
		`{"id":2,"error":{"code":-32000,"message":"synthetic"}}`,
		`{"id":1,"method":"question","params":{"threadId":"child"}}`,
		`{"id":1,"result":{"answer":"blue"}}`,
		`{"id":1,"result":{"live":true}}`,
	}
	input := "{\"params\":{\"array\":[1,\"x\",null]},\"method\":\"one\",\"id\":\"live\"}\r\n" +
		"{\"id\":9007199254740993,\"method\":\"two\"}\n" +
		"{\"id\":1,\"result\":{\"answer\":\"blue\"}}\n"
	output, err := runScript(t, "codex", directions, wires, input)
	require.NoError(t, err)
	assert.Equal(t, strings.Join([]string{
		wires[2],
		`{"id":9007199254740993,"error":{"code":-32000,"message":"synthetic"}}`,
		wires[4],
		`{"id":"live","result":{"live":true}}`,
		"",
	}, "\n"), output)
}

func TestCodexResolvedRequestReleasesPendingState(t *testing.T) {
	directions := []scenario.Direction{scenario.ServerToClient, scenario.ServerToClient}
	wires := []string{
		`{"id":"q","method":"approval","params":{"threadId":"child"}}`,
		`{"method":"serverRequest/resolved","params":{"threadId":"child","requestId":"q"}}`,
	}
	output, err := runScript(t, "codex", directions, wires, "")
	require.NoError(t, err)
	assert.Equal(t, strings.Join(append(append([]string{}, wires...), ""), "\n"), output)
}

func TestCodexStrictFailures(t *testing.T) {
	request := `{"id":1,"method":"one","params":{"x":1}}`
	response := `{"id":1,"result":{}}`
	reverse := `{"id":"q","method":"approval","params":{"threadId":"child"}}`
	answer := `{"id":"q","result":{}}`
	resolved := `{"method":"serverRequest/resolved","params":{"threadId":"child","requestId":"q"}}`
	baseInput := `{"id":"live","method":"one","params":{"x":1}}` + "\n"
	for _, test := range []struct {
		name       string
		directions []scenario.Direction
		wires      []string
		input      string
		diagnostic string
	}{
		{"early EOF", []scenario.Direction{scenario.ClientToServer, scenario.ServerToClient}, []string{request, response}, "", "early client EOF"},
		{"method mismatch", []scenario.Direction{scenario.ClientToServer}, []string{request}, `{"id":9,"method":"other"}` + "\n", "client mismatch"},
		{"parameter mismatch", []scenario.Direction{scenario.ClientToServer}, []string{request}, `{"id":9,"method":"one","params":{"x":2}}` + "\n", "client mismatch"},
		{"missing ID", []scenario.Direction{scenario.ClientToServer}, []string{request}, `{"method":"one","params":{"x":1}}` + "\n", "invalid protocol ID"},
		{"null ID", []scenario.Direction{scenario.ClientToServer}, []string{request}, `{"id":null,"method":"one"}` + "\n", "invalid protocol ID"},
		{"extra field", []scenario.Direction{scenario.ClientToServer}, []string{request}, `{"id":9,"method":"one","params":{"x":1},"extra":null}` + "\n", "client mismatch"},
		{"extra byte", []scenario.Direction{scenario.ClientToServer, scenario.ServerToClient}, []string{request, response}, baseInput + " ", "extra client byte"},
		{"duplicate live ID", []scenario.Direction{scenario.ClientToServer, scenario.ServerToClient, scenario.ClientToServer}, []string{request, response, `{"id":2,"method":"two"}`}, `{"id":"live","method":"one","params":{"x":1}}` + "\n" + `{"id":"live","method":"two"}` + "\n", "duplicate client ID"},
		{"pending client", []scenario.Direction{scenario.ClientToServer}, []string{request}, baseInput, "unfinished protocol requests"},
		{"pending reverse", []scenario.Direction{scenario.ServerToClient}, []string{reverse}, "", "unfinished protocol requests"},
		{"wrong reverse ID", []scenario.Direction{scenario.ServerToClient, scenario.ClientToServer}, []string{reverse, answer}, `{"id":"wrong","result":{}}` + "\n", "client mismatch"},
		{"late answer", []scenario.Direction{scenario.ServerToClient, scenario.ServerToClient, scenario.ClientToServer}, []string{reverse, resolved, answer}, answer + "\n", "unknown or duplicate reverse answer"},
		{"unknown answer", []scenario.Direction{scenario.ClientToServer}, []string{answer}, answer + "\n", "unknown or duplicate reverse answer"},
		{"duplicate answer", []scenario.Direction{scenario.ServerToClient, scenario.ClientToServer, scenario.ClientToServer}, []string{reverse, answer, answer}, strings.Repeat(answer+"\n", 2), "unknown or duplicate reverse answer"},
		{"nonobject client", []scenario.Direction{scenario.ClientToServer}, []string{request}, "[1,2]\n", "must be an object"},
		{"trailing JSON", []scenario.Direction{scenario.ClientToServer}, []string{request}, "{} {}\n", "invalid JSON"},
		{"duplicate JSON key", []scenario.Direction{scenario.ClientToServer}, []string{request}, `{"id":1,"id":2}` + "\n", "duplicate JSON member"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := runScript(t, "codex", test.directions, test.wires, test.input)
			require.ErrorContains(t, err, test.diagnostic)
			assert.Contains(t, err.Error(), "ordinal ")
		})
	}
}

func TestClaudeRebindsControlRequestsAndSettlesCancellation(t *testing.T) {
	directions := []scenario.Direction{
		scenario.ClientToServer,
		scenario.ClientToServer,
		scenario.ServerToClient,
		scenario.ServerToClient,
		scenario.ServerToClient,
		scenario.ServerToClient,
		scenario.ClientToServer,
		scenario.ServerToClient,
		scenario.ServerToClient,
	}
	wires := []string{
		`{"type":"control_request","request_id":"old","request":{"request_id":"literal"}}`,
		`{"type":"control_request","request_id":2,"request":{}}`,
		`{"type":"notice","unknown":{"number":0.123456789012345678901234567890}}`,
		`{"type":"control_response","response":{"request_id":2,"subtype":"error","error":"synthetic"}}`,
		`{ "type" : "control_response", "response" : { "request_id" : "old", "payload":{"request_id":"old","text":"escaped \"request_id\": \"old\""}} }`,
		`{"type":"control_request","request_id":"permission","request":{}}`,
		`{"type":"control_response","response":{"request_id":"permission","response":{"behavior":"allow"}}}`,
		`{"type":"control_request","request_id":"cancel","request":{}}`,
		`{"type":"control_cancel_request","request_id":"cancel"}`,
	}
	input := `{"type":"control_request","request_id":"dynamic","request":{"request_id":"literal"}}` + "\n" +
		`{"type":"control_request","request_id":9007199254740993,"request":{}}` + "\n" +
		wires[6] + "\n"
	output, err := runScript(t, "claude", directions, wires, input)
	require.NoError(t, err)
	assert.Equal(t, strings.Join([]string{
		wires[2],
		strings.Replace(wires[3], `"request_id":2`, `"request_id":9007199254740993`, 1),
		strings.Replace(wires[4], `"request_id" : "old"`, `"request_id" : "dynamic"`, 1),
		wires[5],
		wires[7],
		wires[8],
		"",
	}, "\n"), output)
}

func TestClaudeStrictFailures(t *testing.T) {
	request := `{"type":"control_request","request_id":"old","request":{"request_id":"literal"}}`
	response := `{"type":"control_response","response":{"request_id":"old","payload":{"request_id":"literal"}}}`
	reverse := `{"type":"control_request","request_id":"permission","request":{}}`
	answer := `{"type":"control_response","response":{"request_id":"permission","response":{}}}`
	cancel := `{"type":"control_cancel_request","request_id":"permission"}`
	input := strings.Replace(request, `"request_id":"old"`, `"request_id":"dynamic"`, 1) + "\n"
	for _, test := range []struct {
		name       string
		directions []scenario.Direction
		wires      []string
		input      string
	}{
		{"pending client", []scenario.Direction{scenario.ClientToServer}, []string{request}, input},
		{"pending reverse", []scenario.Direction{scenario.ServerToClient}, []string{reverse}, ""},
		{"unmatched response", []scenario.Direction{scenario.ServerToClient}, []string{response}, ""},
		{"unknown cancellation", []scenario.Direction{scenario.ServerToClient}, []string{cancel}, ""},
		{"wrong ID type", []scenario.Direction{scenario.ClientToServer, scenario.ServerToClient}, []string{request, response}, strings.Replace(input, `"dynamic"`, `1`, 1)},
		{"empty ID", []scenario.Direction{scenario.ClientToServer, scenario.ServerToClient}, []string{request, response}, strings.Replace(input, `"dynamic"`, `""`, 1)},
		{"payload mismatch", []scenario.Direction{scenario.ClientToServer, scenario.ServerToClient}, []string{request, response}, strings.Replace(input, "literal", "changed", 1)},
		{"wrong reverse ID", []scenario.Direction{scenario.ServerToClient, scenario.ClientToServer}, []string{reverse, answer}, strings.Replace(answer, "permission", "wrong", 1) + "\n"},
		{"duplicate recorded ID", []scenario.Direction{scenario.ClientToServer, scenario.ServerToClient, scenario.ClientToServer}, []string{request, response, request}, input + strings.Replace(input, "dynamic", "second", 1)},
		{"duplicate live ID", []scenario.Direction{scenario.ClientToServer, scenario.ServerToClient, scenario.ClientToServer}, []string{request, response, strings.Replace(request, `"old"`, `"second"`, 1)}, input + input},
		{"answer after cancellation", []scenario.Direction{scenario.ServerToClient, scenario.ServerToClient, scenario.ClientToServer}, []string{reverse, cancel, answer}, answer + "\n"},
		{"missing response ID", []scenario.Direction{scenario.ServerToClient}, []string{`{"type":"control_response","response":{}}`}, ""},
		{"nonobject response", []scenario.Direction{scenario.ServerToClient}, []string{`{"type":"control_response","response":[]}`}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := runScript(t, "claude", test.directions, test.wires, test.input)
			require.Error(t, err)
		})
	}
}

func TestFullObjectMatchingAndNumberLexemes(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		for _, payload := range []string{`{"x":true}`, `{"x":9007199254740993}`, `{"x":1.0}`, `{"x":[1,2]}`, `{"x":"1"}`} {
			t.Run(provider+payload, func(t *testing.T) {
				_, err := runScript(t, provider, []scenario.Direction{scenario.ClientToServer}, []string{payload}, `{"x":1}`+"\n")
				require.ErrorContains(t, err, "client mismatch")
			})
		}
		wire := `{ "type":"notice", "number":0.123456789012345678901234567890, "opaqueId":9007199254740993, "request_id":"literal" }`
		output, err := runScript(t, provider, []scenario.Direction{scenario.ServerToClient}, []string{wire}, "")
		require.NoError(t, err)
		assert.Equal(t, wire+"\n", output)
		_, err = runScript(t, provider, []scenario.Direction{scenario.ClientToServer}, []string{`{"a":"a","b":[1,2]}`}, "{\"b\":[1,2],\"a\":\"\\u0061\"}\n")
		require.NoError(t, err)
		_, err = runScript(t, provider, []scenario.Direction{scenario.ClientToServer}, []string{`{"a":"a","b":[1,2]}`}, "{\"a\":\"a\",\"b\":[2,1]}\n")
		require.Error(t, err)
	}
}

func TestDelayDisconnectContextAndWriteFailure(t *testing.T) {
	disconnect := &scenario.Document{
		Provider: "codex",
		Frames: []scenario.Frame{
			{Ordinal: 1, Direction: scenario.ServerToClient, Channel: "protocol", Epoch: 1, Delay: 20 * time.Millisecond, Wire: mustParse(`{}`)},
			{Ordinal: 2, Direction: scenario.ServerToClient, Channel: "protocol", Epoch: 1, Disconnect: true},
		},
	}
	var output bytes.Buffer
	start := time.Now()
	err := Run(context.Background(), disconnect, scenario.Limits{}, strings.NewReader(""), &output)
	require.ErrorIs(t, err, ErrDisconnected)
	assert.GreaterOrEqual(t, time.Since(start), 20*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	err = Run(ctx, disconnect, scenario.Limits{}, strings.NewReader(""), io.Discard)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	writeFailure := &scenario.Document{
		Provider: "codex",
		Frames: []scenario.Frame{
			{Ordinal: 1, Direction: scenario.ServerToClient, Channel: "protocol", Epoch: 1, Wire: mustParse(`{}`)},
		},
	}
	err = Run(context.Background(), writeFailure, scenario.Limits{}, strings.NewReader(""), shortOutput{})
	require.ErrorIs(t, err, io.ErrShortWrite)
}

type shortOutput struct{}

func (shortOutput) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestExtraClientByteFailsAfterCompleteConsumption(t *testing.T) {
	directions := []scenario.Direction{scenario.ClientToServer, scenario.ServerToClient}
	wires := []string{`{"id":1,"method":"one"}`, `{"id":1,"result":{}}`}
	_, err := runScript(t, "codex", directions, wires, `{"id":"live","method":"one"}`+"\n"+" ")
	require.ErrorContains(t, err, "extra client byte")
}
