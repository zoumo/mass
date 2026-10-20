package transport_test

import (
	"context"
	"encoding/json"

	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

func (a *workAPI) ArchiveSession(_ context.Context, ref resource.SessionRef) (resource.Session, error) {
	a.refs <- ref
	return resource.Session{Status: resource.SessionStatus{SessionID: ref.SessionID, State: "archived"}}, nil
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
		for _, action := range []string{"archive", "resume"} {
			_, ok := paths[base+"/"+action]
			s.True(ok, base+"/"+action)
		}
	}
}

func (s *WorkHTTPSuite) TestRecoveryURLsResolveOneOpaqueIdentityAndStrictEmptyBodies() {
	for _, base := range []string{"/apis/v1/sessions/a%252Fb", "/apis/v1/namespaces/one/sessions/demo"} {
		for _, action := range []string{"archive", "resume"} {
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
