package core_test

import (
	"context"
	"encoding/json"
	"maps"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/massd/pkg/resource"
	"github.com/zoumo/mass/massd/pkg/runtime"
	"github.com/zoumo/mass/superagent/pkg/core"
)

type observerBrowserAudit struct {
	mu          sync.Mutex
	methods     map[string]int
	closeLatest func()
	polls       atomic.Int64
	offers      atomic.Int64
	pending     atomic.Int64
	sends       atomic.Int64
}

func (a *observerBrowserAudit) recordRPC(method string, closeConnection func()) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.methods[method]++
	if method == "session.subscribe" {
		a.closeLatest = closeConnection
	}
	return false
}

func (a *observerBrowserAudit) cutObservation() bool {
	a.mu.Lock()
	closeConnection := a.closeLatest
	a.closeLatest = nil
	a.mu.Unlock()
	if closeConnection == nil {
		return false
	}
	closeConnection()
	return true
}

func (a *observerBrowserAudit) pollOutbox(ctx context.Context, endpoint, token, sentID string) {
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+channelOutbox+"?view=delivery&limit=200", http.NoBody)
			if err != nil {
				return
			}
			req.Header.Set("Authorization", "Bearer "+token)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				continue
			}
			var page struct {
				Items []core.OutboxItem
			}
			err = json.NewDecoder(resp.Body).Decode(&page)
			_ = resp.Body.Close()
			if err != nil || resp.StatusCode != http.StatusOK {
				continue
			}
			a.polls.Add(1)
			for _, item := range page.Items {
				if item.ID == sentID {
					a.offers.Add(1)
				}
				if item.Content.Type == "markdown" && string(item.Content.Data) == string(markdownReply("Observer reply pending").Data) {
					a.pending.Add(1)
				}
			}
		}
	}
}

// These controls live on a separate loopback listener in the Go fixture.
// Browser traffic is observed through the production Core listener.
func serveObserverBrowserAudit(t *testing.T, audit *observerBrowserAudit, boundary *routeBoundary, f *fixture, p *process, binding core.SessionBinding, roleToken string) {
	t.Helper()
	var sentID string
	for _, row := range page(t, p, "outbox?preview=true").Items {
		var item struct {
			ID      string `json:"id"`
			Preview string `json:"preview"`
		}
		require.NoError(t, json.Unmarshal(row, &item))
		if item.Preview == "Observer reply sent" {
			sentID = item.ID
		}
	}
	require.NotEmpty(t, sentID)
	emit := func(ctx context.Context, text string, stored bool) error {
		return emitObserverMessage(ctx, boundary.driver, binding, text, stored)
	}
	require.NoError(t, emit(t.Context(), "Stored before browsing <b>plain text</b> "+f.token+" "+roleToken, true))

	pathCore := seedMessagePathObserver(t)
	conversation := seedTopicConversation(t, 22)
	rebound := seedObserverSessionReuse(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /message-path-fixture", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"endpoint": pathCore})
	})
	mux.HandleFunc("GET /topic-conversation-fixture", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"endpoint": conversation.process.endpoint})
	})
	mux.HandleFunc("POST /topic-conversation-fixture/report", func(w http.ResponseWriter, r *http.Request) {
		if err := postReport(r.Context(), conversation.process, conversation.issueToken, conversation.report); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /session-reuse-fixture", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rebound)
	})
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, _ *http.Request) {
		audit.mu.Lock()
		methods := maps.Clone(audit.methods)
		audit.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"rpc": methods, "polls": audit.polls.Load(), "sentOffers": audit.offers.Load(), "pendingOffers": audit.pending.Load(),
			"channelSends": audit.sends.Load(), "sentId": sentID, "sessionBindingId": binding.ID,
		})
	})
	mux.HandleFunc("POST /emit-live", func(w http.ResponseWriter, r *http.Request) {
		if err := emit(r.Context(), "Live before disconnect "+f.token+" "+roleToken, false); err != nil {
			http.Error(w, "could not publish test event", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /emit-stored", func(w http.ResponseWriter, r *http.Request) {
		if err := emit(r.Context(), "Stored while live", true); err != nil {
			http.Error(w, "could not publish test event", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /disconnect", func(w http.ResponseWriter, r *http.Request) {
		if !audit.cutObservation() {
			http.Error(w, "no subscription to disconnect", http.StatusConflict)
			return
		}
		for index, text := range []string{"Transient during disconnect", "Stored during disconnect"} {
			if err := emit(r.Context(), text, index == 1); err != nil {
				http.Error(w, "could not publish test event", http.StatusInternalServerError)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
	port := os.Getenv("SUPERAGENT_OBSERVER_AUDIT_PORT")
	if port == "" {
		port = "18795"
	}
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:"+port)
	require.NoError(t, err)
	control := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	done := make(chan error, 1)
	go func() { done <- control.Serve(listener) }()
	ctx, cancel := context.WithCancel(t.Context())
	pollDone := make(chan struct{})
	go func() {
		defer close(pollDone)
		audit.pollOutbox(ctx, p.endpoint, f.token, sentID)
	}()
	t.Cleanup(func() {
		cancel()
		<-pollDone
		require.NoError(t, control.Close())
		require.ErrorIs(t, <-done, http.ErrServerClosed)
		t.Logf("Observer audit released; Outbox polls=%d sent offers=%d controlled sends=%d", audit.polls.Load(), audit.offers.Load(), audit.sends.Load())
	})
}

func emitObserverMessage(ctx context.Context, driver *routeDriver, binding core.SessionBinding, text string, stored bool) error {
	message := events.MessagePayload{MessageID: uuid.NewString(), Content: []events.ContentBlock{events.Text(text)}}
	var payload events.Payload = events.MessageChunk(message)
	if stored {
		payload = events.AgentMessage(message)
	}
	return emitObserverEvent(ctx, driver, binding, message.MessageID, payload)
}

func emitObserverEvent(ctx context.Context, driver *routeDriver, binding core.SessionBinding, itemID string, payload events.Payload) error {
	driverID := binding.Namespace + "/" + binding.Name
	driver.mu.Lock()
	source := driver.sources[driverID]
	driver.mu.Unlock()
	event, err := events.NewEvent(events.EventBase{
		EventID: uuid.NewString(), ItemID: itemID, Time: time.Now().UTC(),
		Namespace: binding.Namespace, SessionName: binding.Name, SessionID: driverID,
	}, payload, source)
	if err != nil {
		return err
	}
	return driver.pipe.Send(ctx, event)
}

func seedObserverSessionReuse(t *testing.T) map[string]string {
	t.Helper()
	// Exercise the verified Claude release path against MASS with a controlled native driver.
	boundary := newRouteBoundaryWithRuntime(t, "claude")
	boundary.driver.mu.Lock()
	boundary.driver.allowResume = true
	boundary.driver.injectReceipt = runtime.InjectReceipt{Acceptance: runtime.Acceptance{Sent: runtime.Yes(), NativeAccepted: runtime.Yes()}}
	boundary.driver.mu.Unlock()
	f, operatorFile := eventFixture(t, boundary.url)
	p := f.start()
	router := waitBinding(t, p, "router", "", "ready")
	message := submit(t, p, f, conversationInput("reuse-issue-session", "reuse-issue-session"))
	route := decide(t, p, roleToken(t, f, router), newRoute("reuse-issue-session", []string{message.MessageID}, f.dir))
	topic := waitBinding(t, p, "topic", route.Targets[0].TopicID, "ready")
	topicToken := roleToken(t, f, topic)
	create := issueInput(f.dir, "reuse-issue-session")
	create.Title, create.Start = "Resume the sample Issue Session", true
	work := roleWork[core.WorkReceipt](t, p, topicToken, "create_issue", create)
	original := waitBinding(t, p, "issue", work.SessionBinding.ID, "ready")
	waitWorkEvent(t, p, original.StartEventID, "sent")
	state := events.Idle
	require.NoError(t, emitObserverEvent(t.Context(), boundary.driver, original, "", events.StateUpdate{State: &state}))
	require.Eventually(t, func() bool {
		session, err := boundary.svc.GetSession(resource.SessionRef{SessionID: original.SessionID})
		return err == nil && session.Status.State == events.Idle
	}, 5*time.Second, 25*time.Millisecond)
	const stored = "Session history recorded before the binding changed"
	require.NoError(t, emitObserverMessage(t.Context(), boundary.driver, original, stored, true))
	require.Eventually(t, func() bool {
		status, data := request(t, http.MethodGet, p.endpoint, "/v1/observer/session-preview/"+original.ID+"/history", "", nil)
		return status == http.StatusOK && strings.Contains(string(data), stored)
	}, 5*time.Second, 25*time.Millisecond)
	oldReport := roleWork[core.Report](t, p, roleToken(t, f, original), "submit_report", core.ReportRequest{
		OperationID: "before-reuse", Kind: "progress", Summary: "Report before Session reuse", GoalRevision: original.StartGoalRevision,
	})
	canceled := roleWork[core.WorkReceipt](t, p, topicToken, "issue_cancel", issueLifecycle("cancel-reuse", work.Issue))
	waitObservedStop(t, p, "session-bindings", original.ID)
	settled := recoverRoleBinding(t, p, operatorFile, recoveryFor("settle-reuse", "settle", original.ID))
	require.True(t, settled.Close.Reusable)
	reopened := roleWork[core.WorkReceipt](t, p, topicToken, "issue_reopen", issueLifecycle("reopen-reuse", canceled.Issue))
	started := roleWork[core.WorkReceipt](t, p, topicToken, "start_issue", startRequest("start-reuse", reopened.Issue))
	current := waitBinding(t, p, "issue", started.SessionBinding.ID, "ready")
	require.NotEqual(t, original.ID, current.ID)
	require.Equal(t, original.SessionID, current.SessionID)
	require.Equal(t, original.SessionUID, current.SessionUID)
	waitWorkEvent(t, p, current.StartEventID, "sent")
	newReport := roleWork[core.Report](t, p, roleToken(t, f, current), "submit_report", core.ReportRequest{
		OperationID: "after-reuse", Kind: "progress", Summary: "Report after Session reuse", GoalRevision: current.StartGoalRevision,
	})
	return map[string]string{
		"endpoint": p.endpoint, "issueId": work.Issue.ID, "originalSessionBindingId": original.ID, "currentSessionBindingId": current.ID,
		"originalReportId": oldReport.ID, "currentReportId": newReport.ID, "retainedHistory": stored,
	}
}
