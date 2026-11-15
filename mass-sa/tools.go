package sa

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

const maxToolHTTPBody = 1 << 20

type toolHTTPEnvelope struct {
	Result any             `json:"result,omitempty"`
	Error  *OperationError `json:"error,omitempty"`
}

// HTTPHandler exposes the authenticated internal tool seam. The capability
// token is accepted only in the Authorization header and is never a URL or
// argument value.
func (s *WorkerService) HTTPHandler() http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", http.MethodPost)
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		tool, ok := strings.CutPrefix(request.URL.Path, "/v1/tools/")
		if !ok || tool == "" || strings.Contains(tool, "/") {
			http.NotFound(writer, request)
			return
		}
		token, ok := bearerToken(request.Header.Get("Authorization"))
		if !ok {
			writeToolError(writer, capabilityInvalid())
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, maxToolHTTPBody))
		if err != nil {
			writeToolError(writer, operationError("invalid_argument", "tool_arguments_too_large", "tool arguments exceed the size limit"))
			return
		}
		if len(body) == 0 {
			body = []byte(`{}`)
		}
		result, err := s.CallTool(request.Context(), token, tool, json.RawMessage(body))
		if err != nil {
			writeToolError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, toolHTTPEnvelope{Result: result})
	})
}

func bearerToken(header string) (string, bool) {
	scheme, token, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return "", false
	}
	return strings.TrimSpace(token), true
}

func writeToolError(writer http.ResponseWriter, err error) {
	var operation OperationError
	var pointer *OperationError
	if errors.As(err, &pointer) {
		operation = *pointer
	} else {
		operation = OperationError{
			Code:    "unavailable",
			Reason:  "tool_failure",
			Message: "tool operation failed",
		}
	}
	status := http.StatusInternalServerError
	switch operation.Code {
	case "invalid_argument":
		status = http.StatusBadRequest
	case "unauthorized":
		status = http.StatusUnauthorized
		writer.Header().Set("WWW-Authenticate", `Bearer realm="mass-sa-tools"`)
	case "not_found":
		status = http.StatusNotFound
	case "already_exists", "failed_precondition":
		status = http.StatusConflict
	case "unavailable":
		status = http.StatusServiceUnavailable
	case "outcome_unknown":
		status = http.StatusInternalServerError
	}
	writeJSON(writer, status, toolHTTPEnvelope{Error: &operation})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
