package core

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/zoumo/mass/massd/pkg/client"
	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/massd/pkg/resource"
	"github.com/zoumo/mass/massd/pkg/runtime"
	massstore "github.com/zoumo/mass/massd/pkg/store"
)

type sessionChatCommand struct {
	Action      string          `json:"action" enum:"prompt,cancel,answer"`
	Text        string          `json:"text,omitempty"`
	Interaction string          `json:"interaction,omitempty"`
	Answer      json.RawMessage `json:"answer,omitempty"`
}

type sessionChatFrame struct {
	Kind  string               `json:"kind"`
	State events.SessionState  `json:"state,omitempty"`
	Event *events.SessionEvent `json:"event,omitempty"`
}

type sessionChatEventSchema struct {
	events.EventBase
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
}

func (s *Server) registerSessionChatOperations(api *httpAPI, bindingParam *huma.Param) {
	path := sessionBindingPath + "/chat"
	api.huma.OpenAPI().Components.Schemas.RegisterTypeAlias(reflect.TypeFor[events.SessionEvent](), reflect.TypeFor[sessionChatEventSchema]())
	responses := func(success *huma.Response) map[string]*huma.Response {
		return map[string]*huma.Response{
			"200": success,
			"400": api.coreError("Invalid command or cursor"),
			"403": api.coreError("Request is outside the trusted Host or Origin boundary"),
			"404": api.coreError("Current Topic or Worker binding not found"),
			"409": api.coreError("Session state or identity conflict"),
			"503": api.coreError("Session operation unavailable or outcome unconfirmed"),
		}
	}
	register := func(operation huma.Operation, handler http.HandlerFunc) {
		api.registerRaw(operation, s.observeResourceHTTP(handler).ServeHTTP)
	}
	register(huma.Operation{
		Method: http.MethodGet, Path: path + "/history", OperationID: "getSessionChatHistory",
		Summary: "Read Topic or Worker chat history", Tags: []string{"Core"},
		Parameters: []*huma.Param{bindingParam, queryString("cursor", "Opaque cursor returned by the preceding history page")},
		Responses:  responses(jsonResponse("Session chat history", api.schema(reflect.TypeFor[massstore.HistoryPage]()))),
	}, s.readSessionChatHistory)
	register(huma.Operation{
		Method: http.MethodGet, Path: path + "/live", OperationID: "streamSessionChat",
		Summary: "Stream Topic or Worker chat events", Tags: []string{"Core"},
		Parameters: []*huma.Param{bindingParam},
		Responses: responses(&huma.Response{Description: "Connection state, Session events and heartbeats", Content: map[string]*huma.MediaType{
			"application/x-ndjson": {Schema: &huma.Schema{Type: huma.TypeString, Extensions: map[string]any{
				"x-frame-schema": api.schema(reflect.TypeFor[sessionChatFrame]()),
			}}},
		}}),
	}, s.streamSessionChat)
	register(huma.Operation{
		Method: http.MethodPost, Path: path, OperationID: "controlSessionChat",
		Summary:     "Send, cancel or answer in a Topic or Worker chat",
		Description: "A prompt resumes a verified offline Session with the submitted input.",
		Tags:        []string{"Core"}, Parameters: []*huma.Param{bindingParam},
		RequestBody: &huma.RequestBody{Required: true, Content: jsonContent(api.schema(reflect.TypeFor[sessionChatCommand]()))},
		Responses: responses(jsonResponse("MASS operation receipt", &huma.Schema{AnyOf: []*huma.Schema{
			api.schema(reflect.TypeFor[runtime.InputReceipt]()),
			api.schema(reflect.TypeFor[runtime.CancelReceipt]()),
			api.schema(reflect.TypeFor[runtime.InteractionReceipt]()),
		}})),
	}, s.controlSessionChat)
}

// Resolve only the current binding, including when a client calls Core directly.
func (s *Server) sessionChatBinding(ctx context.Context, id string) (SessionBinding, error) {
	var b SessionBinding
	tx, err := s.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return b, storageError()
	}
	defer func() { _ = tx.Rollback() }()
	if err := readJSON(ctx, tx, `SELECT body FROM session_bindings WHERE id=?`, &b, id); err != nil {
		return b, err
	}
	var current string
	switch b.Role {
	case "topic":
		var topic Topic
		if err := readJSON(ctx, tx, `SELECT body FROM topics WHERE id=?`, &topic, b.OwnerID); err != nil {
			return b, err
		}
		current = topic.SessionBindingID
	case "issue":
		var issue Issue
		if err := readJSON(ctx, tx, `SELECT body FROM issues WHERE id=?`, &issue, b.OwnerID); err != nil {
			return b, err
		}
		current = issue.CurrentSessionBindingID
	}
	if current == "" || current != b.ID || b.RetiredAt != "" {
		return b, problem("not_found", "Current Topic or Worker Session binding not found")
	}
	if b.State != "ready" || b.SessionID == "" || b.SessionUID == "" {
		return b, problem("conflict", "Session binding is not ready")
	}
	return b, nil
}

type chatSession struct {
	binding SessionBinding
	ref     resource.SessionRef
	client  *client.Client
	server  *Server
}

func (s *Server) openSessionChat(ctx context.Context, r *http.Request) (chatSession, error) {
	b, err := s.sessionChatBinding(ctx, sessionBindingPathValue(r))
	if err != nil {
		return chatSession{}, err
	}
	c, err := s.massClient()
	if err != nil {
		return chatSession{}, problem("unavailable", "MASS connection unavailable")
	}
	return chatSession{binding: b, ref: bindingSessionRef(b), client: c, server: s}, nil
}

func (chat chatSession) requireCurrent(ctx context.Context) error {
	current, err := chat.server.sessionChatBinding(ctx, chat.binding.ID)
	if err != nil {
		return err
	}
	if current.SessionID != chat.binding.SessionID || current.SessionUID != chat.binding.SessionUID {
		return problem("conflict", "Session binding identity changed")
	}
	return nil
}

func (chat chatSession) state(ctx context.Context) (events.SessionState, error) {
	var session resource.Session
	if err := chat.client.GetSession(ctx, chat.ref, &session); err != nil {
		return "", err
	}
	if !previewSessionMatches(session, chat.binding) {
		return "", problem("conflict", "Core binding and MASS Session identity do not match")
	}
	return session.Status.State, nil
}

func (s *Server) readSessionChatHistory(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	chat, err := s.openSessionChat(ctx, r)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = chat.client.Close() }()
	var page massstore.HistoryPage
	if _, err = chat.state(ctx); err == nil {
		err = chat.client.History(ctx, chat.ref, massstore.HistoryQuery{After: r.URL.Query().Get("cursor"), Limit: 100}, &page)
	}
	if err != nil {
		s.failSessionChat(w, err)
		return
	}
	for _, event := range page.Events {
		if !previewEventMatches(event, chat.binding) {
			s.fail(w, problem("conflict", "Session history identity does not match"))
			return
		}
	}
	if err := chat.requireCurrent(ctx); err != nil {
		s.fail(w, err)
		return
	}
	if page.Events == nil {
		page.Events = []events.SessionEvent{}
	}
	s.write(w, http.StatusOK, page)
}

func (s *Server) streamSessionChat(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(s.observerLife, cancel)
	defer stop()
	setup, finish := context.WithTimeout(ctx, 3*time.Second)
	defer finish()
	chat, err := s.openSessionChat(setup, r)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = chat.client.Close() }()
	if _, err := chat.state(setup); err != nil {
		s.failSessionChat(w, err)
		return
	}
	sub, err := chat.client.Subscribe(setup, chat.ref)
	if err != nil {
		s.failSessionChat(w, err)
		return
	}
	state, err := chat.state(setup)
	if err != nil {
		s.failSessionChat(w, err)
		return
	}
	if err := chat.requireCurrent(setup); err != nil {
		s.fail(w, err)
		return
	}
	finish()
	w.Header().Set("Content-Type", "application/x-ndjson")
	controller := http.NewResponseController(w)
	if !s.writeSessionFrame(ctx, w, controller, sessionChatFrame{Kind: "connected", State: state}) {
		return
	}
	if observed, ok := w.(*telemetryResponse); ok {
		observed.finishRequest()
	}
	const heartbeat = 10 * time.Second
	checkAt := time.Now().Add(heartbeat)
	for ctx.Err() == nil {
		wait, cancelWait := context.WithDeadline(ctx, checkAt)
		event, err := sub.Next(wait)
		cancelWait()
		if !time.Now().Before(checkAt) {
			if chat.requireCurrent(ctx) != nil {
				return
			}
			checkAt = time.Now().Add(heartbeat)
		}
		if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			if !s.writeSessionFrame(ctx, w, controller, sessionChatFrame{Kind: "heartbeat"}) {
				return
			}
			continue
		}
		if err != nil || !previewEventMatches(event, chat.binding) ||
			!s.writeSessionFrame(ctx, w, controller, sessionChatFrame{Kind: "event", Event: &event}) {
			return
		}
	}
}

func (s *Server) controlSessionChat(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	var command sessionChatCommand
	if err != nil || mediaType != "application/json" || decoder.Decode(&command) != nil || decoder.Decode(new(any)) != io.EOF {
		s.fail(w, problem("invalid_argument", "Expected a Session chat command as application/json"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	chat, err := s.openSessionChat(ctx, r)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = chat.client.Close() }()
	state, err := chat.state(ctx)
	if err == nil {
		var receipt any
		receipt, err = chat.apply(ctx, state, command)
		if err == nil {
			s.write(w, http.StatusOK, receipt)
			return
		}
	}
	s.failSessionChat(w, err)
}

func (chat chatSession) apply(ctx context.Context, state events.SessionState, command sessionChatCommand) (any, error) {
	switch command.Action {
	case "prompt":
		if strings.TrimSpace(command.Text) != "" && command.Interaction == "" && len(command.Answer) == 0 {
			return chat.prompt(ctx, state, runtime.Input{Content: []events.ContentBlock{events.Text(command.Text)}})
		}
	case "cancel":
		if command.Text != "" || command.Interaction != "" || len(command.Answer) != 0 {
			break
		}
		if state != events.Running {
			return nil, problem("conflict", "Session is not running")
		}
		var receipt runtime.CancelReceipt
		if err := chat.requireCurrent(ctx); err != nil {
			return receipt, err
		}
		err := chat.client.Cancel(ctx, chat.ref, &receipt)
		return receipt, err
	case "answer":
		if command.Text == "" && command.Interaction != "" && len(command.Answer) != 0 {
			decoder := json.NewDecoder(bytes.NewReader(command.Answer))
			decoder.UseNumber()
			var answer any
			if err := decoder.Decode(&answer); err != nil {
				break
			}
			var receipt runtime.InteractionReceipt
			if err := chat.requireCurrent(ctx); err != nil {
				return receipt, err
			}
			err := chat.client.Answer(ctx, chat.ref, command.Interaction, answer, &receipt)
			return receipt, err
		}
	}
	return nil, problem("invalid_argument", "Invalid Session chat command")
}

func (chat chatSession) awaitClose(ctx context.Context, state events.SessionState) (events.SessionState, error) {
	for range 20 {
		if state != "closing" {
			return state, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
		var err error
		state, err = chat.state(ctx)
		if err != nil {
			return "", err
		}
	}
	if state == "closing" {
		return "", problem("conflict", "Session is closing; send again once it is offline")
	}
	return state, nil
}

func (chat chatSession) prompt(ctx context.Context, state events.SessionState, input runtime.Input) (runtime.InputReceipt, error) {
	var receipt runtime.InputReceipt
	state, err := chat.awaitClose(ctx, state)
	if err != nil {
		return receipt, err
	}
	if state != "offline" {
		if err := chat.requireCurrent(ctx); err != nil {
			return receipt, err
		}
		err := chat.client.SendPrompt(ctx, chat.ref, input, &receipt)
		var operation *runtime.OperationError
		if !errors.As(err, &operation) || operation.Reason != "session_resume_required" {
			return receipt, err
		}
		// MASS rejected the input before dispatch. Check whether idle close won.
		state, readErr := chat.state(ctx)
		if readErr != nil {
			return receipt, readErr
		}
		state, readErr = chat.awaitClose(ctx, state)
		if readErr != nil {
			return receipt, readErr
		}
		if state != "offline" {
			return receipt, err
		}
	}
	var resumed resource.SessionResumed
	if err := chat.requireCurrent(ctx); err != nil {
		return receipt, err
	}
	if err := chat.client.ResumeSessionWithInput(ctx, chat.ref, input, &resumed); err != nil {
		return receipt, err
	}
	if !previewSessionMatches(resumed.Session, chat.binding) || resumed.InitialInputReceipt == nil || resumed.InitialInputReceipt.Result == "" {
		return receipt, problem("outcome_unknown", "Resume input result unconfirmed; inspect the event stream before sending again")
	}
	return *resumed.InitialInputReceipt, nil
}

func (s *Server) failSessionChat(w http.ResponseWriter, err error) {
	var operation *runtime.OperationError
	if errors.As(err, &operation) {
		err = problem(operation.Code, operation.Message)
	}
	if errorCode(err) == "outcome_unknown" {
		var failure *Error
		if errors.As(err, &failure) {
			s.write(w, http.StatusServiceUnavailable, coreHTTPError{Error: failure})
			return
		}
	}
	s.fail(w, err)
}
