package sa

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const (
	managementPrefix      = "/v1/management"
	maxManagementHTTPBody = 1 << 20
)

// NewManagementHandler returns the authenticated loopback management API.
// Every endpoint requires the management bearer token, including health.
func NewManagementHandler(service *WorkerService, token string) (http.Handler, error) {
	if service == nil || strings.TrimSpace(token) == "" {
		return nil, operationError("invalid_argument", "invalid_management_handler", "management service and token are required")
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !validManagementAuthorization(request.Header.Get("Authorization"), token) {
			writer.Header().Set("WWW-Authenticate", `Bearer realm="mass-sa-management"`)
			writeManagementError(writer, http.StatusUnauthorized, capabilityInvalid(), nil)
			return
		}
		if !strings.HasPrefix(request.URL.Path, managementPrefix) {
			http.NotFound(writer, request)
			return
		}
		path := strings.TrimPrefix(request.URL.Path, managementPrefix)
		service.handleManagement(writer, request, path)
	}), nil
}

func (s *WorkerService) handleManagement(writer http.ResponseWriter, request *http.Request, path string) {
	switch {
	case path == "/outputs" && request.Method == http.MethodGet:
		query := request.URL.Query()
		req := OutputReadRequest{ProjectID: query.Get("projectId"), Consumer: query.Get("consumer"), Source: query.Get("source"), ThreadID: query.Get("threadId")}
		if raw := query.Get("after"); raw != "" {
			value, err := strconv.ParseUint(raw, 10, 64)
			if err != nil {
				writeManagementError(writer, http.StatusBadRequest, operationError("invalid_argument", "invalid_output_cursor", "after must be an unsigned sequence"), nil)
				return
			}
			req.After = &value
		}
		if raw := query.Get("limit"); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil {
				writeManagementError(writer, http.StatusBadRequest, operationError("invalid_argument", "invalid_output_limit", "limit must be an integer"), nil)
				return
			}
			req.Limit = value
		}
		result, err := s.ReadOutputs(req)
		if err != nil {
			writeManagementError(writer, managementStatus(err), err, nil)
			return
		}
		writeJSON(writer, http.StatusOK, result)
	case path == "/outputs/ack" && request.Method == http.MethodPost:
		var body OutputAckRequest
		if err := readManagementJSON(writer, request, &body); err != nil {
			writeManagementError(writer, managementStatus(err), err, nil)
			return
		}
		result, err := s.AckOutput(body)
		if err != nil {
			writeManagementError(writer, managementStatus(err), err, nil)
			return
		}
		writeJSON(writer, http.StatusOK, result)
	case strings.HasPrefix(path, "/tickets/") && strings.HasSuffix(path, "/reconcile") && request.Method == http.MethodPost:
		var body struct {
			ProjectID string `json:"projectId"`
		}
		if err := readManagementJSON(writer, request, &body); err != nil {
			writeManagementError(writer, managementStatus(err), err, nil)
			return
		}
		ticketID := strings.TrimSuffix(strings.TrimPrefix(path, "/tickets/"), "/reconcile")
		result, err := s.tickets.ReconcileTicketTransition(request.Context(), body.ProjectID, ticketID)
		if err != nil {
			writeManagementError(writer, managementStatus(err), err, nil)
			return
		}
		writeJSON(writer, http.StatusOK, result)
	case path == "/triggers" && request.Method == http.MethodPost:
		var body TriggerRequest
		if err := readManagementJSON(writer, request, &body); err != nil {
			writeManagementError(writer, managementStatus(err), err, nil)
			return
		}
		result, err := s.AcceptTrigger(body)
		if err != nil {
			writeManagementError(writer, managementStatus(err), err, nil)
			return
		}
		writeJSON(writer, http.StatusAccepted, result)
	case strings.HasPrefix(path, "/triggers/") && request.Method == http.MethodGet:
		projectID := request.URL.Query().Get("projectId")
		if _, err := s.project(projectID); err != nil {
			writeManagementError(writer, managementStatus(err), err, nil)
			return
		}
		result, ok := s.store.Snapshot().Projects[projectID].Triggers[strings.TrimPrefix(path, "/triggers/")]
		if !ok {
			writeManagementError(writer, http.StatusNotFound, workerNotFound(), nil)
			return
		}
		writeJSON(writer, http.StatusOK, result)
	case path == "/tickets" && request.Method == http.MethodPost:
		var body struct {
			ProjectID  string `json:"projectId"`
			Title      string `json:"title"`
			Objective  string `json:"objective"`
			Acceptance string `json:"acceptance"`
		}
		if err := readManagementJSON(writer, request, &body); err != nil {
			writeManagementError(writer, managementStatus(err), err, nil)
			return
		}
		if body.Title == "" {
			body.Title = summarizeWorkerMessage(body.Objective)
		}
		if body.Acceptance == "" {
			body.Acceptance = "Explicit acceptance of the requested objective"
		}
		result, err := s.createTicket(request.Context(), body.ProjectID, createTicketArgs{Title: body.Title, Objective: body.Objective, Acceptance: body.Acceptance})
		if err != nil {
			writeManagementError(writer, managementStatus(err), err, nil)
			return
		}
		writeJSON(writer, http.StatusCreated, result)
	case strings.HasPrefix(path, "/tickets/") && strings.HasSuffix(path, "/inspect") && request.Method == http.MethodGet:
		ticketID := strings.TrimSuffix(strings.TrimPrefix(path, "/tickets/"), "/inspect")
		result, err := s.InspectTicket(request.URL.Query().Get("projectId"), ticketID)
		if err != nil {
			writeManagementError(writer, managementStatus(err), err, nil)
			return
		}
		writeJSON(writer, http.StatusOK, result)
	case strings.HasPrefix(path, "/tickets/") && request.Method == http.MethodGet:
		result, err := s.ticket(request.URL.Query().Get("projectId"), strings.TrimPrefix(path, "/tickets/"))
		if err != nil {
			writeManagementError(writer, managementStatus(err), err, nil)
			return
		}
		writeJSON(writer, http.StatusOK, result)
	case path == "/health" && request.Method == http.MethodGet:
		writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
	case path == "/events" && request.Method == http.MethodPost:
		var body AcceptEventRequest
		if err := readManagementJSON(writer, request, &body); err != nil {
			writeManagementError(writer, http.StatusBadRequest, err, nil)
			return
		}
		if strings.TrimSpace(body.Source) == "" {
			body.Source = "management"
		}
		result, err := s.AcceptEvent(body)
		if err != nil {
			writeManagementError(writer, managementStatus(err), err, nil)
			return
		}
		writeJSON(writer, http.StatusCreated, result)
	case path == "/workers" && request.Method == http.MethodGet:
		result, err := s.ListWorkers(request.URL.Query().Get("projectId"), request.URL.Query().Get("rootTicketId"))
		if err != nil {
			writeManagementError(writer, managementStatus(err), err, nil)
			return
		}
		writeJSON(writer, http.StatusOK, result)
	case path == "/routes" && request.Method == http.MethodGet:
		bindings, err := s.ListEventRoutes(request.URL.Query().Get("projectId"), request.URL.Query().Get("rootTicketId"))
		if err != nil {
			writeManagementError(writer, managementStatus(err), err, nil)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"routeBindings": bindings})
	case path == "/routes" && request.Method == http.MethodPost:
		var body BindEventRouteRequest
		if err := readManagementJSON(writer, request, &body); err != nil {
			writeManagementError(writer, http.StatusBadRequest, err, nil)
			return
		}
		result, err := s.BindEventRoute(body)
		if err != nil {
			writeManagementError(writer, managementStatus(err), err, nil)
			return
		}
		writeJSON(writer, http.StatusCreated, result)
	case path == "/routes/revoke" && request.Method == http.MethodPost:
		var body struct {
			ProjectID    string `json:"projectId"`
			RootTicketID string `json:"rootTicketId"`
			RouteKey     string `json:"routeKey"`
		}
		if err := readManagementJSON(writer, request, &body); err != nil {
			writeManagementError(writer, http.StatusBadRequest, err, nil)
			return
		}
		result, err := s.RevokeEventRoute(body.ProjectID, body.RootTicketID, body.RouteKey)
		if err != nil {
			writeManagementError(writer, managementStatus(err), err, nil)
			return
		}
		writeJSON(writer, http.StatusOK, result)
	default:
		http.NotFound(writer, request)
	}
}

func validManagementAuthorization(header, expected string) bool {
	scheme, token, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return false
	}
	actual := strings.TrimSpace(token)
	return len(actual) == len(expected) && subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) == 1
}

func readManagementJSON(writer http.ResponseWriter, request *http.Request, target any) error {
	body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, maxManagementHTTPBody))
	if err != nil {
		return operationError("invalid_argument", "management_body_too_large", "management request body exceeds the size limit")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return operationError("invalid_argument", "invalid_management_body", "management request body is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return operationError("invalid_argument", "invalid_management_body", "management request body does not match the declared schema")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return operationError("invalid_argument", "invalid_management_body", "management request body must contain one JSON object")
	}
	return nil
}

func writeManagementError(writer http.ResponseWriter, status int, err error, value any) {
	var operation *OperationError
	if !errors.As(err, &operation) {
		operation = &OperationError{Code: "unavailable", Reason: "management_failure", Message: "management operation failed"}
	}
	envelope := map[string]any{"error": operation}
	if value != nil {
		envelope["result"] = value
	}
	writeJSON(writer, status, envelope)
}

func managementStatus(err error) int {
	var operation *OperationError
	if !errors.As(err, &operation) {
		return http.StatusInternalServerError
	}
	switch operation.Code {
	case "invalid_argument":
		return http.StatusBadRequest
	case "unauthorized":
		return http.StatusUnauthorized
	case "not_found":
		return http.StatusNotFound
	case "already_exists", "failed_precondition":
		return http.StatusConflict
	case "unavailable":
		return http.StatusServiceUnavailable
	case "outcome_unknown":
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}
