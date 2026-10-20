package transport_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

// Protocol fixture: business semantics are tested independently at Service.
type workAPI struct {
	transport.API
	answers chan any
	refs    chan resource.SessionRef
}

func (a *workAPI) ResolveSession(ns, target string) (resource.SessionRef, error) {
	return resource.SessionRef{SessionID: "a%2Fb"}, nil
}
func (a *workAPI) SendPrompt(ctx context.Context, ref resource.SessionRef, input runtime.Input) (runtime.InputReceipt, error) {
	a.refs <- ref
	raw, err := json.Marshal(input.Content)
	no := false
	return runtime.InputReceipt{Submission: string(raw), Acceptance: runtime.Acceptance{Sent: runtime.Yes(), NativeAccepted: &no}}, err
}
func (a *workAPI) AnswerInteraction(ctx context.Context, ref resource.SessionRef, interaction string, answer any) (runtime.InteractionReceipt, error) {
	a.refs <- ref
	a.answers <- answer
	return runtime.InteractionReceipt{Interaction: interaction}, nil
}
func (a *workAPI) Cancel(ctx context.Context, ref resource.SessionRef) (runtime.CancelReceipt, error) {
	a.refs <- ref
	return runtime.CancelReceipt{Target: "original-work", Result: "controlAcknowledged"}, nil
}

type WorkHTTPSuite struct {
	suite.Suite
	url string
	api *workAPI
}

func (s *WorkHTTPSuite) SetupTest() {
	s.api = &workAPI{answers: make(chan any, 64), refs: make(chan resource.SessionRef, 64)}
	handler, err := transport.New(s.api, transport.Options{Token: "test-token"})
	s.Require().NoError(err)
	s.T().Cleanup(handler.Shutdown)
	server := httptest.NewServer(handler)
	s.T().Cleanup(server.Close)
	s.url = server.URL
}
func (s *WorkHTTPSuite) call(path, body string) (int, string) {
	req, err := http.NewRequest("POST", s.url+path, strings.NewReader(body))
	s.Require().NoError(err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	response, err := http.DefaultClient.Do(req)
	s.Require().NoError(err)
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	s.Require().NoError(err)
	s.Equal("no-store", response.Header.Get("Cache-Control"))
	return response.StatusCode, string(raw)
}
func (s *WorkHTTPSuite) TestWorkOperationsHaveTypedSchemasOnBothURLs() {
	raw, err := transport.OpenAPI()
	s.Require().NoError(err)
	var doc map[string]any
	s.Require().NoError(json.Unmarshal(raw, &doc))
	paths := doc["paths"].(map[string]any)
	for _, base := range []string{"/apis/v1/sessions/{sessionId}", "/apis/v1/namespaces/{namespace}/sessions/{nameOrID}"} {
		for _, suffix := range []string{"/inputs", "/cancel", "/interactions/{interaction64}/answer"} {
			s.Contains(paths, base+suffix)
		}
	}
}
func TestWorkHTTPSuite(t *testing.T) { suite.Run(t, new(WorkHTTPSuite)) }

func (s *WorkHTTPSuite) TestOpenAnswersPreserveNullScalarArrayObjectAndLargeNumbers() {
	for _, base := range []string{"/apis/v1/sessions/a%252Fb", "/apis/v1/namespaces/one/sessions/demo"} {
		for _, answer := range []string{`null`, `true`, `"text"`, `9007199254740993`, `[null,9007199254740993]`, `{"value":9007199254740993}`} {
			status, body := s.call(base+"/interactions/YS9i/answer", answer)
			s.Require().Equal(200, status, body)
			s.JSONEq(`{"interaction":"a/b","evidence":{}}`, body)
			encoded, err := json.Marshal(<-s.api.answers)
			s.Require().NoError(err)
			s.Equal(answer, string(encoded))
			s.Equal(resource.SessionRef{SessionID: "a%2Fb"}, <-s.api.refs)
		}
		status, body := s.call(base+"/interactions/YS9i/answer", "")
		s.Equal(400, status, body)
	}
}
func (s *WorkHTTPSuite) TestContentSchemaDescribesPublicUnion() {
	raw, err := transport.OpenAPI()
	s.Require().NoError(err)
	var doc map[string]any
	s.Require().NoError(json.Unmarshal(raw, &doc))
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	block := schemas["ContentBlock"].(map[string]any)
	s.Require().Contains(block, "oneOf")
	s.Len(block["oneOf"], 5)
	s.Equal("type", block["discriminator"].(map[string]any)["propertyName"])
}

func (s *WorkHTTPSuite) TestAllContentBranchesRoundTripOnBothURLs() {
	blocks := []string{
		`{"type":"text","text":"hello","annotations":{"audience":["user"],"priority":0,"lastModified":"2026-10-20T01:02:03Z"}}`,
		`{"type":"image","data":"aGk=","mimeType":"image/png","uri":"remote:image"}`,
		`{"type":"audio","data":"","mimeType":"audio/wav"}`,
		`{"type":"resource_link","uri":"remote:file","name":"file","size":9007199254740993}`,
		`{"type":"resource_link","uri":"remote:file","name":"large","size":18446744073709551616}`,
		`{"type":"resource","resource":{"uri":"remote:file","text":"content"}}`,
		`{"type":"resource","resource":{"uri":"remote:file","blob":"aGk="}}`,
	}
	for _, base := range []string{"/apis/v1/sessions/a%252Fb", "/apis/v1/namespaces/one/sessions/demo"} {
		for _, block := range blocks {
			status, body := s.call(base+"/inputs", `{"content":[`+block+`]}`)
			s.Require().Equal(200, status, body)
			var receipt runtime.InputReceipt
			s.Require().NoError(json.Unmarshal([]byte(body), &receipt))
			// JSONEq decodes via float64; compare compact wire strings for large values.
			var actual []json.RawMessage
			s.Require().NoError(json.Unmarshal([]byte(receipt.Submission), &actual))
			var expected, got any
			before := json.NewDecoder(strings.NewReader(block))
			before.UseNumber()
			after := json.NewDecoder(strings.NewReader(string(actual[0])))
			after.UseNumber()
			s.Require().NoError(before.Decode(&expected))
			s.Require().NoError(after.Decode(&got))
			s.Equal(expected, got)
			s.Equal(resource.SessionRef{SessionID: "a%2Fb"}, <-s.api.refs)
			s.Equal(runtime.Yes(), receipt.Acceptance.Sent)
			s.Require().NotNil(receipt.Acceptance.NativeAccepted)
			s.False(*receipt.Acceptance.NativeAccepted)
			s.Nil(receipt.Acceptance.NativeRecorded)
		}
	}
}
func (s *WorkHTTPSuite) TestWorkRequestsRejectMalformedStructuresWithoutDispatch() {
	for _, base := range []string{"/apis/v1/sessions/a%252Fb", "/apis/v1/namespaces/one/sessions/demo"} {
		for _, body := range []string{`null`, `{}`, `{"content":null}`, `{"content":[]}`, `{"content":[{"type":"image","data":"aGk=","mimeType":"image/png","text":"extra"}]}`, `{"content":[{"type":"resource","resource":{"uri":"x","text":"a","blob":""}}]}`, `{"content":[{"type":"text","text":"a","text":"b"}]}`, `{"content":[],"sessionId":"other"}`} {
			status, response := s.call(base+"/inputs", body)
			s.Equal(400, status, response)
		}
		for _, body := range []string{`null`, `[]`, `{"unexpected":true}`} {
			status, response := s.call(base+"/cancel", body)
			s.Equal(400, status, response)
		}
		for _, ref := range []string{"YS9i=", "YS9j/", "_w", "YR", "Li4="} {
			status, response := s.call(base+"/interactions/"+ref+"/answer", `null`)
			s.True(status == 400 || status == 404, response)
		}
		for _, body := range []string{`{"a":1,"a":2}`, `[1] true`, `NaN`, ``} {
			status, response := s.call(base+"/interactions/YS9i/answer", body)
			s.Equal(400, status, response)
		}
		status, response := s.call(base+"/cancel?unknown=1", `{}`)
		s.Equal(400, status, response)
	}
	s.Empty(s.api.refs)
	s.Empty(s.api.answers)
}

func (s *WorkHTTPSuite) TestAuthenticationAndIdempotencyRejectBeforeDispatch() {
	for _, base := range []string{"/apis/v1/sessions/a%252Fb", "/apis/v1/namespaces/one/sessions/demo"} {
		for _, suffix := range []string{"/inputs", "/cancel", "/interactions/YS9i/answer"} {
			for _, authenticated := range []bool{false, true} {
				req, err := http.NewRequest("POST", s.url+base+suffix, strings.NewReader(`{}`))
				s.Require().NoError(err)
				req.Header.Set("Content-Type", "application/json")
				expected := 401
				if authenticated {
					req.Header.Set("Authorization", "Bearer test-token")
					req.Header["Idempotency-Key"] = []string{""}
					expected = 400
				}
				response, err := http.DefaultClient.Do(req)
				s.Require().NoError(err)
				raw, err := io.ReadAll(response.Body)
				response.Body.Close()
				s.Require().NoError(err)
				s.Equal(expected, response.StatusCode, string(raw))
			}
		}
	}
	s.Empty(s.api.refs)
	s.Empty(s.api.answers)
}
func (s *WorkHTTPSuite) TestCancelAndDotInteractionAreControlReceipts() {
	for _, base := range []string{"/apis/v1/sessions/a%252Fb", "/apis/v1/namespaces/one/sessions/demo"} {
		for _, body := range []string{"", `{}`, `{ }`} {
			status, response := s.call(base+"/cancel", body)
			s.Equal(200, status, response)
			s.JSONEq(`{"target":"original-work","result":"controlAcknowledged"}`, response)
			<-s.api.refs
		}
		status, response := s.call(base+"/interactions/Li4/answer", `null`)
		s.Equal(200, status, response)
		s.JSONEq(`{"interaction":"..","evidence":{}}`, response)
		<-s.api.refs
		<-s.api.answers
	}
}
