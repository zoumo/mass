package service_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/transport"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"time"
)

func (s *SessionWorkSuite) fact(id string, p events.Payload) events.SessionEvent {
	e, err := events.NewEvent(events.EventBase{EventID: uuid.NewString(), Namespace: "one", SessionName: "demo", SessionID: "original", ItemID: id, Time: time.Now().UTC()}, p, s.driver.source)
	s.Require().NoError(err)
	return e
}
func (s *SessionWorkSuite) TestHistoryHTTPFilteringAndIndependentCursors() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sub, err := s.svc.SubscribeEvents(s.ref)
	s.Require().NoError(err)
	defer sub.Close()
	facts := []events.SessionEvent{
		s.fact("first", events.AgentMessage{MessageID: "first", Content: []events.ContentBlock{events.Text("first")}}),
		s.fact("second", events.ToolCall{ID: "second", Kind: "execute", RawInput: json.Number("9007199254740993")}),
		s.fact("third", events.AgentMessage{MessageID: "third", Content: []events.ContentBlock{events.Text("third")}}),
	}
	for _, e := range facts {
		s.Require().NoError(s.driver.pipe.Send(ctx, e))
		got, err := sub.Next(ctx)
		s.Require().NoError(err)
		s.Equal(e.EventID, got.EventID)
	}
	handler, err := transport.New(s.svc, transport.Options{Token: "test-token"})
	s.Require().NoError(err)
	defer handler.Shutdown()
	server := httptest.NewServer(handler)
	defer server.Close()
	get := func(path string, status int) store.HistoryPage {
		req, err := http.NewRequestWithContext(ctx, "GET", server.URL+path, nil)
		s.Require().NoError(err)
		req.Header.Set("Authorization", "Bearer test-token")
		res, err := server.Client().Do(req)
		s.Require().NoError(err)
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		s.Require().NoError(err)
		s.Require().Equal(status, res.StatusCode, string(data))
		var page store.HistoryPage
		if status == 200 {
			s.Require().NoError(json.Unmarshal(data, &page))
		}
		return page
	}
	base := "/apis/v1/sessions/original/events"
	first := get(base+"?types=agent.message&types=agent.tool_call&types=agent.message&limit=1", 200)
	s.Require().Len(first.Events, 1)
	s.Equal(facts[0].EventID, first.Events[0].EventID)
	s.Equal(facts[2].EventID, *first.SyncedEventID)
	s.NotEmpty(first.Next)
	next := get("/apis/v1/namespaces/one/sessions/demo/events?types=agent.tool_call&types=agent.message&limit=8&after="+url.QueryEscape(first.Next), 200)
	s.Require().Len(next.Events, 2)
	s.Empty(next.Next)
	s.Equal(facts[1].EventID, next.Events[0].EventID)
	s.Equal(facts[2].EventID, *next.SyncedEventID)
	independent := get(base+"?types=agent.message&limit=1", 200)
	s.Equal(facts[0].EventID, independent.Events[0].EventID)
	for _, q := range []string{"?types=agent.message&after=" + url.QueryEscape(first.Next), "?after=" + facts[0].EventID, "?types=", "?types=unknown", "?types=agent.message,agent.tool_call", "?types[]=agent.message", "?limit=0", "?limit=-1", "?limit=1&limit=2", "?limit=+1", "?unknown=x"} {
		get(base+q, 400)
	}
	empty := get(base+"?types=agent.message_chunk", 200)
	s.Empty(empty.Events)
	s.Equal(facts[2].EventID, *empty.SyncedEventID)
	_, err = s.svc.StopRuntime(ctx, "agent")
	s.Require().NoError(err)
	stopped := get(base, 200)
	s.Len(stopped.Events, 3)
	r, err := s.svc.GetRuntime("agent")
	s.Require().NoError(err)
	s.Equal("stopped", r.Status.State)
}
func (s *SessionWorkSuite) TestBoundedIndependentObservationDoesNotCancelWork() {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	slow, err := s.svc.SubscribeEvents(s.ref)
	s.Require().NoError(err)
	defer slow.Close()
	healthy, err := s.svc.SubscribeEvents(s.ref)
	s.Require().NoError(err)
	defer healthy.Close()
	for i := 0; i < 140; i++ {
		e := s.fact(fmt.Sprint(i), events.AgentMessage{MessageID: fmt.Sprint(i), Content: []events.ContentBlock{events.Text("committed")}})
		s.Require().NoError(s.driver.pipe.Send(ctx, e))
		got, err := healthy.Next(ctx)
		s.Require().NoError(err)
		s.Equal(e.EventID, got.EventID)
	}
	_, err = slow.Next(ctx)
	var public *runtime.OperationError
	s.Require().ErrorAs(err, &public)
	s.Equal("resource_exhausted", public.Code)
	s.Equal("slow_subscriber", public.Reason)
	s.NoError(slow.Close())
	_, err = slow.Next(ctx)
	s.Require().ErrorAs(err, &public)
	s.NoError(healthy.Close())
	s.NoError(healthy.Close())
	_, err = healthy.Next(ctx)
	s.ErrorIs(err, io.EOF)
	page, err := s.svc.ListHistoryEvents(s.ref, store.HistoryQuery{Limit: 200})
	s.Require().NoError(err)
	s.Len(page.Events, 140)
	session, err := s.svc.GetSession(s.ref)
	s.Require().NoError(err)
	s.NotContains([]events.SessionState{"closing", "archived"}, session.Status.State)
}
func (s *SessionWorkSuite) TestSSEEstablishmentErrorsRemainJSON() {
	handler, err := transport.New(s.svc, transport.Options{Token: "test-token"})
	s.Require().NoError(err)
	defer handler.Shutdown()
	server := httptest.NewServer(handler)
	defer server.Close()
	for _, base := range []string{"/apis/v1/sessions/original", "/apis/v1/namespaces/one/sessions/demo"} {
		for _, suffix := range []string{"?after=x", "?lastEventId=x", "?retry=1", "?types=agent.message"} {
			req, err := http.NewRequest("GET", server.URL+base+"/events/stream"+suffix, nil)
			s.Require().NoError(err)
			req.Header.Set("Authorization", "Bearer test-token")
			res, err := server.Client().Do(req)
			s.Require().NoError(err)
			data, _ := io.ReadAll(res.Body)
			res.Body.Close()
			s.Equal(400, res.StatusCode, string(data))
			s.Equal("application/json", res.Header.Get("Content-Type"))
			s.NotContains(string(data), "subscription.ready")
		}
	}
	for _, headers := range []http.Header{{"Last-Event-Id": []string{"resume"}, "Authorization": []string{"Bearer test-token"}}, {"Authorization": []string{"Bearer wrong"}}} {
		req, _ := http.NewRequest("GET", server.URL+"/apis/v1/sessions/original/events/stream", nil)
		req.Header = headers
		res, err := server.Client().Do(req)
		s.Require().NoError(err)
		res.Body.Close()
		s.Contains([]int{400, 401}, res.StatusCode)
		s.Equal("application/json", res.Header.Get("Content-Type"))
	}
	_, err = s.svc.StopRuntime(context.Background(), "agent")
	s.Require().NoError(err)
	req, _ := http.NewRequest("GET", server.URL+"/apis/v1/sessions/original/events/stream", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	res, err := server.Client().Do(req)
	s.Require().NoError(err)
	defer res.Body.Close()
	s.Equal(503, res.StatusCode)
	s.Equal("application/json", res.Header.Get("Content-Type"))
	s.Empty(res.Header.Get("X-Accel-Buffering"))
}

func readFrame(reader *bufio.Reader) (string, error) {
	var frame strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return frame.String(), err
		}
		frame.WriteString(line)
		if line == "\n" {
			return frame.String(), nil
		}
	}
}
func (s *SessionWorkSuite) TestSSEReadyLiveOnlyArchiveAndServerShutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	prior, err := s.svc.SubscribeEvents(s.ref)
	s.Require().NoError(err)
	e := s.fact("old", events.AgentMessage{MessageID: "old", Content: []events.ContentBlock{events.Text("old")}})
	s.Require().NoError(s.driver.pipe.Send(ctx, e))
	_, err = prior.Next(ctx)
	s.Require().NoError(err)
	s.NoError(prior.Close())
	handler, err := transport.New(s.svc, transport.Options{Token: "test-token"})
	s.Require().NoError(err)
	defer handler.Shutdown()
	server := httptest.NewServer(handler)
	defer server.Close()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/apis/v1/namespaces/one/sessions/demo/events/stream", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	res, err := server.Client().Do(req)
	s.Require().NoError(err)
	defer res.Body.Close()
	s.Equal(200, res.StatusCode)
	s.Equal("no-cache, no-store, no-transform", res.Header.Get("Cache-Control"))
	s.Equal("no", res.Header.Get("X-Accel-Buffering"))
	reader := bufio.NewReader(res.Body)
	frame, err := readFrame(reader)
	s.Require().NoError(err)
	s.Equal("event: subscription.ready\ndata: {}\n\n", frame)
	fresh := s.fact("new", events.AgentMessage{MessageID: "new", Content: []events.ContentBlock{events.Text("new")}})
	s.Require().NoError(s.driver.pipe.Send(ctx, fresh))
	frame, err = readFrame(reader)
	s.Require().NoError(err)
	s.Contains(frame, fresh.EventID)
	s.NotContains(frame, e.EventID)
	s.NotContains(frame, "\nid:")
	s.NotContains(frame, "\nretry:")
	page, err := s.svc.ListHistoryEvents(s.ref, store.HistoryQuery{})
	s.Require().NoError(err)
	s.Require().Len(page.Events, 2)
	s.Equal(fresh.EventID, page.Events[1].EventID)
	_, err = s.svc.ArchiveSession(ctx, s.ref)
	s.Require().NoError(err)
	handler.Shutdown()
	frame, err = readFrame(reader)
	s.Require().NoError(err)
	s.Contains(frame, "event: subscription.error")
	s.Contains(frame, "server_shutdown")
	_, err = reader.ReadByte()
	s.ErrorIs(err, io.EOF)
	session, err := s.svc.GetSession(resource.SessionRef{SessionID: "original"})
	s.Require().NoError(err)
	s.Equal(events.SessionState("archived"), session.Status.State)
}

// captureObservation exposes the public subscription handle to exercise a
// normal observer close independently from Runtime or Session lifecycle.
type captureObservation struct {
	*service.Service
	subscriptions chan *service.Subscription
}

func (a captureObservation) SubscribeEvents(ref resource.SessionRef) (*service.Subscription, error) {
	sub, err := a.Service.SubscribeEvents(ref)
	if err == nil {
		a.subscriptions <- sub
	}
	return sub, err
}
func (s *SessionWorkSuite) TestSSEExplicitObserverCloseSendsClosed() {
	captured := captureObservation{s.svc, make(chan *service.Subscription, 1)}
	handler, err := transport.New(captured, transport.Options{Token: "test-token"})
	s.Require().NoError(err)
	defer handler.Shutdown()
	server := httptest.NewServer(handler)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/apis/v1/sessions/original/events/stream", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	res, err := server.Client().Do(req)
	s.Require().NoError(err)
	defer res.Body.Close()
	reader := bufio.NewReader(res.Body)
	ready, err := readFrame(reader)
	s.Require().NoError(err)
	s.Contains(ready, "subscription.ready")
	select {
	case sub := <-captured.subscriptions:
		s.NoError(sub.Close())
	case <-ctx.Done():
		s.FailNow("subscription handle missing")
	}
	frame, err := readFrame(reader)
	s.Require().NoError(err)
	s.Equal("event: subscription.closed\ndata: {}\n\n", frame)
	_, err = reader.ReadByte()
	s.ErrorIs(err, io.EOF)
	session, err := s.svc.GetSession(s.ref)
	s.Require().NoError(err)
	s.NotContains([]events.SessionState{"archived", "closing"}, session.Status.State)
}

func (s *SessionWorkSuite) TestAllEventBranchesUseSameLiveAndHistoryModel() {
	data, err := os.ReadFile("../../../harness/modules/runtime/event-examples.json")
	s.Require().NoError(err)
	var examples struct {
		Events []events.SessionEvent `json:"events"`
	}
	s.Require().NoError(json.Unmarshal(data, &examples))
	s.Require().Len(examples.Events, 17)
	handler, err := transport.New(s.svc, transport.Options{Token: "test-token"})
	s.Require().NoError(err)
	defer handler.Shutdown()
	server := httptest.NewServer(handler)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/apis/v1/sessions/original/events/stream", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	res, err := server.Client().Do(req)
	s.Require().NoError(err)
	defer res.Body.Close()
	reader := bufio.NewReader(res.Body)
	_, err = readFrame(reader)
	s.Require().NoError(err)
	persistent := []string{}
	for _, example := range examples.Events {
		base := example.EventBase
		base.Namespace = "one"
		base.SessionName = "demo"
		base.SessionID = "original"
		e, err := events.NewEvent(base, example.Payload(), s.driver.source)
		s.Require().NoError(err)
		s.Require().NoError(s.driver.pipe.Send(ctx, e))
		frame, err := readFrame(reader)
		s.Require().NoError(err)
		s.Require().True(strings.HasPrefix(frame, "event: session.event\ndata: "), frame)
		wire := strings.TrimSuffix(strings.TrimPrefix(frame, "event: session.event\ndata: "), "\n\n")
		encoded, err := json.Marshal(e)
		s.Require().NoError(err)
		s.JSONEq(string(encoded), wire)
		if e.Persistent() {
			persistent = append(persistent, e.EventID)
		}
	}
	page, err := s.svc.ListHistoryEvents(s.ref, store.HistoryQuery{})
	s.Require().NoError(err)
	s.Require().Len(page.Events, 5)
	for i, e := range page.Events {
		s.Equal(persistent[i], e.EventID)
	}
}
