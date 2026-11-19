package core_test

import (
	"context"
	"encoding/json"
	"maps"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/zoumo/mass/mass-go/pkg/events"

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
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+channelOutbox+"?limit=200", http.NoBody)
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
func serveObserverBrowserAudit(t *testing.T, audit *observerBrowserAudit, boundary *routeBoundary, f *fixture, p *process, binding core.Binding, roleToken string) {
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
	driverID := binding.Namespace + "/" + binding.Name
	boundary.driver.mu.Lock()
	source := boundary.driver.sources[driverID]
	boundary.driver.mu.Unlock()
	emit := func(ctx context.Context, text string, stored bool) error {
		message := events.MessagePayload{MessageID: uuid.NewString(), Content: []events.ContentBlock{events.Text(text)}}
		var payload events.Payload = events.MessageChunk(message)
		if stored {
			payload = events.AgentMessage(message)
		}
		event, err := events.NewEvent(events.EventBase{
			EventID: uuid.NewString(), ItemID: message.MessageID, Time: time.Now().UTC(),
			Namespace: binding.Namespace, SessionName: binding.Name, SessionID: driverID,
		}, payload, source)
		if err != nil {
			return err
		}
		return boundary.driver.pipe.Send(ctx, event)
	}
	require.NoError(t, emit(t.Context(), "Stored before browsing <b>plain text</b> "+f.token+" "+roleToken, true))

	pathCore := seedMessagePathObserver(t)
	conversation := seedTopicConversation(t, 22)
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
		if err := postReport(r.Context(), conversation.process, conversation.workerToken, conversation.report); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, _ *http.Request) {
		audit.mu.Lock()
		methods := maps.Clone(audit.methods)
		audit.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"rpc": methods, "polls": audit.polls.Load(), "sentOffers": audit.offers.Load(), "pendingOffers": audit.pending.Load(),
			"channelSends": audit.sends.Load(), "sentId": sentID, "bindingId": binding.ID,
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
