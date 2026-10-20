package transport_test

import (
	"encoding/json"
	"github.com/stretchr/testify/suite"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
	"github.com/zoumo/mass/mass-go/pkg/transport"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

type SessionHTTPSuite struct {
	suite.Suite
	url string
	st  *file.Store
}

func (s *SessionHTTPSuite) call(method, path, body string) (int, http.Header, string) {
	helper := RuntimeHTTPSuite{url: s.url}
	helper.SetT(s.T())
	return helper.call(method, path, body)
}
func (s *SessionHTTPSuite) SetupTest() {
	st, err := file.Open(s.T().TempDir(), false)
	s.Require().NoError(err)
	s.st = st
	s.T().Cleanup(func() { s.NoError(st.Close()) })
	handler, err := transport.New(service.New(st, st, nil), transport.Options{Token: "test-token"})
	s.Require().NoError(err)
	s.T().Cleanup(handler.Shutdown)
	server := httptest.NewServer(handler)
	s.T().Cleanup(server.Close)
	s.url = server.URL
}
func (s *SessionHTTPSuite) seed(ns, name, id string) {
	_, err := s.st.CreateSession(resource.Session{Metadata: resource.Metadata{Namespace: ns, Name: name}, Spec: resource.SessionSpec{RuntimeRef: "agent"}, Status: resource.SessionStatus{SessionID: id, State: "offline"}})
	s.Require().NoError(err)
}
func (s *SessionHTTPSuite) TestRawIDAndNameOrIDAddressSamePersistentSession() {
	ids := []string{"a/b", "a%2Fb", "a b?#", "空间/会话", "a..b"}
	for i, id := range ids {
		name := []string{"slash", "percent", "space", "unicode", "dots"}[i]
		s.seed("one", name, id)
		for _, path := range []string{"/apis/v1/sessions/" + url.PathEscape(id), "/apis/v1/namespaces/one/sessions/" + url.PathEscape(id), "/apis/v1/namespaces/one/sessions/" + name} {
			status, headers, body := s.call("GET", path, "")
			s.Require().Equal(200, status, body)
			s.Equal("no-store", headers.Get("Cache-Control"))
			var got resource.Session
			s.Require().NoError(json.Unmarshal([]byte(body), &got))
			s.Equal(id, got.Status.SessionID)
			s.Equal(name, got.Metadata.Name)
		}
	}
	s.seed("one", "alpha", "beta")
	s.seed("one", "beta", "other")
	status, _, body := s.call("GET", "/apis/v1/namespaces/one/sessions/beta", "")
	s.Equal(409, status, body)
	status, _, body = s.call("GET", "/apis/v1/namespaces/two/sessions/a%2Fb", "")
	s.Equal(404, status, body)
}
func TestSessionHTTPSuite(t *testing.T) { suite.Run(t, new(SessionHTTPSuite)) }

func (s *SessionHTTPSuite) TestSessionSchemaAndStrictQueryContract() {
	raw, err := transport.OpenAPI()
	s.Require().NoError(err)
	var doc map[string]any
	s.Require().NoError(json.Unmarshal(raw, &doc))
	paths := doc["paths"].(map[string]any)
	for _, path := range []string{"/apis/v1/sessions", "/apis/v1/sessions/{sessionId}", "/apis/v1/namespaces/{namespace}/sessions/{nameOrID}"} {
		s.Contains(paths, path)
	}
	for _, path := range []string{"/apis/v1/sessions?limit=%2B1", "/apis/v1/sessions?limit=0", "/apis/v1/sessions?limit=1&limit=2", "/apis/v1/sessions?unknown=x", "/apis/v1/sessions?namespace=", "/apis/v1/sessions?state=madeup"} {
		status, _, body := s.call("GET", path, "")
		s.Equal(400, status, body)
	}
	for _, base := range []string{"/apis/v1/sessions/", "/apis/v1/namespaces/one/sessions/"} {
		for _, id := range []string{"%2E", ".%2E", "%2E%2E", "%FF"} {
			status, _, body := s.call("GET", base+id, "")
			s.Equal(400, status, body)
			s.Contains(body, `"reason":"invalid_session_id"`)
		}
	}
}

func (s *SessionHTTPSuite) TestUUIDCreationReturnsBusinessReasonBeforeRuntimeAccess() {
	for _, name := range []string{"00000000-0000-0000-0000-000000000000", "12345678-ABCD-FFFF-0000-123456789ABC"} {
		body := `{"metadata":{"namespace":"one","name":"` + name + `"},"spec":{"runtimeRef":"missing","cwd":"/remote"}}`
		status, _, response := s.call("POST", "/apis/v1/sessions", body)
		s.Equal(400, status, response)
		s.Contains(response, `"reason":"session_name_is_uuid"`)
	}
}
