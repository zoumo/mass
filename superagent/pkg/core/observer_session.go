package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	massstore "github.com/zoumo/mass/mass-go/pkg/store"
)

type sessionPreviewEvent struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	At        time.Time `json:"at"`
	Text      string    `json:"text,omitempty"`
	Truncated bool      `json:"truncated,omitempty"`
	Replayed  bool      `json:"replayed,omitempty"`
}

type sessionPreviewFrame struct {
	Kind  string               `json:"kind"`
	Event *sessionPreviewEvent `json:"event,omitempty"`
}

// Only Core-owned bindings can be inspected. The browser never chooses a
// massd target or sees its connection configuration.
func (s *Server) sessionPreview(w http.ResponseWriter, r *http.Request, path string) {
	id, view, ok := strings.Cut(path, "/")
	if !ok || (view != "history" && view != "live") {
		s.fail(w, problem("not_found", "unknown Session preview"))
		return
	}
	raw, err := s.store.Get(r.Context(), "session-bindings", id)
	if err != nil {
		s.fail(w, err)
		return
	}
	var binding SessionBinding
	if json.Unmarshal(raw, &binding) != nil || s.config.Sessions == nil || binding.SessionID == "" || binding.SessionUID == "" {
		s.fail(w, problem("unavailable", "Session identity has not been recorded"))
		return
	}
	if r.Method == http.MethodHead {
		if view == "live" {
			w.Header().Set("Content-Type", "application/x-ndjson")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(s.observerLife, cancel)
	defer stop()
	c, err := s.massClient()
	if err != nil {
		s.fail(w, problem("unavailable", "massd observation unavailable"))
		return
	}
	defer func() { _ = c.Close() }()
	setup, finish := context.WithTimeout(ctx, 3*time.Second)
	defer finish()
	ref := bindingSessionRef(binding)
	if !matchesPreviewSession(setup, c, binding, ref) {
		s.fail(w, problem("unavailable", "massd Session unavailable or identity unconfirmed"))
		return
	}
	if view == "live" {
		sub, subErr := c.Subscribe(setup, ref)
		if subErr != nil || !matchesPreviewSession(setup, c, binding, ref) {
			s.fail(w, problem("unavailable", "massd live observation unavailable"))
			return
		}
		finish()
		s.streamSessionPreview(ctx, w, sub, binding)
		return
	}
	var page massstore.HistoryPage
	err = c.History(setup, ref, massstore.HistoryQuery{After: r.URL.Query().Get("cursor"), Limit: 50}, &page)
	if err != nil {
		switch errorCode(err) {
		case "invalid_argument", "not_found":
			s.fail(w, problem("invalid_argument", "massd did not recognize the history cursor"))
		default:
			s.fail(w, problem("unavailable", "massd history unavailable"))
		}
		return
	}
	rows := make([]sessionPreviewEvent, 0, len(page.Events))
	for _, event := range page.Events {
		if !previewEventMatches(event, binding) {
			s.fail(w, problem("unavailable", "massd history identity unconfirmed"))
			return
		}
		rows = append(rows, projectPreviewEvent(event))
	}
	s.write(w, http.StatusOK, map[string]any{"events": rows, "nextCursor": page.Next, "syncedEventId": page.SyncedEventID, "observedAt": now()})
}

func matchesPreviewSession(ctx context.Context, c *client.Client, b SessionBinding, ref resource.SessionRef) bool {
	var session resource.Session
	return c.GetSession(ctx, ref, &session) == nil &&
		session.Status.SessionID == b.SessionID && session.Metadata.UID == b.SessionUID &&
		session.Metadata.Namespace == b.Namespace && session.Metadata.Name == b.Name
}

func previewEventMatches(e events.SessionEvent, b SessionBinding) bool {
	return e.SessionID == b.SessionID && e.Namespace == b.Namespace && e.SessionName == b.Name
}

func (s *Server) streamSessionPreview(ctx context.Context, w http.ResponseWriter, sub *client.Subscription, b SessionBinding) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	controller := http.NewResponseController(w)
	send := func(frame sessionPreviewFrame) bool {
		body, err := s.responseJSON(ctx, frame)
		if err != nil || controller.SetWriteDeadline(time.Now().Add(5*time.Second)) != nil {
			return false
		}
		if _, err := io.WriteString(w, body+"\n"); err != nil {
			return false
		}
		return controller.Flush() == nil && controller.SetWriteDeadline(time.Time{}) == nil
	}
	if !send(sessionPreviewFrame{Kind: "connected"}) {
		return
	}
	if observed, ok := w.(*telemetryResponse); ok {
		observed.finishRequest()
	}
	closePreview := s.store.telemetry.preview(ctx)
	outcome := "closed"
	defer func() {
		if ctx.Err() != nil {
			outcome = "canceled"
		}
		closePreview(outcome)
	}()
	for ctx.Err() == nil {
		wait, cancel := context.WithTimeout(ctx, 10*time.Second)
		event, err := sub.Next(wait)
		cancel()
		if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			if !send(sessionPreviewFrame{Kind: "heartbeat"}) {
				outcome = "write_failed"
				return
			}
			continue
		}
		if err != nil || !previewEventMatches(event, b) {
			if !errors.Is(err, io.EOF) {
				outcome = "interrupted"
			}
			return
		}
		frame := sessionPreviewFrame{Kind: "history_changed"}
		if !event.Persistent() {
			record := projectPreviewEvent(event)
			frame.Kind = "preview"
			frame.Event = &record
		}
		if !send(frame) {
			outcome = "write_failed"
			return
		}
	}
}

func projectPreviewEvent(e events.SessionEvent) sessionPreviewEvent {
	record := sessionPreviewEvent{ID: e.EventID, Type: e.Type(), At: e.Time, Replayed: e.Replay != nil}
	// Select readable fields. Runtime metadata, raw tool values, errors and
	// configuration payloads may contain material unrelated to observation.
	switch payload := e.Payload().(type) {
	case events.UserMessage:
		record.Text = previewText(events.MessagePayload(payload))
	case events.AgentMessage:
		record.Text = previewText(events.MessagePayload(payload))
	case events.AgentThinking:
		record.Text = previewText(events.MessagePayload(payload))
	case events.MessageChunk:
		record.Text = previewText(events.MessagePayload(payload))
	case events.ThinkingChunk:
		record.Text = previewText(events.MessagePayload(payload))
	case events.ToolCall:
		record.Text = payload.ToolName
	case events.StateUpdate:
		if payload.State != nil {
			record.Text = string(*payload.State)
		}
	}
	const limit = 16 * 1024
	if len(record.Text) > limit {
		end := limit
		for !utf8.RuneStart(record.Text[end]) {
			end--
		}
		record.Text, record.Truncated = record.Text[:end], true
	}
	return record
}

func previewText(payload events.MessagePayload) string {
	text := make([]string, 0, len(payload.Content))
	for _, block := range payload.Content {
		if block.Text != nil {
			text = append(text, block.Text.Text)
		}
	}
	return strings.Join(text, "\n")
}
