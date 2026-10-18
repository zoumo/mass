package codex

import (
	"encoding/json"
	"fmt"

	"github.com/zoumo/agent-sdk-go/codex/types"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

func toolEvent(n types.Event, id string) rt.Event {
	e := rt.Event{Kind: rt.Unknown, SessionID: id, Diagnostic: diagnostic(n.Method, n.Params)}
	var d struct {
		TurnID string          `json:"turnId"`
		Item   json.RawMessage `json:"item"`
	}
	var h struct{ ID, Type string }
	fail := func() rt.Event {
		e.Kind = rt.Failure
		e.Error = &rt.SafeError{Category: "decode_error", Scope: "session"}
		return e
	}
	if json.Unmarshal(n.Params, &d) != nil || json.Unmarshal(d.Item, &h) != nil {
		return fail()
	}
	e.WorkRef = d.TurnID
	switch h.Type {
	case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall":
	default:
		e.Diagnostic.Classification = "unknown_variant"
		e.Diagnostic.CallRef = h.ID
		return e
	}
	if n.DecodeError != nil || h.ID == "" {
		return fail()
	}
	var item types.ThreadItem
	if json.Unmarshal(d.Item, &item) != nil {
		return fail()
	}
	var f map[string]json.RawMessage
	if json.Unmarshal(d.Item, &f) != nil {
		return fail()
	}
	t := &rt.ToolPayload{CallRef: h.ID, Category: "unknown", State: "announced", Input: rt.ContentSet{Availability: "missing", Completeness: "unknown"}, Output: rt.ContentSet{Availability: "missing", Completeness: "unknown"}, OutcomeEvidence: "call_observed"}
	e.Kind = rt.ToolAnnounced
	status := ""
	if n.Method == "item/completed" {
		e.Kind = rt.ToolCompleted
		t.State = "unknown"
		t.OutcomeEvidence = "native_item_completed"
	}
	switch {
	case item.CommandExecution != nil:
		v := item.CommandExecution
		t.Name = "commandExecution"
		t.Category = "execute"
		t.DisplayLabel = &v.Command
		input, _ := json.Marshal(struct {
			Command string          `json:"command"`
			Cwd     string          `json:"cwd"`
			Actions json.RawMessage `json:"actions"`
		}{v.Command, string(v.Cwd), f["commandActions"]})
		t.Input = rt.JSONValueContent(input, "completed", "complete")
		t.Locations = []rt.ToolLocation{{Path: string(v.Cwd)}}
		t.Output = rt.JSONContent(f["aggregatedOutput"], "completed", "complete")
		if len(f["aggregatedOutput"]) > 0 && string(f["aggregatedOutput"]) != "null" {
			var text string
			if json.Unmarshal(f["aggregatedOutput"], &text) != nil {
				return fail()
			}
			t.Output = rt.TextContent(text, "completed", "complete", nil)
		}
		if raw := f["exitCode"]; len(raw) > 0 && string(raw) != "null" {
			var code int32
			if json.Unmarshal(raw, &code) != nil {
				return fail()
			}
			t.ExitCode = &code
		}
		status = string(v.Status)
	case item.FileChange != nil:
		v := item.FileChange
		t.Name = "fileChange"
		t.Category = "write"
		status = string(v.Status)
		t.Input = rt.JSONValueContent(f["changes"], "completed", "complete")
		for _, change := range v.Changes {
			t.Locations = append(t.Locations, rt.ToolLocation{Path: change.Path})
		}
	case item.MCPToolCall != nil:
		v := item.MCPToolCall
		t.Name = v.Tool
		t.Namespace = &v.Server
		status = string(v.Status)
		t.Input = rt.JSONContent(f["arguments"], "completed", "complete")
		t.Output = rt.JSONContent(f["result"], "completed", "complete")
		if raw := f["result"]; len(raw) > 0 && string(raw) != "null" {
			var result map[string]json.RawMessage
			if json.Unmarshal(raw, &result) != nil {
				return fail()
			}
			var err error
			t.Output, err = toolBlocks(result["content"])
			if err != nil {
				return fail()
			}
			if structured, ok := result["structuredContent"]; ok {
				c := rt.JSONValueContent(structured, "completed", "complete")
				t.Content = &c
			}
		}
		if raw := f["error"]; len(raw) > 0 && string(raw) != "null" {
			t.State = "failed"
			t.OutcomeEvidence = "native_error"
		}
	case item.DynamicToolCall != nil:
		v := item.DynamicToolCall
		t.Name = v.Tool
		status = string(v.Status)
		if raw := f["namespace"]; len(raw) > 0 && string(raw) != "null" {
			var namespace string
			if json.Unmarshal(raw, &namespace) != nil {
				return fail()
			}
			t.Namespace = &namespace
		}
		t.Input = rt.JSONContent(f["arguments"], "completed", "complete")
		var err error
		t.Output, err = toolBlocks(f["contentItems"])
		if err != nil {
			return fail()
		}
		if string(f["success"]) == "false" {
			t.State = "failed"
			t.OutcomeEvidence = "native_success_false"
		}
	}
	if raw := f["durationMs"]; len(raw) > 0 && string(raw) != "null" {
		var duration int64
		if json.Unmarshal(raw, &duration) != nil {
			return fail()
		}
		t.DurationMS = &duration
	}
	// Explicit terminal status/result evidence takes precedence over the native
	// notification label. A bare in-progress call remains only announced.
	if n.Method == "item/completed" || status == "completed" || status == "failed" || status == "declined" || status == "cancelled" || t.State == "failed" || (t.ExitCode != nil && *t.ExitCode != 0) {
		e.Kind = rt.ToolCompleted
		if t.OutcomeEvidence == "call_observed" {
			t.OutcomeEvidence = "native_status"
		}
		if t.State == "announced" {
			t.State = "unknown"
		}
		switch status {
		case "completed":
			if t.State != "failed" {
				t.State = "completed"
			}
		case "failed", "declined":
			t.State = "failed"
			t.OutcomeEvidence = "native_status"
		case "cancelled":
			t.State = "cancelled"
		}
		if t.ExitCode != nil && *t.ExitCode != 0 {
			t.State = "failed"
			t.OutcomeEvidence = "native_exit_code"
		}
	}
	if e.Kind == rt.ToolAnnounced {
		t.Output.Completeness = "partial"
	}
	if t.Name == "" {
		return fail()
	}
	e.Tool = t
	return e
}

// Native content arrays retain their order; unsupported blocks remain explicit
// and never become a stringified substitute for supported content.
func toolBlocks(raw json.RawMessage) (rt.ContentSet, error) {
	c := rt.JSONContent(raw, "completed", "complete")
	if c.Availability == "missing" || c.Availability == "null" {
		return c, nil
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return c, err
	}
	c = rt.ContentSet{Availability: "provided", Completeness: "complete"}
	if len(blocks) == 0 {
		c.Availability = "provided_empty"
	}
	for i, raw := range blocks {
		var b struct {
			Type string
			Text *string
			JSON json.RawMessage `json:"json"`
		}
		if err := json.Unmarshal(raw, &b); err != nil {
			return c, err
		}
		block := rt.ContentBlock{Index: &i, Kind: "unsupported", Descriptor: "native_content", Stage: "completed", Completeness: "unknown"}
		switch b.Type {
		case "text", "inputText":
			if b.Text == nil {
				return c, fmt.Errorf("missing tool text")
			}
			block.Kind = "text"
			block.Text = b.Text
			block.Descriptor = ""
			block.Completeness = "complete"
		case "json":
			if len(b.JSON) == 0 {
				return c, fmt.Errorf("missing tool JSON")
			}
			block.Kind = "json"
			block.JSON = b.JSON
			block.Descriptor = ""
			block.Completeness = "complete"
		default:
			c.Completeness = "partial"
		}
		c.Blocks = append(c.Blocks, block)
	}
	if len(c.Blocks) == 1 && c.Blocks[0].Kind == "unsupported" {
		c.Availability = "unsupported"
	}
	return c, nil
}

func toolUpdate(n types.Event, id string) rt.Event {
	e := rt.Event{Kind: rt.ToolUpdated, SessionID: id, Diagnostic: diagnostic(n.Method, n.Params)}
	var d struct {
		TurnID  string  `json:"turnId"`
		ItemID  string  `json:"itemId"`
		Delta   *string `json:"delta"`
		Message *string `json:"message"`
	}
	if json.Unmarshal(n.Params, &d) != nil || n.DecodeError != nil || d.ItemID == "" || (d.Delta == nil && d.Message == nil) {
		e.Kind = rt.Failure
		e.Error = &rt.SafeError{Category: "decode_error", Scope: "session"}
		return e
	}
	e.WorkRef = d.TurnID
	t := &rt.ToolPayload{CallRef: d.ItemID, Category: "unknown", State: "running", Input: rt.ContentSet{Availability: "missing", Completeness: "unknown"}, Output: rt.ContentSet{Availability: "missing", Completeness: "unknown"}, OutcomeEvidence: "native_progress"}
	if d.Delta != nil {
		t.Output = rt.TextContent(*d.Delta, "delta", "partial", nil)
	}
	if d.Message != nil {
		c := rt.TextContent(*d.Message, "delta", "partial", nil)
		t.Content = &c
	}
	switch n.Method {
	case "item/commandExecution/outputDelta":
		t.Name = "commandExecution"
		t.Category = "execute"
	case "item/fileChange/outputDelta":
		t.Name = "fileChange"
		t.Category = "write"
	}
	e.Tool = t
	return e
}
