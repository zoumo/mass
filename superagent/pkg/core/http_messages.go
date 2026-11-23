package core

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

const (
	triggerMessagesPath = "/v1/core/trigger-messages"
	outboxMessagesPath  = "/v1/core/outbox-messages"
)

func (s *Server) observeMessageHTTP(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.store.telemetry.enabled {
			observed, request := s.store.telemetry.startHTTP(w, r)
			defer observed.finishRequest()
			next(observed, request)
			return
		}
		next(w, r)
	}
}

func (s *Server) postTriggerMessage(w http.ResponseWriter, r *http.Request) {
	source, ok := s.authenticate(r)
	if !ok {
		s.fail(w, problem("unauthorized", "valid source credential is required"))
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		s.fail(w, problem("invalid_argument", "Content-Type must be application/json"))
		return
	}
	receipt, err := s.store.submitChannelMessage(r.Context(), source, r.Body)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.write(w, http.StatusOK, receipt)
}

func (s *Server) readTriggerReceipt(w http.ResponseWriter, r *http.Request) {
	source, ok := s.authenticate(r)
	if !ok {
		s.fail(w, problem("unauthorized", "valid source credential is required"))
		return
	}
	ids := r.URL.Query()["sourceMessageId"]
	if len(ids) != 1 || strings.TrimSpace(ids[0]) == "" {
		s.fail(w, problem("invalid_argument", "one nonblank sourceMessageId is required"))
		return
	}
	receipt, err := s.store.channelMessageReceipt(r.Context(), source, ids[0])
	if err != nil {
		s.fail(w, err)
		return
	}
	s.write(w, http.StatusOK, receipt)
}

func (s *Server) readTriggerMessages(w http.ResponseWriter, r *http.Request) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q["view"]) > 1 {
		s.fail(w, problem("invalid_argument", "invalid Trigger Message query"))
		return
	}
	if q.Has("sourceMessageId") {
		if len(q) != 1 {
			s.fail(w, problem("invalid_argument", "sourceMessageId must be the only query parameter"))
			return
		}
		s.readTriggerReceipt(w, r)
		return
	}
	switch q.Get("view") {
	case "":
		s.listMessageRecords(w, r, "messages", q)
	case "router":
		s.listMessageRecords(w, r, "router-messages", q)
	default:
		s.fail(w, problem("invalid_argument", "unsupported Trigger Message view"))
	}
}

func (s *Server) readTriggerMessage(w http.ResponseWriter, r *http.Request) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || q.Has("sourceMessageId") || q.Has("view") {
		s.fail(w, problem("invalid_argument", "invalid Trigger Message detail query"))
		return
	}
	s.readMessageRecord(w, r, "messages", chi.URLParam(r, "messageId"))
}

func (s *Server) readOutboxMessages(w http.ResponseWriter, r *http.Request) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q["view"]) > 1 {
		s.fail(w, problem("invalid_argument", "invalid Outbox Message query"))
		return
	}
	id := chi.URLParam(r, "outboxId")
	switch q.Get("view") {
	case "delivery":
		source, err := s.authenticateOutboxConsumer(r)
		if err != nil {
			s.fail(w, err)
			return
		}
		s.readOutboxDelivery(w, r, id, id == "", source)
	case "":
		if id == "" {
			s.listMessageRecords(w, r, "outbox", q)
			return
		}
		s.readMessageRecord(w, r, "outbox", id)
	default:
		s.fail(w, problem("invalid_argument", "unsupported Outbox Message view"))
	}
}

func (s *Server) readMessageRecord(w http.ResponseWriter, r *http.Request, kind, id string) {
	result, err := s.store.getRecord(r.Context(), kind, id, nil, nil, r.URL.Query().Get("trace") == "true")
	if err != nil {
		s.fail(w, err)
		return
	}
	s.write(w, http.StatusOK, result)
}

func (s *Server) listMessageRecords(w http.ResponseWriter, r *http.Request, kind string, q url.Values) {
	limit := 50
	if kind == "router-messages" {
		limit = 20
	}
	if value := q.Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			s.fail(w, problem("invalid_argument", "limit must be an integer"))
			return
		}
		limit = parsed
	}
	query := QueryRequest{
		Collection: kind, ThreadKey: q.Get("threadKey"), TopicID: q.Get("topicId"), IssueID: q.Get("issueId"),
		MessageID: q.Get("messageId"), Cursor: q.Get("cursor"), Limit: limit,
		preview: q.Get("preview") == "true", newest: q.Get("order") == "newest", observerTrace: q.Get("trace") == "true",
	}
	var page Page
	var err error
	if kind == "router-messages" {
		if q.Get("order") != "" && !query.newest {
			s.fail(w, problem("invalid_argument", "Router view uses newest order"))
			return
		}
		page, err = s.store.observerRouterMessages(r.Context(), query)
	} else {
		page, err = s.store.listScoped(r.Context(), query, nil, nil)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.write(w, http.StatusOK, page)
}

type outboxPatch struct {
	Expected  OutboxState `json:"expected"`
	Outcome   string      `json:"outcome" enum:"pending,sent,retryable_failure,permanent_failure,unknown"`
	TaskID    string      `json:"taskId,omitempty"`
	ErrorCode string      `json:"errorCode,omitempty"`
}

func (s *Server) patchOutboxMessage(w http.ResponseWriter, r *http.Request) {
	source, err := s.authenticateOutboxConsumer(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	var patch outboxPatch
	if err := decodeOutboxResult(w, r, &patch); err != nil {
		s.fail(w, err)
		return
	}
	s.writeOutboxResult(w, r, source, OutboxCommand{
		OutboxID: chi.URLParam(r, "outboxId"), Expected: patch.Expected, Outcome: patch.Outcome,
		TaskID: patch.TaskID, ErrorCode: patch.ErrorCode,
	})
}
