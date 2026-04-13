package events

import (
	"encoding/json"
	"testing"

	acp "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── Test 1: ToolCall full translation ─────────────────────────────────────────

func TestTranslateRich_ToolCall_FullFields(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("s1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	meta := map[string]any{"provider": "anthropic"}
	rawIn := json.RawMessage(`{"cmd":"ls"}`)
	rawOut := json.RawMessage(`{"exit":0}`)
	line := 42

	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.ToolCall = &acp.SessionUpdateToolCall{
			Meta:       meta,
			ToolCallId: "tc-1",
			Kind:       "shell",
			Title:      "run ls",
			Status:     "in_progress",
			Content: []acp.ToolCallContent{
				{Content: &acp.ToolCallContentContent{Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: "content variant"}}}},
				{Diff: &acp.ToolCallContentDiff{Path: "a.go", NewText: "pkg main"}},
				{Terminal: &acp.ToolCallContentTerminal{TerminalId: "term-1", Meta: map[string]any{"x": 1}}},
			},
			Locations: []acp.ToolCallLocation{{Path: "a.go", Line: &line}},
			RawInput:  rawIn,
			RawOutput: rawOut,
		}
	})

	ev, ok := sessionPayload(t, drainEnvelope(t, ch)).(ToolCallEvent)
	require.True(t, ok)
	assert.Equal(t, "anthropic", ev.Meta["provider"])
	assert.Equal(t, "tc-1", ev.ID)
	assert.Equal(t, "shell", ev.Kind)
	assert.Equal(t, "run ls", ev.Title)
	assert.Equal(t, "in_progress", ev.Status)
	require.Len(t, ev.Content, 3)
	// content variant
	require.NotNil(t, ev.Content[0].Content)
	require.NotNil(t, ev.Content[0].Content.Content.Text)
	assert.Equal(t, "content variant", ev.Content[0].Content.Content.Text.Text)
	// diff variant
	require.NotNil(t, ev.Content[1].Diff)
	assert.Equal(t, "a.go", ev.Content[1].Diff.Path)
	assert.Equal(t, "pkg main", ev.Content[1].Diff.NewText)
	// terminal variant
	require.NotNil(t, ev.Content[2].Terminal)
	assert.Equal(t, "term-1", ev.Content[2].Terminal.TerminalID)
	assert.EqualValues(t, 1, ev.Content[2].Terminal.Meta["x"])
	require.Len(t, ev.Locations, 1)
	assert.Equal(t, "a.go", ev.Locations[0].Path)
	require.NotNil(t, ev.Locations[0].Line)
	assert.Equal(t, 42, *ev.Locations[0].Line)
	assert.NotNil(t, ev.RawInput)
	assert.NotNil(t, ev.RawOutput)
}

// ── Test 2: ToolCallUpdate full translation ────────────────────────────────────

func TestTranslateRich_ToolCallUpdate_FullFields(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("s1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	status := acp.ToolCallStatus("completed")
	kind := acp.ToolKind("shell")
	title := "run ls"

	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.ToolCallUpdate = &acp.SessionToolCallUpdate{
			Meta:       map[string]any{"k": "v"},
			ToolCallId: "tc-2",
			Status:     &status,
			Kind:       &kind,
			Title:      &title,
			Content:    []acp.ToolCallContent{{Diff: &acp.ToolCallContentDiff{Path: "b.go", NewText: "x"}}},
			Locations:  []acp.ToolCallLocation{{Path: "b.go"}},
			RawInput:   "in",
			RawOutput:  "out",
		}
	})

	ev, ok := sessionPayload(t, drainEnvelope(t, ch)).(ToolResultEvent)
	require.True(t, ok)
	assert.Equal(t, "v", ev.Meta["k"])
	assert.Equal(t, "tc-2", ev.ID)
	assert.Equal(t, "completed", ev.Status)
	assert.Equal(t, "shell", ev.Kind)
	assert.Equal(t, "run ls", ev.Title)
	require.Len(t, ev.Content, 1)
	require.NotNil(t, ev.Content[0].Diff)
	require.Len(t, ev.Locations, 1)
	assert.NotNil(t, ev.RawInput)
	assert.NotNil(t, ev.RawOutput)
}

// ── Tests 3-7: ContentBlock all 5 variants ────────────────────────────────────

func TestTranslateRich_ContentBlock_Text(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("s1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	prio := 0.9
	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.AgentMessageChunk = &acp.SessionUpdateAgentMessageChunk{
			Content: acp.ContentBlock{Text: &acp.ContentBlockText{
				Meta: map[string]any{"src": "llm"},
				Text: "hello",
				Annotations: &acp.Annotations{
					Meta:     map[string]any{"a": 1},
					Audience: []acp.Role{"user"},
					Priority: &prio,
				},
			}},
		}
	})

	ev, ok := sessionPayload(t, drainEnvelope(t, ch)).(TextEvent)
	require.True(t, ok)
	assert.Equal(t, "hello", ev.Text) // convenience field preserved
	require.NotNil(t, ev.Content)
	require.NotNil(t, ev.Content.Text)
	assert.Equal(t, "llm", ev.Content.Text.Meta["src"])
	assert.Equal(t, "hello", ev.Content.Text.Text)
	require.NotNil(t, ev.Content.Text.Annotations)
	assert.EqualValues(t, 1, ev.Content.Text.Annotations.Meta["a"])
	assert.Equal(t, []string{"user"}, ev.Content.Text.Annotations.Audience)
	require.NotNil(t, ev.Content.Text.Annotations.Priority)
	assert.InDelta(t, 0.9, *ev.Content.Text.Annotations.Priority, 0.001)
}

func TestTranslateRich_ContentBlock_Image(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("s1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	uri := "https://example.com/img.png"
	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.AgentMessageChunk = &acp.SessionUpdateAgentMessageChunk{
			Content: acp.ContentBlock{Image: &acp.ContentBlockImage{
				Meta:        map[string]any{"src": "camera"},
				Data:        "base64data",
				MimeType:    "image/png",
				Uri:         &uri,
				Annotations: &acp.Annotations{Audience: []acp.Role{"assistant"}},
			}},
		}
	})

	ev, ok := sessionPayload(t, drainEnvelope(t, ch)).(TextEvent)
	require.True(t, ok)
	require.NotNil(t, ev.Content)
	require.NotNil(t, ev.Content.Image)
	img := ev.Content.Image
	assert.Equal(t, "camera", img.Meta["src"])
	assert.Equal(t, "base64data", img.Data)
	assert.Equal(t, "image/png", img.MimeType)
	require.NotNil(t, img.URI)
	assert.Equal(t, uri, *img.URI)
	require.NotNil(t, img.Annotations)
	assert.Equal(t, []string{"assistant"}, img.Annotations.Audience)
}

func TestTranslateRich_ContentBlock_Audio(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("s1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.AgentMessageChunk = &acp.SessionUpdateAgentMessageChunk{
			Content: acp.ContentBlock{Audio: &acp.ContentBlockAudio{
				Meta:        map[string]any{"enc": "opus"},
				Data:        "audiodata",
				MimeType:    "audio/ogg",
				Annotations: &acp.Annotations{Audience: []acp.Role{"user"}},
			}},
		}
	})

	ev, ok := sessionPayload(t, drainEnvelope(t, ch)).(TextEvent)
	require.True(t, ok)
	require.NotNil(t, ev.Content)
	require.NotNil(t, ev.Content.Audio)
	aud := ev.Content.Audio
	assert.Equal(t, "opus", aud.Meta["enc"])
	assert.Equal(t, "audiodata", aud.Data)
	assert.Equal(t, "audio/ogg", aud.MimeType)
	require.NotNil(t, aud.Annotations)
}

func TestTranslateRich_ContentBlock_ResourceLink(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("s1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	desc := "readme"
	mt := "text/plain"
	title := "README"
	size := 1234

	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.AgentMessageChunk = &acp.SessionUpdateAgentMessageChunk{
			Content: acp.ContentBlock{ResourceLink: &acp.ContentBlockResourceLink{
				Meta:        map[string]any{"rl": true},
				Uri:         "file:///README.md",
				Name:        "README.md",
				Description: &desc,
				MimeType:    &mt,
				Title:       &title,
				Size:        &size,
				Annotations: &acp.Annotations{Audience: []acp.Role{"user"}},
			}},
		}
	})

	ev, ok := sessionPayload(t, drainEnvelope(t, ch)).(TextEvent)
	require.True(t, ok)
	require.NotNil(t, ev.Content)
	require.NotNil(t, ev.Content.ResourceLink)
	rl := ev.Content.ResourceLink
	assert.True(t, rl.Meta["rl"].(bool))
	assert.Equal(t, "file:///README.md", rl.URI)
	assert.Equal(t, "README.md", rl.Name)
	require.NotNil(t, rl.Description)
	assert.Equal(t, "readme", *rl.Description)
	require.NotNil(t, rl.MimeType)
	assert.Equal(t, "text/plain", *rl.MimeType)
	require.NotNil(t, rl.Title)
	assert.Equal(t, "README", *rl.Title)
	require.NotNil(t, rl.Size)
	assert.Equal(t, 1234, *rl.Size)
	require.NotNil(t, rl.Annotations)
}

func TestTranslateRich_ContentBlock_Resource(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("s1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	mt := "text/plain"

	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.AgentMessageChunk = &acp.SessionUpdateAgentMessageChunk{
			Content: acp.ContentBlock{Resource: &acp.ContentBlockResource{
				Meta: map[string]any{"res": "yes"},
				Resource: acp.EmbeddedResourceResource{
					TextResourceContents: &acp.TextResourceContents{
						Uri:      "file:///foo.txt",
						MimeType: &mt,
						Text:     "hello world",
					},
				},
				Annotations: &acp.Annotations{Audience: []acp.Role{"user"}},
			}},
		}
	})

	ev, ok := sessionPayload(t, drainEnvelope(t, ch)).(TextEvent)
	require.True(t, ok)
	require.NotNil(t, ev.Content)
	require.NotNil(t, ev.Content.Resource)
	res := ev.Content.Resource
	assert.Equal(t, "yes", res.Meta["res"])
	require.NotNil(t, res.Resource.TextResource)
	assert.Equal(t, "file:///foo.txt", res.Resource.TextResource.URI)
	assert.Equal(t, "hello world", res.Resource.TextResource.Text)
	require.NotNil(t, res.Resource.TextResource.MimeType)
	require.NotNil(t, res.Annotations)

	// Also test blob variant via direct conversion
	blobResource := acp.EmbeddedResourceResource{
		BlobResourceContents: &acp.BlobResourceContents{
			Uri:  "file:///img.png",
			Blob: "binarydata",
		},
	}
	embedded := convertEmbeddedResource(blobResource)
	require.NotNil(t, embedded.BlobResource)
	assert.Equal(t, "file:///img.png", embedded.BlobResource.URI)
	assert.Equal(t, "binarydata", embedded.BlobResource.Blob)
}

// ── Test 8: AvailableCommandsUpdate ──────────────────────────────────────────

func TestTranslateRich_AvailableCommandsUpdate(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("s1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.AvailableCommandsUpdate = &acp.SessionAvailableCommandsUpdate{
			Meta: map[string]any{"src": "agent"},
			AvailableCommands: []acp.AvailableCommand{
				{
					Meta:        map[string]any{"cmd": "go"},
					Name:        "run_tests",
					Description: "run the test suite",
					Input: &acp.AvailableCommandInput{
						Unstructured: &acp.UnstructuredCommandInput{
							Meta: map[string]any{"ui": 1},
							Hint: "pass flags here",
						},
					},
				},
			},
		}
	})

	ev, ok := sessionPayload(t, drainEnvelope(t, ch)).(AvailableCommandsEvent)
	require.True(t, ok)
	assert.Equal(t, "agent", ev.Meta["src"])
	require.Len(t, ev.Commands, 1)
	cmd := ev.Commands[0]
	assert.Equal(t, "go", cmd.Meta["cmd"])
	assert.Equal(t, "run_tests", cmd.Name)
	assert.Equal(t, "run the test suite", cmd.Description)
	require.NotNil(t, cmd.Input)
	require.NotNil(t, cmd.Input.Unstructured)
	assert.Equal(t, "pass flags here", cmd.Input.Unstructured.Hint)
	assert.EqualValues(t, 1, cmd.Input.Unstructured.Meta["ui"])
}

// ── Test 9: CurrentModeUpdate ─────────────────────────────────────────────────

func TestTranslateRich_CurrentModeUpdate(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("s1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.CurrentModeUpdate = &acp.SessionCurrentModeUpdate{
			Meta:          map[string]any{"v": 2},
			CurrentModeId: "edit",
		}
	})

	ev, ok := sessionPayload(t, drainEnvelope(t, ch)).(CurrentModeEvent)
	require.True(t, ok)
	assert.EqualValues(t, 2, ev.Meta["v"])
	assert.Equal(t, "edit", ev.ModeID)
}

// ── Test 10: ConfigOptionUpdate ───────────────────────────────────────────────

func TestTranslateRich_ConfigOptionUpdate(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("s1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	desc := "temperature"
	cat := acp.SessionConfigOptionCategoryOther("sampling")
	ungroupedOptions := acp.SessionConfigSelectOptionsUngrouped([]acp.SessionConfigSelectOption{
		{Name: "low", Value: "0.2"},
		{Name: "high", Value: "0.9"},
	})

	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.ConfigOptionUpdate = &acp.SessionConfigOptionUpdate{
			Meta: map[string]any{"cfg": true},
			ConfigOptions: []acp.SessionConfigOption{
				{Select: &acp.SessionConfigOptionSelect{
					Meta:         map[string]any{"s": 1},
					Id:           "temp",
					Name:         "Temperature",
					CurrentValue: "0.2",
					Description:  &desc,
					Category:     &acp.SessionConfigOptionCategory{Other: &cat},
					Options:      acp.SessionConfigSelectOptions{Ungrouped: &ungroupedOptions},
				}},
			},
		}
	})

	ev, ok := sessionPayload(t, drainEnvelope(t, ch)).(ConfigOptionEvent)
	require.True(t, ok)
	assert.True(t, ev.Meta["cfg"].(bool))
	require.Len(t, ev.ConfigOptions, 1)
	opt := ev.ConfigOptions[0]
	require.NotNil(t, opt.Select)
	s := opt.Select
	assert.EqualValues(t, 1, s.Meta["s"])
	assert.Equal(t, "temp", s.ID)
	assert.Equal(t, "Temperature", s.Name)
	assert.Equal(t, "0.2", s.CurrentValue)
	require.NotNil(t, s.Description)
	assert.Equal(t, "temperature", *s.Description)
	require.NotNil(t, s.Category)
	assert.Equal(t, "sampling", *s.Category)
	require.NotNil(t, s.Options.Ungrouped)
	require.Len(t, s.Options.Ungrouped, 2)
	assert.Equal(t, "low", s.Options.Ungrouped[0].Name)
	assert.Equal(t, "0.2", s.Options.Ungrouped[0].Value)
}

func TestTranslateRich_ConfigOptionUpdate_GroupedOptions(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("s1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	groupedOptions := acp.SessionConfigSelectOptionsGrouped([]acp.SessionConfigSelectGroup{
		{
			Group: "basic",
			Name:  "Basic",
			Options: []acp.SessionConfigSelectOption{
				{Name: "fast", Value: "fast"},
			},
		},
	})

	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.ConfigOptionUpdate = &acp.SessionConfigOptionUpdate{
			ConfigOptions: []acp.SessionConfigOption{
				{Select: &acp.SessionConfigOptionSelect{
					Id:           "model",
					Name:         "Model",
					CurrentValue: "fast",
					Options:      acp.SessionConfigSelectOptions{Grouped: &groupedOptions},
				}},
			},
		}
	})

	ev, ok := sessionPayload(t, drainEnvelope(t, ch)).(ConfigOptionEvent)
	require.True(t, ok)
	require.Len(t, ev.ConfigOptions, 1)
	s := ev.ConfigOptions[0].Select
	require.NotNil(t, s)
	require.NotNil(t, s.Options.Grouped)
	require.Len(t, s.Options.Grouped, 1)
	g := s.Options.Grouped[0]
	assert.Equal(t, "basic", g.Group)
	assert.Equal(t, "Basic", g.Name)
	require.Len(t, g.Options, 1)
	assert.Equal(t, "fast", g.Options[0].Value)
}

// ── Test 11: SessionInfoUpdate ────────────────────────────────────────────────

func TestTranslateRich_SessionInfoUpdate(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("s1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	title := "My Session"
	updated := "2026-04-12T10:00:00Z"

	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.SessionInfoUpdate = &acp.SessionSessionInfoUpdate{
			Meta:      map[string]any{"si": 1},
			Title:     &title,
			UpdatedAt: &updated,
		}
	})

	ev, ok := sessionPayload(t, drainEnvelope(t, ch)).(SessionInfoEvent)
	require.True(t, ok)
	assert.EqualValues(t, 1, ev.Meta["si"])
	require.NotNil(t, ev.Title)
	assert.Equal(t, "My Session", *ev.Title)
	require.NotNil(t, ev.UpdatedAt)
	assert.Equal(t, updated, *ev.UpdatedAt)
}

// ── Test 12: UsageUpdate ──────────────────────────────────────────────────────

func TestTranslateRich_UsageUpdate(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("s1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.UsageUpdate = &acp.SessionUsageUpdate{
			Meta:  map[string]any{"u": "yes"},
			Cost:  &acp.Cost{Amount: 0.042, Currency: "USD"},
			Size:  8192,
			Used:  4096,
		}
	})

	ev, ok := sessionPayload(t, drainEnvelope(t, ch)).(UsageEvent)
	require.True(t, ok)
	assert.Equal(t, "yes", ev.Meta["u"])
	require.NotNil(t, ev.Cost)
	assert.InDelta(t, 0.042, ev.Cost.Amount, 0.0001)
	assert.Equal(t, "USD", ev.Cost.Currency)
	assert.Equal(t, 8192, ev.Size)
	assert.Equal(t, 4096, ev.Used)
}

// ── Test 13: RawInput/RawOutput JSON round-trip ───────────────────────────────

func TestTranslateRich_RawInputOutput_RoundTrip(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("s1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	rawIn := map[string]any{"cmd": "ls", "args": []any{"-la"}}
	rawOut := map[string]any{"stdout": "file1\nfile2", "exit_code": float64(0)}

	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.ToolCall = &acp.SessionUpdateToolCall{
			ToolCallId: "tc-rt",
			Kind:       "shell",
			Title:      "ls",
			RawInput:   rawIn,
			RawOutput:  rawOut,
		}
	})

	ev, ok := sessionPayload(t, drainEnvelope(t, ch)).(ToolCallEvent)
	require.True(t, ok)

	// Marshal the event and unmarshal back.
	b, err := json.Marshal(ev)
	require.NoError(t, err)
	var ev2 ToolCallEvent
	require.NoError(t, json.Unmarshal(b, &ev2))

	// RawInput and RawOutput should survive the round-trip.
	ri, ok := ev2.RawInput.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "ls", ri["cmd"])

	ro, ok := ev2.RawOutput.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "file1\nfile2", ro["stdout"])
	assert.InDelta(t, 0, ro["exit_code"], 0.001)
}

// ── Test 14: Envelope decode all 17 event types ───────────────────────────────

func TestTranslateRich_EnvelopeDecode_AllEventTypes(t *testing.T) {
	cases := []struct {
		name    string
		evType  string
		payload Event
	}{
		{"text", "text", TextEvent{Text: "hi"}},
		{"thinking", "thinking", ThinkingEvent{Text: "thought"}},
		{"user_message", "user_message", UserMessageEvent{Text: "user"}},
		{"tool_call", "tool_call", ToolCallEvent{ID: "1", Kind: "shell", Title: "t"}},
		{"tool_result", "tool_result", ToolResultEvent{ID: "1", Status: "completed"}},
		{"file_write", "file_write", FileWriteEvent{Path: "/a", Allowed: true}},
		{"file_read", "file_read", FileReadEvent{Path: "/b", Allowed: false}},
		{"command", "command", CommandEvent{Command: "ls", Allowed: true}},
		{"plan", "plan", PlanEvent{Entries: nil}},
		{"turn_start", "turn_start", TurnStartEvent{}},
		{"turn_end", "turn_end", TurnEndEvent{StopReason: "stop"}},
		{"error", "error", ErrorEvent{Msg: "oops"}},
		{"available_commands", "available_commands", AvailableCommandsEvent{Commands: nil}},
		{"current_mode", "current_mode", CurrentModeEvent{ModeID: "edit"}},
		{"config_option", "config_option", ConfigOptionEvent{ConfigOptions: nil}},
		{"session_info", "session_info", SessionInfoEvent{}},
		{"usage", "usage", UsageEvent{Size: 100, Used: 50}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Build a TypedEvent, marshal, unmarshal.
			te := newTypedEvent(tc.payload)
			b, err := json.Marshal(te)
			require.NoError(t, err, "marshal")
			var te2 TypedEvent
			require.NoError(t, json.Unmarshal(b, &te2), "unmarshal")
			assert.Equal(t, tc.evType, te2.Type)
			// The payload type should survive round-trip.
			assert.IsType(t, tc.payload, te2.Payload, "payload type mismatch")
		})
	}
}

// ── Test 15: Backward compatibility ──────────────────────────────────────────

func TestTranslateRich_BackwardCompat_OldJSON(t *testing.T) {
	// Old ToolCallEvent JSON (only id, kind, title — no new fields).
	oldJSON := `{"type":"tool_call","payload":{"id":"tc-1","kind":"shell","title":"run ls"}}`
	var te TypedEvent
	require.NoError(t, json.Unmarshal([]byte(oldJSON), &te))
	ev, ok := te.Payload.(ToolCallEvent)
	require.True(t, ok)
	assert.Equal(t, "tc-1", ev.ID)
	assert.Equal(t, "shell", ev.Kind)
	assert.Equal(t, "run ls", ev.Title)
	assert.Nil(t, ev.Meta)
	assert.Nil(t, ev.Content)

	// Old ToolResultEvent JSON.
	oldResultJSON := `{"type":"tool_result","payload":{"id":"tc-1","status":"completed"}}`
	var te2 TypedEvent
	require.NoError(t, json.Unmarshal([]byte(oldResultJSON), &te2))
	res, ok := te2.Payload.(ToolResultEvent)
	require.True(t, ok)
	assert.Equal(t, "tc-1", res.ID)
	assert.Equal(t, "completed", res.Status)
	assert.Empty(t, res.Kind)
	assert.Nil(t, res.Content)

	// Old TextEvent JSON (no content field).
	oldTextJSON := `{"type":"text","payload":{"text":"hello"}}`
	var te3 TypedEvent
	require.NoError(t, json.Unmarshal([]byte(oldTextJSON), &te3))
	txt, ok := te3.Payload.(TextEvent)
	require.True(t, ok)
	assert.Equal(t, "hello", txt.Text)
	assert.Nil(t, txt.Content)
}
