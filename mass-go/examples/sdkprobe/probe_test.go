// These probes use synthetic SDK-owned peers, never a real agent or account.
package sdkprobe_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	claude "github.com/zoumo/agent-sdk-go/claude"
	codex "github.com/zoumo/agent-sdk-go/codex"
	ct "github.com/zoumo/agent-sdk-go/codex/types"
)

type ProbeSuite struct{ suite.Suite }

var bins map[string]string

// Build by public import path from this consumer module, using its pinned SDK.
func TestMain(m *testing.M) {
	mainCtx, mainCancel := context.WithTimeout(context.Background(), 3*time.Minute)
	dir, err := os.MkdirTemp("", "mass-sdk-probe-")
	if err != nil {
		panic(err)
	}
	bins = make(map[string]string)
	code := 1
	func() {
		defer func() {
			// SDK Close waits for its owned child. Independently read back the process
			// table before deleting binaries so leaked children cannot hide in cleanup.
			output, err := exec.CommandContext(mainCtx, "ps", "-axo", "pid=,args=").Output()
			if err != nil || bytes.Contains(output, []byte(dir+"/")) {
				fmt.Fprintln(os.Stderr, "FAIL process cleanup readback", err)
				code = 1
			} else {
				fmt.Println("PASS cleanup: no owned peer process remains")
			}
			if err := os.RemoveAll(dir); err != nil {
				code = 1
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				code = 1
			} else {
				fmt.Println("PASS cleanup: temporary binaries removed")
			}
		}()
		// Refuse a workspace/module replacement or an accidentally upgraded SDK.
		output, err := exec.CommandContext(mainCtx, "go", "list", "-m", "-json", "github.com/zoumo/agent-sdk-go").Output()
		var module struct {
			Version string
			Replace *json.RawMessage
		}
		if err != nil || json.Unmarshal(output, &module) != nil || module.Version != "v0.0.0-20261018163950-6dbf3e9c8264" || module.Replace != nil {
			fmt.Fprintln(os.Stderr, "FAIL: probe requires the pinned SDK without replacement", err)
			return
		}
		fmt.Println("SDK github.com/zoumo/agent-sdk-go", module.Version)
		for _, native := range []string{"codex", "claude"} {
			bins[native] = filepath.Join(dir, "mock-"+native)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			cmd := exec.CommandContext(ctx, "go", "build", "-o", bins[native], "github.com/zoumo/agent-sdk-go/"+native+"/cmd/mock")
			output, err := cmd.CombinedOutput()
			cancel()
			if err != nil {
				fmt.Fprintf(os.Stderr, "build %s: %v\n%s", native, err, output)
				return
			}
		}
		code = m.Run()
	}()
	mainCancel()
	os.Exit(code)
}

// Every owned directory is removed and read back even after a failed assertion.
func workDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(dir))
		_, err := os.Stat(dir)
		require.True(t, os.IsNotExist(err), "temporary work directory still exists")
	})
	return dir
}

func scenario(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("testdata", name+".json"))
	require.NoError(t, err)
	return path
}

func deadline(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func openCodex(t *testing.T, ctx context.Context, name string) *codex.Client {
	t.Helper()
	c, err := codex.NewStdio(ctx, codex.StdioOptions{
		Executable: bins["codex"], Args: []string{"app-server"},
		Env: []string{"CODEX_MOCK_SCENARIO=" + scenario(t, name)}, Dir: workDir(t),
		Initialize: ct.InitializeParams{ClientInfo: ct.ClientInfo{Name: "probe", Version: "1"}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, c.Close())
		select {
		case <-c.Done():
		default:
			t.Error("Codex still active after Close")
		}
	})
	return c
}

func (probeSuite *ProbeSuite) TestCodex() {
	for _, mode := range []string{"basic", "question"} {
		probeSuite.Run(mode, func() {
			t := probeSuite.T()
			ctx := deadline(t)
			c := openCodex(t, ctx, "codex-"+mode)
			thread, err := c.StartThread(ctx, ct.ThreadStartParams{})
			require.NoError(t, err)
			require.Equal(t, "codex-session", thread.Thread.ID)
			require.Equal(t, "codex-session", thread.Thread.SessionID)
			t.Log("PASS no-input native thread identity: codex-session (synthetic)")
			sub, err := c.Subscribe(ctx, thread.Thread.ID, ct.SubscriptionOptions{})
			require.NoError(t, err)
			defer sub.Abort()
			var params ct.TurnStartParams
			require.NoError(t, json.Unmarshal([]byte(`{"threadId":"codex-session","input":[{"type":"text","text":"hello","text_elements":[]}]}`), &params))
			turn, err := c.StartTurn(ctx, params)
			require.NoError(t, err)
			require.Equal(t, "turn-1", turn.Turn.ID)
			t.Log("PASS independent input turn acknowledged")
			expected := []struct{ id, method, answer string }{
				{"command-approval", "item/commandExecution/requestApproval", `{"decision":"accept"}`},
				{"file-approval", "item/fileChange/requestApproval", `{"decision":"accept"}`},
			}
			if mode == "question" {
				expected = []struct{ id, method, answer string }{{"question-1", "item/tool/requestUserInput", `{"answers":{"format":{"answers":["json"]}}}`}}
			}
			for _, want := range expected {
				select {
				case request, ok := <-c.Requests():
					require.True(t, ok)
					require.Equal(t, codex.StringID(want.id), request.ID())
					require.Equal(t, want.method, request.Method())
					var metadata struct{ ThreadID, TurnID, ItemID string }
					require.NoError(t, json.Unmarshal(request.Params(), &metadata))
					require.Equal(t, "codex-session", metadata.ThreadID)
					require.Equal(t, "turn-1", metadata.TurnID)
					wantItem := "tool-1"
					if mode == "question" {
						wantItem = "question-tool"
					}
					require.Equal(t, wantItem, metadata.ItemID)
					require.NoError(t, request.Handle().Answer(ctx, json.RawMessage(want.answer)))
					t.Logf("PASS original handle answered: %s %s", want.id, want.method)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			seen := map[string]bool{}
			for !seen["turn/completed"] {
				select {
				case event, ok := <-sub.Events():
					require.True(t, ok)
					if event.Method != "item/completed" && event.Method != "turn/completed" {
						continue
					}
					var correlation struct{ ThreadID, TurnID string }
					require.NoError(t, json.Unmarshal(event.Params, &correlation))
					require.Equal(t, "codex-session", correlation.ThreadID)
					if event.Method == "turn/completed" {
						var completed ct.TurnCompletedNotification
						require.NoError(t, json.Unmarshal(event.Params, &completed))
						require.Equal(t, "turn-1", completed.Turn.ID)
						require.Equal(t, ct.TurnStatus("completed"), completed.Turn.Status)
					}
					if event.Method == "item/completed" {
						require.Equal(t, "turn-1", correlation.TurnID)
						var value struct {
							Item struct {
								ID, Type, Text, AggregatedOutput string
								ExitCode                         int
							}
						}
						require.NoError(t, json.Unmarshal(event.Params, &value))
						switch value.Item.Type {
						case "agentMessage":
							require.Equal(t, "message-1", value.Item.ID)
							require.Equal(t, "complete answer", value.Item.Text)
							seen["message"] = true
						case "commandExecution":
							require.Equal(t, "tool-1", value.Item.ID)
							require.Equal(t, "synthetic output", value.Item.AggregatedOutput)
							require.Zero(t, value.Item.ExitCode)
							seen["tool"] = true
						}
					}
					seen[event.Method] = true
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			require.True(t, seen["tool"])
			require.True(t, seen["message"])
			// The reply can only arrive after the peer validated every original answer.
			_, err = c.InterruptTurn(ctx, ct.TurnInterruptParams{ThreadID: "codex-session", TurnID: "turn-1"})
			require.NoError(t, err)
			require.NoError(t, c.Close())
			require.Zero(t, c.Diagnostics().StderrBytes)
			require.Zero(t, c.Diagnostics().UnmatchedResponses)
			t.Log("PASS complete message, tool result, round-trip barrier and SDK Close")
		})
	}
}

func (probeSuite *ProbeSuite) TestClaude() {
	for _, mode := range []string{"basic", "question", "no-identity"} {
		probeSuite.Run(mode, func() {
			t := probeSuite.T()
			ctx := deadline(t)
			requests := make(chan *claude.PermissionRequest, 1)
			c, err := claude.Open(ctx, claude.Options{CLIPath: bins["claude"], WorkDir: workDir(t), Env: []string{"CLAUDE_MOCK_SCENARIO=" + scenario(t, "claude-"+mode)}, PermissionHandler: func(p *claude.PermissionRequest) {
				select {
				case requests <- p:
				case <-p.Context().Done():
				}
			}})
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(t, c.Close())
				select {
				case <-c.Done():
				default:
					t.Error("Claude still active after Close")
				}
			})
			if mode == "no-identity" {
				// No system init is scripted until real input. Observe absence without
				// sending an empty prompt or manufacturing an identifier.
				wait, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
				for {
					_, err = c.Receive(wait)
					if err != nil {
						break
					}
				}
				cancel()
				require.ErrorIs(t, err, context.DeadlineExceeded)
				id, known := c.SessionID()
				require.False(t, known)
				require.Empty(t, id)
				t.Log("PASS no-input missing identity explicitly observed; creation must fail if identity is required")
			} else {
				for {
					event, err := c.Receive(ctx)
					require.NoError(t, err)
					if value, ok := event.(claude.Event); ok && value.Type == "system" && value.Subtype == "init" {
						require.Equal(t, "claude-session", value.SessionID)
						break
					}
				}
				id, known := c.SessionID()
				require.True(t, known)
				require.Equal(t, "claude-session", id)
				t.Log("PASS no-input native system init identity: claude-session (synthetic only)")
			}
			require.NoError(t, c.Send(ctx, "hello"))
			t.Log("PASS subsequent independent nonempty input sent")
			select {
			case p := <-requests:
				wantID, wantTool, wantUse := "permission-1", "Bash", "tool-1"
				answer := p.Input() // Approvals preserve original tool input.
				if mode == "question" {
					wantID, wantTool, wantUse = "question-1", "AskUserQuestion", "question-tool"
					// Explicit user answer, separate from the unconditional permission policy.
					answer = json.RawMessage(`{"answers":{"format":"json"}}`)
				}
				require.Equal(t, wantID, p.RequestID())
				require.Equal(t, wantTool, p.ToolName())
				require.Equal(t, wantUse, p.ToolUseID())
				require.NoError(t, p.Answer(ctx, claude.PermissionAnswer{Behavior: claude.PermissionAllow, UpdatedInput: answer}))
				t.Logf("PASS original handle answered: %s %s %s", p.RequestID(), p.ToolName(), p.ToolUseID())
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			seen := map[string]bool{}
			for !seen["result"] {
				event, err := c.Receive(ctx)
				require.NoError(t, err)
				value, ok := event.(claude.Event)
				if !ok {
					continue
				}
				if value.Type == "assistant" || value.Type == "user" || value.Type == "result" {
					require.Equal(t, "claude-session", value.SessionID)
				}
				if value.Type == "assistant" && value.Assistant != nil {
					for _, block := range value.Assistant.Content.Blocks {
						if block.Type == "text" {
							require.JSONEq(t, `{"type":"text","text":"complete answer"}`, string(block.Raw))
							seen["message"] = true
						}
						if block.Type == "tool_use" {
							require.JSONEq(t, `{"type":"tool_use","id":"tool-1","name":"Bash","input":{"command":"echo synthetic"}}`, string(block.Raw))
							seen["tool-use"] = true
						}
					}
				}
				if value.Type == "user" && value.User != nil {
					require.JSONEq(t, `{"type":"user","session_id":"claude-session","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":"synthetic output"}]}}`, string(value.Raw))
					seen["tool-result"] = true
				}
				if value.Result != nil {
					require.False(t, value.Result.IsError)
					require.Equal(t, "complete answer", value.Result.Result)
					seen["result"] = true
				}
			}
			require.True(t, seen["message"])
			require.True(t, seen["tool-use"])
			require.True(t, seen["tool-result"])
			require.NoError(t, c.Interrupt(ctx)) // Strict peer barrier after all replies.
			require.NoError(t, c.Close())
			t.Log("PASS complete message, tool result, round-trip barrier and SDK Close")
		})
	}
}

// Independently verify the exact CLI exit contract with complete native input.
// This also checks failures are reaped, rather than treating shutdown as success.
func (probeSuite *ProbeSuite) TestPeerExit() {
	for _, native := range []string{"codex", "claude"} {
		for _, mode := range []string{"complete", "mismatch", "incomplete"} {
			probeSuite.Run(native+"/"+mode, func() {
				t := probeSuite.T()
				ctx := deadline(t)
				var fixture struct {
					Frames []struct {
						Direction  string
						Wire, Data json.RawMessage
					}
				}
				b, err := os.ReadFile(scenario(t, native+"-basic"))
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(b, &fixture))
				var input bytes.Buffer
				for _, f := range fixture.Frames {
					if f.Direction == "client" {
						require.NoError(t, json.Compact(&input, f.Data))
						input.WriteByte('\n')
					}
					if f.Direction == "c2s" {
						require.NoError(t, json.Compact(&input, f.Wire))
						input.WriteByte('\n')
					}
				}
				if mode == "mismatch" {
					input.Reset()
					input.WriteString("{}\n")
				}
				if mode == "incomplete" {
					input.Reset()
				}
				cmd := exec.CommandContext(ctx, bins[native])
				if native == "codex" {
					cmd.Args = append(cmd.Args, "app-server")
				}
				cmd.Dir = workDir(t)
				cmd.Env = []string{strings.ToUpper(native) + "_MOCK_SCENARIO=" + scenario(t, native+"-basic")}
				cmd.Stdin = &input
				var stderr, stdout bytes.Buffer
				cmd.Stderr = &stderr
				cmd.Stdout = &stdout
				err = cmd.Run()
				require.NoError(t, ctx.Err())
				require.True(t, cmd.ProcessState.Exited(), "child must be reaped")
				if mode == "complete" {
					require.NoError(t, err, stderr.String())
					require.Empty(t, stderr.String())
				} else {
					require.Error(t, err)
					require.Equal(t, 1, cmd.ProcessState.ExitCode())
					require.NotEmpty(t, stderr.String())
				}
				t.Logf("PASS strict scenario %s, exit=%d, child waited/reaped", mode, cmd.ProcessState.ExitCode())
			})
		}
	}
}

// Exercise SDK-owned initialization failure, not just direct peer failures.
// Each strict scenario expects initialization; malformed script-side fields
// force the SDK open to fail. TestMain reads back process and directory cleanup.
func (probeSuite *ProbeSuite) TestSDKInitializationFailure() {
	for _, native := range []string{"codex", "claude"} {
		probeSuite.Run(native, func() {
			t := probeSuite.T()
			data, err := os.ReadFile(scenario(t, native+"-basic"))
			require.NoError(t, err)
			var fixture map[string]any
			require.NoError(t, json.Unmarshal(data, &fixture))
			first := fixture["frames"].([]any)[0].(map[string]any)
			if native == "codex" {
				first["wire"].(map[string]any)["params"] = map[string]any{"unexpected": true}
			} else {
				first["data"].(map[string]any)["request"] = map[string]any{"subtype": "unexpected"}
			}
			data, err = json.Marshal(fixture)
			require.NoError(t, err)
			path := filepath.Join(workDir(t), "mismatch.json")
			require.NoError(t, os.WriteFile(path, data, 0o600))
			ctx := deadline(t)
			if native == "codex" {
				c, err := codex.NewStdio(ctx, codex.StdioOptions{
					Executable: bins[native], Args: []string{"app-server"}, Dir: workDir(t),
					Env:        []string{"CODEX_MOCK_SCENARIO=" + path},
					Initialize: ct.InitializeParams{ClientInfo: ct.ClientInfo{Name: "probe", Version: "1"}},
				})
				if c != nil {
					t.Cleanup(func() { _ = c.Close() })
				}
				require.Error(t, err)
			} else {
				c, err := claude.Open(ctx, claude.Options{
					CLIPath: bins[native], WorkDir: workDir(t), Env: []string{"CLAUDE_MOCK_SCENARIO=" + path},
				})
				if c != nil {
					t.Cleanup(func() { _ = c.Close() })
				}
				require.Error(t, err)
			}
			t.Log("PASS SDK initialization mismatch returned an error; cleanup readback follows")
		})
	}
}

func TestProbeSuite(t *testing.T) { suite.Run(t, new(ProbeSuite)) }
