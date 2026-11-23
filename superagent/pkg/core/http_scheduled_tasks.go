package core

import (
	"io"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

const scheduledTasksPath = coreResourcePrefix + "scheduled-tasks"

type scheduledTaskTriggerInput struct {
	OperationID string `json:"operationId" minLength:"1"`
}

type scheduledTaskDeletion struct {
	ScheduledTaskID string `json:"scheduledTaskId"`
}

type scheduledTaskReadProjection struct {
	ScheduledTask
	TraceAssociations []messageTrace `json:"traceAssociations,omitempty"`
}

func (s *Server) registerScheduledTaskResources(api *httpAPI) {
	task := api.schema(reflect.TypeFor[ScheduledTask]())
	taskParam := pathParam("scheduledTaskId")
	path := scheduledTasksPath + "/{scheduledTaskId}"
	operation := func(method, path, id, description string, response *huma.Schema) huma.Operation {
		op := api.messageOperation(method, path, id, description, response)
		op.Tags = []string{"Operator"}
		op.Security = bearerSecurity(operatorBearer)
		op.Description = "Requires a Core operator Bearer credential and a direct loopback CLI connection."
		op.Responses["401"] = api.coreError("Operator credential required")
		return op
	}
	registerRead := func(op huma.Operation, handler http.HandlerFunc) {
		api.registerMessageRead(op, s.serveOperatorScheduledTask(handler))
	}
	registerWrite := func(op huma.Operation, handler http.HandlerFunc) {
		op.Responses["409"] = api.coreError("Request conflicts with the current task or operation")
		api.registerRaw(op, s.serveOperatorScheduledTask(handler))
	}

	list := operation(http.MethodGet, scheduledTasksPath, "listScheduledTasks", "List scheduled tasks", pageSchema(task))
	list.Parameters = []*huma.Param{
		pageLimit(50), queryString("cursor", "Opaque continuation for the same filters"),
		queryString("order", "Use newest for newest-first order"),
		queryString("ownerType", "Owner type. Use topic with ownerId"), queryString("ownerId", "Owner ID. Supply with ownerType"),
	}
	registerRead(list, s.listScheduledTasks)

	create := operation(http.MethodPost, scheduledTasksPath, "createScheduledTask", "Create an independent scheduled task", task)
	create.RequestBody = &huma.RequestBody{Required: true, Content: jsonContent(api.schema(reflect.TypeFor[CreateScheduledTaskRequest]()))}
	registerWrite(create, s.postScheduledTask)

	read := operation(http.MethodGet, path, "getScheduledTask", "Read a scheduled task", api.schema(reflect.TypeFor[scheduledTaskReadProjection]()))
	read.Parameters = []*huma.Param{taskParam, traceQuery()}
	registerRead(read, s.readScheduledTask)

	update := operation(http.MethodPatch, path, "updateScheduledTask", "Update an independent scheduled task", task)
	update.Parameters = []*huma.Param{taskParam}
	update.RequestBody = &huma.RequestBody{Required: true, Content: jsonContent(api.schema(reflect.TypeFor[scheduledTaskPatch]()))}
	registerWrite(update, s.patchScheduledTask)

	deleteOperation := operation(http.MethodDelete, path, "deleteScheduledTask", "Delete an independent scheduled task", api.schema(reflect.TypeFor[scheduledTaskDeletion]()))
	minimum := 1.0
	deleteOperation.Parameters = []*huma.Param{taskParam, {Name: "expectedRevision", In: "query", Required: true, Schema: &huma.Schema{Type: huma.TypeInteger, Minimum: &minimum}}}
	registerWrite(deleteOperation, s.deleteScheduledTask)

	trigger := operation(http.MethodPost, path+"/trigger", "triggerScheduledTask", "Trigger an active scheduled task immediately", api.schema(reflect.TypeFor[Receipt]()))
	trigger.Parameters = []*huma.Param{taskParam}
	trigger.RequestBody = &huma.RequestBody{Required: true, Content: jsonContent(api.schema(reflect.TypeFor[scheduledTaskTriggerInput]()))}
	registerWrite(trigger, s.postScheduledTaskTrigger)

	history := operation(http.MethodGet, path+"/trigger-messages", "listScheduledTaskTriggerMessages", "List Trigger Messages produced by a scheduled task", api.schema(reflect.TypeFor[messagePage[triggerMessageRecord]]()))
	history.Parameters = append(messageListParams(), taskParam)
	registerRead(history, s.listScheduledTaskTriggerMessages)
}

func (s *Server) serveOperatorScheduledTask(handler http.HandlerFunc) http.HandlerFunc {
	return s.observeMessageHTTP(func(w http.ResponseWriter, r *http.Request) {
		if s.authorizeOperator(w, r) {
			handler(w, r)
		}
	})
}

func (s *Server) listScheduledTasks(w http.ResponseWriter, r *http.Request) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		s.fail(w, problem("invalid_argument", "invalid scheduled task query"))
		return
	}
	for key, values := range q {
		if !slices.Contains([]string{"ownerType", "ownerId", "limit", "order", "cursor"}, key) || len(values) != 1 || strings.TrimSpace(values[0]) == "" {
			s.fail(w, problem("invalid_argument", "unknown, blank or repeated scheduled task query field"))
			return
		}
	}
	if (q.Has("ownerType") != q.Has("ownerId")) || (q.Has("ownerType") && q.Get("ownerType") != "topic") {
		s.fail(w, problem("invalid_argument", "ownerType=topic and ownerId must be supplied together"))
		return
	}
	if order := q.Get("order"); order != "" && order != "newest" {
		s.fail(w, problem("invalid_argument", "order must be newest"))
		return
	}
	limit, err := coreListLimit(q.Get("limit"), 50)
	if err != nil {
		s.fail(w, err)
		return
	}
	page, err := s.store.listScoped(r.Context(), QueryRequest{
		Collection: "scheduled-tasks", OwnerType: q.Get("ownerType"), OwnerID: q.Get("ownerId"),
		Limit: limit, Cursor: q.Get("cursor"), newest: q.Get("order") == "newest",
	}, nil, nil)
	s.writeScheduledTask(w, page, err)
}

func (s *Server) readScheduledTask(w http.ResponseWriter, r *http.Request) {
	s.readMessageRecord(w, r, "scheduled-tasks", resourcePathValue(r, "scheduledTaskId"))
}

func (s *Server) postScheduledTask(w http.ResponseWriter, r *http.Request) {
	var in CreateScheduledTaskRequest
	if err := decodeScheduledTaskBody(w, r, &in); err != nil {
		s.fail(w, err)
		return
	}
	result, err := s.store.commitScheduledTaskOperator(r.Context(), "create_scheduled_task", in.OperationID, createScheduledTask(in))
	s.writeScheduledTask(w, result, err)
}

type scheduledTaskPatch struct {
	ExpectedRevision int `json:"expectedRevision" minimum:"1"`
	ScheduledTaskChanges
}

func (r *scheduledTaskPatch) UnmarshalJSON(data []byte) error {
	type wire scheduledTaskPatch
	return decodeScheduledTaskUpdate(data, (*wire)(r))
}

func (*scheduledTaskPatch) TransformSchema(_ huma.Registry, schema *huma.Schema) *huma.Schema {
	for _, field := range scheduledTaskChangeFields {
		schema.AnyOf = append(schema.AnyOf, &huma.Schema{Required: []string{field}})
	}
	return schema
}

func (s *Server) patchScheduledTask(w http.ResponseWriter, r *http.Request) {
	var in scheduledTaskPatch
	if err := decodeScheduledTaskBody(w, r, &in); err != nil {
		s.fail(w, err)
		return
	}
	id := resourcePathValue(r, "scheduledTaskId")
	operationID := id + ":" + strconv.Itoa(in.ExpectedRevision)
	change := UpdateScheduledTaskRequest{
		ScheduledTaskRevision: ScheduledTaskRevision{ScheduledTaskID: id, OperationID: operationID, ExpectedRevision: in.ExpectedRevision},
		ScheduledTaskChanges:  in.ScheduledTaskChanges,
	}
	result, err := s.store.commitScheduledTaskOperator(r.Context(), "update_scheduled_task", operationID, updateScheduledTask(change))
	s.writeScheduledTask(w, result, err)
}

func (s *Server) deleteScheduledTask(w http.ResponseWriter, r *http.Request) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	var revision int
	if err == nil && len(q) == 1 && len(q["expectedRevision"]) == 1 {
		revision, err = strconv.Atoi(q.Get("expectedRevision"))
	}
	if err != nil || revision < 1 {
		s.fail(w, problem("invalid_argument", "expectedRevision must be one positive integer"))
		return
	}
	id := resourcePathValue(r, "scheduledTaskId")
	in := ScheduledTaskRevision{ScheduledTaskID: id, ExpectedRevision: revision, OperationID: id + ":" + strconv.Itoa(revision)}
	result, err := s.store.commitScheduledTaskOperator(r.Context(), "delete_scheduled_task", in.OperationID, deleteScheduledTask(in))
	s.writeScheduledTask(w, result, err)
}

func (s *Server) postScheduledTaskTrigger(w http.ResponseWriter, r *http.Request) {
	var in scheduledTaskTriggerInput
	if err := decodeScheduledTaskBody(w, r, &in); err != nil {
		s.fail(w, err)
		return
	}
	result, err := s.store.triggerScheduledTask(r.Context(), resourcePathValue(r, "scheduledTaskId"), in.OperationID)
	s.writeScheduledTask(w, result, err)
}

func (s *Server) listScheduledTaskTriggerMessages(w http.ResponseWriter, r *http.Request) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || q.Has("view") || q.Has("sourceMessageId") || hasOperatorMessageFilter(q) {
		s.fail(w, problem("invalid_argument", "invalid scheduled task history query"))
		return
	}
	id := resourcePathValue(r, "scheduledTaskId")
	exists, err := s.store.scheduledTaskExists(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if !exists {
		s.fail(w, problem("not_found", "scheduled task not found"))
		return
	}
	q.Set("scheduledTaskId", id)
	s.listMessageRecords(w, r, "messages", q)
}

func decodeScheduledTaskBody(w http.ResponseWriter, r *http.Request, input any) error {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return problem("invalid_argument", "Content-Type must be application/json")
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		return problem("invalid_argument", "scheduled task request exceeds the body limit or cannot be read")
	}
	return decodeStrict(body, input)
}

func (s *Server) writeScheduledTask(w http.ResponseWriter, result any, err error) {
	if err != nil {
		s.fail(w, err)
		return
	}
	s.write(w, http.StatusOK, result)
}
