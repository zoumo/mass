package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestCommandWireContract(t *testing.T) {
	for _, tc := range []struct {
		args           []string
		method, params string
	}{
		{[]string{"status"}, "agentd/status", `{}`},
		{[]string{"rt", "types"}, "agentd/runtime/types", `{}`},
		{[]string{"runtime", "list"}, "agentd/runtime/list", `{}`},
		{[]string{"runtime", "get", "local"}, "agentd/runtime/status", `{"name":"local"}`},
		{[]string{"session", "new", "s", "--runtime-name", "remote", "--cwd", "/remote/only"}, "agentd/session/new", `{"sessionName":"s","spec":{"runtimeName":"remote","cwd":"/remote/only"}}`},
		{[]string{"session", "new", "s", "--runtime-name", "claude", "--cwd", "/workspace", "--prompt", "hello"}, "agentd/session/new", `{"sessionName":"s","spec":{"runtimeName":"claude","cwd":"/workspace"},"initialInput":"hello"}`},
		{[]string{"session", "list"}, "agentd/session/list", `{}`},
		{[]string{"session", "get", "s"}, "agentd/session/status", `{"sessionName":"s"}`},
		{[]string{"session", "prompt", "s", "hello\nworld"}, "agentd/session/prompt", `{"sessionName":"s","prompt":[{"type":"text","text":"hello\nworld"}]}`},
		{[]string{"session", "archive", "s"}, "agentd/session/archive", `{"sessionName":"s"}`},
		{[]string{"session", "delete", "s"}, "agentd/session/archive", `{"sessionName":"s"}`},
		{[]string{"session", "resume", "s"}, "agentd/session/resume", `{"sessionName":"s"}`},
		{[]string{"session", "cancel", "s"}, "agentd/session/cancel", `{"sessionName":"s"}`},
		{[]string{"session", "events", "s", "--after-seq", "9007199254740993", "--limit", "2"}, "agentd/session/get_event", `{"sessionName":"s","afterSeq":9007199254740993,"limit":2}`},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var req struct {
					Method string
					Params json.RawMessage
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				var got, want any
				// Decode numbers losslessly when checking uint64 cursors.
				d := json.NewDecoder(bytes.NewReader(req.Params))
				d.UseNumber()
				_ = d.Decode(&got)
				d = json.NewDecoder(strings.NewReader(tc.params))
				d.UseNumber()
				_ = d.Decode(&want)
				if req.Method != tc.method || !reflect.DeepEqual(got, want) {
					t.Errorf("got %s %s", req.Method, req.Params)
				}
				result := `{}`
				if strings.HasSuffix(tc.method, "/list") || tc.method == "agentd/runtime/types" {
					result = `[]`
				}
				if tc.method == "agentd/session/get_event" {
					result = `{"events":[{"seq":9007199254740994}],"nextSeq":9007199254740994}`
				}
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":"massctl","result":%s}`, result)
			}))
			defer srv.Close()
			root := NewRootCommand()
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs(append(tc.args, "--endpoint", srv.URL, "--json"))
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("calls=%d", calls)
			}
			if tc.method == "agentd/session/get_event" && out.String() != "{\"seq\":9007199254740994}\n" {
				t.Fatalf("event corrupted: %s", out.String())
			}
		})
	}
}

func TestInvalidCommandsDoNotContactServer(t *testing.T) {
	for _, args := range [][]string{
		{"runtime", "add", "r"}, {"runtime", "update", "r"}, {"session", "update", "s"},
		{"session", "new", "s"}, {"session", "new", "s", "--runtime-name", "r", "--cwd", "relative"},
		{"session", "events", "s", "--limit", "1001"}, {"session", "events", "s", "--after-seq", "-1"},
		{"session", "watch", "s", "--after-seq", "1"}, {"session", "prompt", "s", ""},
		{"status", "extra"}, {"status", "--timeout", "0s"}, {"status", "-o", "yaml"},
	} {
		root := NewRootCommand()
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetArgs(args)
		if err := root.Execute(); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}

func TestEventPageAndTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":"massctl","result":{"events":[],"nextSeq":8}}`)
	}))
	defer srv.Close()
	root := NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"session", "events", "s", "--page", "--json", "--endpoint", srv.URL})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if out.String() != "{\"events\":[],\"nextSeq\":8}\n" {
		t.Fatalf("lost cursor: %s", out.String())
	}
	out.Reset()
	o := options{output: "table"}
	if err := o.print(&out, "agentd/runtime/list", json.RawMessage(`[{"name":"r\nname","type":"codex","enabled":false,"status":{"phase":"pending"}}]`), false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "pending") || !strings.Contains(out.String(), "false") || strings.Contains(out.String(), "r\nname") {
		t.Fatalf("bad table: %s", out.String())
	}
}
