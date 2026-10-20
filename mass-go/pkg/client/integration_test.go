package client_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/client/generated"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type generatedHTTPSuite struct{ suite.Suite }

func TestGeneratedHTTPSuite(t *testing.T) { suite.Run(t, new(generatedHTTPSuite)) }

func (s *generatedHTTPSuite) TestRawIDsThroughActualHTTP() {
	st, err := file.Open(s.T().TempDir(), false)
	s.Require().NoError(err)
	defer st.Close()
	handler, err := transport.New(service.New(st, st, nil), transport.Options{Token: "test-token"})
	s.Require().NoError(err)
	defer handler.Shutdown()
	h := httptest.NewServer(handler)
	defer h.Close()
	c := client.Client{URL: h.URL, Token: "test-token"}
	for i, id := range []string{"a/b", "a%2Fb", "a b?#", "空间/会话", "a..b", "  "} {
		name := fmt.Sprintf("session-%d", i)
		_, err = st.CreateSession(resource.Session{Metadata: resource.Metadata{Namespace: "demo", Name: name}, Spec: resource.SessionSpec{RuntimeRef: "agent"}, Status: resource.SessionStatus{SessionID: id, State: "offline"}})
		s.Require().NoError(err)
		for _, ref := range []resource.SessionRef{{SessionID: id}, {Namespace: "demo", Name: name}, {Namespace: "demo", Name: id}} {
			var got resource.Session
			s.Require().NoError(c.GetSession(s.T().Context(), ref, &got))
			s.Equal(id, got.Status.SessionID)
			var history store.HistoryPage
			s.Require().NoError(c.History(s.T().Context(), ref, store.HistoryQuery{}, &history))
			s.Empty(history.Events)
		}
	}
	for _, id := range []string{".", "..", "", string([]byte{0xff})} {
		var got resource.Session
		err = c.GetSession(s.T().Context(), resource.SessionRef{SessionID: id}, &got)
		var public *transport.Error
		s.Require().ErrorAs(err, &public)
		s.Equal("invalid_argument", public.Code)
	}
}

func (s *generatedHTTPSuite) TestAllEventBranchesStreamAndGeneratedModelRoundTrip() {
	b, err := os.ReadFile("../../../harness/modules/runtime/event-examples.json")
	s.Require().NoError(err)
	var examples struct {
		Events []json.RawMessage `json:"events"`
	}
	s.Require().NoError(json.Unmarshal(b, &examples))
	s.Require().Len(examples.Events, 17)
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event:subscription.ready\r\ndata:{}\r\n\r\n")
		w.(http.Flusher).Flush()
		for _, raw := range examples.Events {
			compact, err := json.Marshal(raw)
			if err != nil {
				return
			}
			fmt.Fprintf(w, "event:session.event\ndata:%s\n\n", compact)
			w.(http.Flusher).Flush()
		}
		fmt.Fprint(w, "event:subscription.closed\ndata:{}\n\n")
	}))
	defer h.Close()
	observer, err := (client.Client{URL: h.URL}).Observe(context.Background(), resource.SessionRef{SessionID: "s1"})
	s.Require().NoError(err)
	defer observer.Close()
	for _, raw := range examples.Events {
		event, err := observer.Next()
		s.Require().NoError(err)
		encoded, err := json.Marshal(event)
		s.Require().NoError(err)
		s.JSONEq(string(raw), string(encoded))
		var model generated.SessionEvent
		s.Require().NoError(json.Unmarshal(raw, &model))
		encoded, err = json.Marshal(model)
		s.Require().NoError(err)
		s.JSONEq(string(raw), string(encoded))
	}
	_, err = observer.Next()
	s.Require().ErrorIs(err, io.EOF)
}
