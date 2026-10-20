package transport_test

import (
	"encoding/json"
	"github.com/stretchr/testify/suite"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
	"github.com/zoumo/mass/mass-go/pkg/transport"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type RuntimeHTTPSuite struct {
	suite.Suite
	url string
}
type httpConfigFactory struct{}

func (httpConfigFactory) Validate(runtime.Config) error { return nil }
func (httpConfigFactory) New(string, runtime.Config) (runtime.Driver, error) {
	panic("HTTP config access must not construct a Driver")
}
func (s *RuntimeHTTPSuite) SetupTest() {
	st, err := file.Open(s.T().TempDir(), false)
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.Require().NoError(st.Close()) })
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fixture": httpConfigFactory{}}))
	handler, err := transport.New(svc, transport.Options{Token: "test-token"})
	s.Require().NoError(err)
	s.T().Cleanup(handler.Shutdown)
	server := httptest.NewServer(handler)
	s.T().Cleanup(server.Close)
	s.url = server.URL
}
func (s *RuntimeHTTPSuite) call(method, path, body string) (int, http.Header, string) {
	req, err := http.NewRequest(method, s.url+path, strings.NewReader(body))
	s.Require().NoError(err)
	req.Header.Set("Authorization", "Bearer test-token")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	s.Require().NoError(err)
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	s.Require().NoError(err)
	return res.StatusCode, res.Header, string(b)
}
func (s *RuntimeHTTPSuite) TestRuntimeConfigurationRoundTrip() {
	status, headers, body := s.call("POST", "/apis/v1/runtimes", `{"metadata":{"name":"agent"},"spec":{"type":"fixture","config":{"old":true,"large":9007199254740993}}}`)
	s.Require().Equal(201, status, body)
	s.Equal("/apis/v1/runtimes/agent", headers.Get("Location"))
	s.Equal("application/json", headers.Get("Content-Type"))
	s.Contains(body, "9007199254740993")
	s.Contains(body, `"state":"stopped"`)
	status, _, body = s.call("PUT", "/apis/v1/runtimes/agent", `{"type":"fixture","config":{"new":true}}`)
	s.Require().Equal(200, status, body)
	s.NotContains(body, `"old"`)
	s.Contains(body, `"generation":2`)
	status, _, body = s.call("GET", "/apis/v1/runtimes/agent", "")
	s.Require().Equal(200, status, body)
	s.Contains(body, `"new":true`)
	status, _, body = s.call("GET", "/apis/v1/runtimes?state=stopped&type=fixture", "")
	s.Require().Equal(200, status, body)
	s.True(strings.HasPrefix(body, "["))
	status, _, body = s.call("GET", "/v1/runtimes", "")
	s.Equal(404, status, body)
}
func (s *RuntimeHTTPSuite) TestLargeConfigurationPreservesJSONNumbers() {
	body := `{"metadata":{"name":"large"},"spec":{"type":"fixture","config":{"integer":9007199254740993,"fraction":0.12345678901234567890123456789,"nested":[null,true,{"value":18446744073709551616}],"payload":"` + strings.Repeat("x", (1<<20)+1) + `"}}}`
	status, headers, response := s.call("POST", "/apis/v1/runtimes", body)
	s.Require().Equal(201, status)
	s.Equal("no-store", headers.Get("Cache-Control"))
	s.Contains(response, "9007199254740993")
	s.Contains(response, "0.12345678901234567890123456789")
	s.Contains(response, "18446744073709551616")
	status, _, response = s.call("GET", "/apis/v1/runtimes/large", "")
	s.Require().Equal(200, status)
	s.Contains(response, "9007199254740993")
	s.Contains(response, "0.12345678901234567890123456789")
}
func (s *RuntimeHTTPSuite) TestStrictConfigurationBoundary() {
	for _, body := range []string{
		`null`, `{}`, `{"metadata":{"name":"agent","uid":""},"spec":{"type":"fixture","config":{}}}`,
		`{"metadata":{"name":"agent"},"spec":{"type":"fixture","config":{"nested":[{"x":1,"x":2}]}}}`,
		`{"metadata":{"name":"agent"},"spec":{"type":"fixture","config":null}}`,
		`{"metadata":{"name":"agent"},"spec":{"type":"fixture","config":{}},"status":{}}`,
		`{"metadata":{"name":"agent"},"spec":{"type":"fixture","config":{},"unknown":true}}`,
	} {
		status, _, response := s.call("POST", "/apis/v1/runtimes", body)
		s.Equal(400, status, response)
		s.Contains(response, `"code":"invalid_argument"`)
		s.NotContains(response, "errors")
	}
	status, _, response := s.call("GET", "/apis/v1/runtimes", "")
	s.Equal(200, status)
	s.JSONEq("[]", response)
}
func (s *RuntimeHTTPSuite) TestOpenAPIDeclaresRuntimeContract() {
	raw, err := transport.OpenAPI()
	s.Require().NoError(err)
	var doc map[string]any
	s.Require().NoError(json.Unmarshal(raw, &doc))
	paths := doc["paths"].(map[string]any)
	s.Contains(paths, "/apis/v1/runtimes")
	s.NotContains(paths, "/v1/runtimes")
	resourcePath := paths["/apis/v1/runtimes/{runtime}"].(map[string]any)
	s.Contains(resourcePath, "put")
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	fields := schemas["Metadata"].(map[string]any)["properties"].(map[string]any)
	for _, key := range []string{"uid", "createdAt", "generation", "resourceVersion"} {
		s.Equal(true, fields[key].(map[string]any)["readOnly"], key)
	}
	spec := schemas["RuntimeSpec"].(map[string]any)["properties"].(map[string]any)
	s.NotContains(spec["config"].(map[string]any), "anyOf")
	errors := schemas["OperationError"].(map[string]any)
	s.ElementsMatch([]any{"code", "reason", "message"}, errors["required"])
	s.NotContains(string(raw), "application/problem+json")
}
func TestRuntimeHTTPSuite(t *testing.T) { suite.Run(t, new(RuntimeHTTPSuite)) }
