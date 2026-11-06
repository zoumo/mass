package transport_test

import (
	"context"
	"encoding/json"

	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

func (a *workAPI) CloseSession(_ context.Context, ref resource.SessionRef) (resource.Session, error) {
	a.refs <- ref
	if a.closeErr != nil {
		return resource.Session{Status: resource.SessionStatus{SessionID: ref.SessionID, State: "closing", Reason: "session_close_failed"}}, a.closeErr
	}
	return resource.Session{Status: resource.SessionStatus{SessionID: ref.SessionID, State: "offline"}}, nil
}

func (a *workAPI) ResumeSession(_ context.Context, ref resource.SessionRef) (resource.Session, error) {
	a.refs <- ref
	return resource.Session{Status: resource.SessionStatus{SessionID: ref.SessionID, State: "unknown"}}, nil
}

func (s *WorkHTTPSuite) TestRecoveryOperationsHaveTypedSchemasOnBothURLs() {
	raw, err := transport.OpenAPI()
	s.Require().NoError(err)
	var doc map[string]any
	s.Require().NoError(json.Unmarshal(raw, &doc))
	paths := doc["paths"].(map[string]any)
	for _, base := range []string{"/apis/v1/sessions/{sessionId}", "/apis/v1/namespaces/{namespace}/sessions/{nameOrID}"} {
		for _, action := range []string{"close", "resume"} {
			_, ok := paths[base+"/"+action]
			s.True(ok, base+"/"+action)
		}
	}
}

func (s *WorkHTTPSuite) TestRecoveryURLsResolveOneOpaqueIdentityAndStrictEmptyBodies() {
	for _, base := range []string{"/apis/v1/sessions/a%252Fb", "/apis/v1/namespaces/one/sessions/demo"} {
		for _, action := range []string{"close", "resume"} {
			for _, body := range []string{"", "{}"} {
				status, raw := s.call(base+"/"+action, body)
				s.Require().Equal(200, status, raw)
				s.Contains(raw, `"sessionId":"a%2Fb"`)
				s.Equal(resource.SessionRef{SessionID: "a%2Fb"}, <-s.api.refs)
			}
			for _, body := range []string{"null", "[]", "true", `{"unexpected":true}`, `{"x":1,"x":2}`} {
				status, raw := s.call(base+"/"+action, body)
				s.Equal(400, status, raw)
			}
			status, raw := s.call(base+"/"+action+"?target=other", "")
			s.Equal(400, status, raw)
			s.Empty(s.api.refs, "invalid protocol must not reach Service")
		}
	}
}

func (s *WorkHTTPSuite) TestCloseFailureIsRetryableAndUsesPublicReason() {
	base := "/apis/v1/sessions/a%252Fb"
	s.api.closeErr = &runtime.OperationError{Code: "outcome_unknown", Reason: "session_close_failed", Message: "Session close did not complete; inspect state before retrying"}
	status, raw := s.call(base+"/close", "")
	s.Equal(500, status, raw)
	s.JSONEq(`{"code":"outcome_unknown","reason":"session_close_failed","message":"Session close did not complete; inspect state before retrying"}`, raw)
	s.Equal(resource.SessionRef{SessionID: "a%2Fb"}, <-s.api.refs)

	s.api.closeErr = nil
	status, raw = s.call(base+"/close", "")
	s.Equal(200, status, raw)
	s.JSONEq(`{"metadata":{"namespace":"","name":"","uid":"","createdAt":"0001-01-01T00:00:00Z","generation":0,"resourceVersion":""},"spec":{"runtimeRef":"","cwd":""},"status":{"sessionId":"a%2Fb","state":"offline"}}`, raw)
	s.NotContains(raw, "reason")
	s.Equal(resource.SessionRef{SessionID: "a%2Fb"}, <-s.api.refs)
}
