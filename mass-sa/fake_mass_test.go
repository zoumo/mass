package sa

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type fakeMASSServer struct {
	server *httptest.Server
	token  string

	mu                   sync.Mutex
	sessions             map[string]Session
	sessionIDs           map[string]string
	initializeCount      int
	getCount             int
	listCount            int
	createCount          int
	resumeCount          int
	closeCount           int
	promptCount          int
	subscribeCount       int
	historyCount         int
	createPayloads       []json.RawMessage
	listPayloads         []json.RawMessage
	closePayloads        []json.RawMessage
	promptPayloads       []json.RawMessage
	dropCreateReply      bool
	dropPromptReply      bool
	dropCloseReply       bool
	dropResumeReply      bool
	promptError          *OperationError
	getError             *OperationError
	promptGates          map[string]chan struct{}
	createState          string
	resumeState          string
	createConfigMismatch bool

	writeMu          sync.Mutex
	nextEvent        int
	nextSubscription int
	history          map[string][]SessionEvent
	subscriptions    map[string]fakeSubscription
	operations       []string
	promptTurn       fakePromptTurn
}

type fakeSubscription struct {
	conn      *websocket.Conn
	sessionID string
}

type fakePromptTurn struct {
	Prompt           string
	FinalText        string
	Phase            string
	EmitFinal        bool
	EmitHistoryFinal bool
	EmitChunks       bool
	EmitPartial      bool
	PartialText      string
}

func newFakeMASSServer(t *testing.T, token string) *fakeMASSServer {
	t.Helper()
	fake := &fakeMASSServer{
		token:         token,
		sessions:      map[string]Session{},
		sessionIDs:    map[string]string{},
		history:       map[string][]SessionEvent{},
		subscriptions: map[string]fakeSubscription{},
		promptGates:   map[string]chan struct{}{},
		promptTurn: fakePromptTurn{
			FinalText:        `{"status":"completed","summary":"worker completed the ticket"}`,
			Phase:            "final_answer",
			EmitFinal:        true,
			EmitHistoryFinal: true,
		},
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	fake.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/rpc" {
			http.NotFound(writer, request)
			return
		}
		if fake.token != "" && request.Header.Get("Authorization") != "Bearer "+fake.token {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		conn, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		fake.serveConnection(t, conn)
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeMASSServer) endpoint() string {
	return "ws" + strings.TrimPrefix(f.server.URL, "http") + "/rpc"
}

func (f *fakeMASSServer) addSession(session Session) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.storeSessionLocked(session)
}

func (f *fakeMASSServer) setState(sessionID, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	session, ok := f.sessions[sessionID]
	if !ok {
		panic("unknown fake session")
	}
	session.Status.State = state
	f.sessions[sessionID] = session
}

func (f *fakeMASSServer) setPromptTurn(turn fakePromptTurn) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.promptTurn = turn
}

func (f *fakeMASSServer) setResumeResponseState(state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumeState = state
}

func (f *fakeMASSServer) setDropPromptReply(drop bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dropPromptReply = drop
}

func (f *fakeMASSServer) setDropLifecycleReply(operation string, drop bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if operation == "close" {
		f.dropCloseReply = drop
	} else {
		f.dropResumeReply = drop
	}
}

func (f *fakeMASSServer) setCreateResponseState(state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createState = state
}

func (f *fakeMASSServer) setGetError(err *OperationError) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getError = err
}

func (f *fakeMASSServer) blockPromptFinal(sessionID string) chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	gate := make(chan struct{})
	f.promptGates[sessionID] = gate
	return gate
}

func (f *fakeMASSServer) eventCounts() (subscribe, history int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.subscribeCount, f.historyCount
}

func (f *fakeMASSServer) operationOrder() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.operations...)
}

func (f *fakeMASSServer) historyEvents(sessionID string) []SessionEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]SessionEvent(nil), f.history[sessionID]...)
}

func (f *fakeMASSServer) counts() (initialize, get, create, resume int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.initializeCount, f.getCount, f.createCount, f.resumeCount
}

func (f *fakeMASSServer) operationCounts() (list, close, prompt int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listCount, f.closeCount, f.promptCount
}

func (f *fakeMASSServer) creates() []json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]json.RawMessage(nil), f.createPayloads...)
}

func (f *fakeMASSServer) session(sessionID string) (Session, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	session, ok := f.sessions[sessionID]
	return cloneSession(session), ok
}

func (f *fakeMASSServer) storeSessionLocked(session Session) {
	f.sessions[session.Status.SessionID] = cloneSession(session)
	f.sessionIDs[session.Metadata.Namespace+"/"+session.Metadata.Name] = session.Status.SessionID
}

func cloneSession(session Session) Session {
	clone := session
	clone.InitialInputReceipt = append(json.RawMessage(nil), session.InitialInputReceipt...)
	if session.Spec.Config != nil {
		config := *session.Spec.Config
		clone.Spec.Config = &config
	}
	return clone
}

type fakeRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func (f *fakeMASSServer) serveConnection(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	initialized := false
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var request fakeRPCRequest
		if err := json.Unmarshal(raw, &request); err != nil || request.JSONRPC != "2.0" || request.Method == "" || len(request.ID) == 0 {
			return
		}
		if request.Method == "initialize" {
			f.mu.Lock()
			f.initializeCount++
			f.mu.Unlock()
			if initialized {
				f.writeProtocolError(conn, request.ID, "already_initialized")
				return
			}
			initialized = true
			f.writeResult(conn, request.ID, map[string]any{
				"protocolVersion": "1",
				"serverInfo":      map[string]string{"name": "fake-mass", "version": "test"},
			})
			continue
		}
		if !initialized {
			f.writeProtocolError(conn, request.ID, "not_initialized")
			continue
		}
		f.recordOperation(request.Method)

		switch request.Method {
		case "session.get":
			if f.handleGet(conn, request) {
				return
			}
		case "session.list":
			if f.handleList(conn, request) {
				return
			}
		case "session.create":
			if f.handleCreate(conn, request) {
				return
			}
		case "session.resume":
			if f.handleResume(conn, request) {
				return
			}
		case "session.close":
			if f.handleClose(conn, request) {
				return
			}
		case "session.prompt":
			if f.handlePrompt(conn, request) {
				return
			}
		case "session.subscribe":
			if f.handleSubscribe(conn, request) {
				return
			}
		case "session.unsubscribe":
			if f.handleUnsubscribe(conn, request) {
				return
			}
		case "session.history":
			if f.handleHistory(conn, request) {
				return
			}
		default:
			f.writeRPCError(conn, request.ID, OperationError{
				Code: "not_found", Reason: "method_not_found", Message: "Method not found",
			})
		}
	}
}

func (f *fakeMASSServer) recordOperation(method string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.operations = append(f.operations, method)
}

func (f *fakeMASSServer) handleList(conn *websocket.Conn, request fakeRPCRequest) bool {
	var params struct {
		Query *struct {
			Namespace string `json:"namespace"`
			Page      *struct {
				After string `json:"after"`
				Limit int    `json:"limit"`
			} `json:"page"`
		} `json:"query"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil {
		f.writeInvalidParams(conn, request.ID)
		return false
	}
	f.mu.Lock()
	f.listCount++
	f.listPayloads = append(f.listPayloads, append(json.RawMessage(nil), request.Params...))
	var sessions []Session
	for _, session := range f.sessions {
		if params.Query != nil && params.Query.Namespace != "" && session.Metadata.Namespace != params.Query.Namespace {
			continue
		}
		sessions = append(sessions, cloneSession(session))
	}
	f.mu.Unlock()
	f.write(conn, map[string]any{
		"jsonrpc": "2.0",
		"method":  "session.event",
		"params":  map[string]any{"subscriptionId": "ignored", "event": map[string]any{"id": "notice"}},
	})
	f.writeResult(conn, request.ID, SessionPage{Items: sessions})
	return false
}

func (f *fakeMASSServer) handleGet(conn *websocket.Conn, request fakeRPCRequest) bool {
	var params struct {
		Session SessionRef `json:"session"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil {
		f.writeInvalidParams(conn, request.ID)
		return false
	}
	f.mu.Lock()
	f.getCount++
	getError := f.getError
	session, found := f.findSessionLocked(params.Session)
	f.mu.Unlock()
	if getError != nil {
		f.writeRPCError(conn, request.ID, *getError)
		return false
	}
	if !found {
		f.writeRPCError(conn, request.ID, OperationError{
			Code: "not_found", Reason: "resource_not_found", Message: "Resource not found",
		})
		return false
	}
	f.writeResult(conn, request.ID, session)
	return false
}

func (f *fakeMASSServer) handleCreate(conn *websocket.Conn, request fakeRPCRequest) bool {
	var params struct {
		Input CreateSessionInput `json:"input"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil {
		f.writeInvalidParams(conn, request.ID)
		return false
	}
	f.mu.Lock()
	f.createCount++
	f.createPayloads = append(f.createPayloads, append(json.RawMessage(nil), request.Params...))
	key := params.Input.Metadata.Namespace + "/" + params.Input.Metadata.Name
	if _, exists := f.sessionIDs[key]; exists {
		f.mu.Unlock()
		f.writeRPCError(conn, request.ID, OperationError{
			Code: "already_exists", Reason: "resource_exists", Message: "Resource already exists",
		})
		return false
	}
	session := Session{
		Metadata: params.Input.Metadata,
		Spec:     params.Input.Spec,
		Status: SessionStatus{
			SessionID: fmt.Sprintf("native-%d", f.createCount),
			State:     "idle",
		},
	}
	if params.Input.InitialInput != nil {
		session.InitialInputReceipt = json.RawMessage(fmt.Sprintf(`{"status":"accepted","blocks":%d}`, len(params.Input.InitialInput.Content)))
	}
	f.storeSessionLocked(session)
	response := cloneSession(session)
	if f.createState != "" {
		response.Status.State = f.createState
	}
	if f.createConfigMismatch {
		response.Spec.Config = &InitialSessionConfig{Model: "unrequested-model", Effort: "low"}
	}
	drop := f.dropCreateReply
	f.mu.Unlock()
	if drop {
		return true
	}
	f.writeResult(conn, request.ID, response)
	return false
}

func (f *fakeMASSServer) handleResume(conn *websocket.Conn, request fakeRPCRequest) bool {
	var params struct {
		Session SessionRef `json:"session"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil {
		f.writeInvalidParams(conn, request.ID)
		return false
	}
	f.mu.Lock()
	f.resumeCount++
	session, found := f.findSessionLocked(params.Session)
	if !found {
		f.mu.Unlock()
		f.writeRPCError(conn, request.ID, OperationError{
			Code: "not_found", Reason: "resource_not_found", Message: "Resource not found",
		})
		return false
	}
	session.Status.State = "idle"
	if f.resumeState != "" {
		session.Status.State = f.resumeState
	}
	f.sessions[session.Status.SessionID] = session
	drop := f.dropResumeReply
	f.mu.Unlock()
	if drop {
		return true
	}
	f.writeResult(conn, request.ID, session)
	return false
}

func (f *fakeMASSServer) handleClose(conn *websocket.Conn, request fakeRPCRequest) bool {
	var params struct {
		Session SessionRef `json:"session"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil {
		f.writeInvalidParams(conn, request.ID)
		return false
	}
	f.mu.Lock()
	f.closeCount++
	f.closePayloads = append(f.closePayloads, append(json.RawMessage(nil), request.Params...))
	session, found := f.findSessionLocked(params.Session)
	if !found {
		f.mu.Unlock()
		f.writeRPCError(conn, request.ID, OperationError{
			Code: "not_found", Reason: "resource_not_found", Message: "Resource not found",
		})
		return false
	}
	session.Status.State = "offline"
	f.sessions[session.Status.SessionID] = session
	drop := f.dropCloseReply
	f.mu.Unlock()
	if drop {
		return true
	}
	f.writeResult(conn, request.ID, session)
	return false
}

func (f *fakeMASSServer) handlePrompt(conn *websocket.Conn, request fakeRPCRequest) bool {
	var params struct {
		Session SessionRef `json:"session"`
		Input   struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil {
		f.writeInvalidParams(conn, request.ID)
		return false
	}
	f.mu.Lock()
	f.promptCount++
	f.promptPayloads = append(f.promptPayloads, append(json.RawMessage(nil), request.Params...))
	session, found := f.findSessionLocked(params.Session)
	dropReply := f.dropPromptReply
	promptErr := f.promptError
	turn := f.promptTurn
	f.mu.Unlock()
	if !found {
		f.writeRPCError(conn, request.ID, OperationError{
			Code: "not_found", Reason: "resource_not_found", Message: "Resource not found",
		})
		return false
	}
	if promptErr != nil {
		f.writeRPCError(conn, request.ID, *promptErr)
		return false
	}
	if dropReply {
		return true
	}
	f.writeResult(conn, request.ID, map[string]any{
		"submission": "submission-test",
		"acceptance": map[string]bool{"sent": true},
	})
	f.mu.Lock()
	gate := f.promptGates[session.Status.SessionID]
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	f.emitTurn(session.Status.SessionID, params.Input.Content, turn)
	return false
}

func (f *fakeMASSServer) handleSubscribe(conn *websocket.Conn, request fakeRPCRequest) bool {
	var params struct {
		Session SessionRef `json:"session"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil {
		f.writeInvalidParams(conn, request.ID)
		return false
	}
	f.mu.Lock()
	session, found := f.findSessionLocked(params.Session)
	if !found {
		f.mu.Unlock()
		f.writeRPCError(conn, request.ID, OperationError{
			Code: "not_found", Reason: "resource_not_found", Message: "Resource not found",
		})
		return false
	}
	f.subscribeCount++
	f.nextSubscription++
	subscriptionID := fmt.Sprintf("subscription-%d", f.nextSubscription)
	f.subscriptions[subscriptionID] = fakeSubscription{conn: conn, sessionID: session.Status.SessionID}
	f.mu.Unlock()
	f.writeResult(conn, request.ID, map[string]string{"subscriptionId": subscriptionID})
	return false
}

func (f *fakeMASSServer) handleUnsubscribe(conn *websocket.Conn, request fakeRPCRequest) bool {
	var params struct {
		SubscriptionID string `json:"subscriptionId"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil || params.SubscriptionID == "" {
		f.writeInvalidParams(conn, request.ID)
		return false
	}
	f.mu.Lock()
	delete(f.subscriptions, params.SubscriptionID)
	f.mu.Unlock()
	f.write(conn, map[string]any{
		"jsonrpc": "2.0",
		"method":  "subscription.closed",
		"params":  map[string]any{"subscriptionId": params.SubscriptionID},
	})
	f.writeResult(conn, request.ID, nil)
	return false
}

func (f *fakeMASSServer) handleHistory(conn *websocket.Conn, request fakeRPCRequest) bool {
	var params struct {
		Session SessionRef `json:"session"`
		Query   *struct {
			Types []string `json:"types"`
			After string   `json:"after"`
			Limit int      `json:"limit"`
		} `json:"query"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil {
		f.writeInvalidParams(conn, request.ID)
		return false
	}
	f.mu.Lock()
	f.historyCount++
	session, found := f.findSessionLocked(params.Session)
	if !found {
		f.mu.Unlock()
		f.writeRPCError(conn, request.ID, OperationError{
			Code: "not_found", Reason: "resource_not_found", Message: "Resource not found",
		})
		return false
	}
	all := append([]SessionEvent(nil), f.history[session.Status.SessionID]...)
	f.mu.Unlock()

	var filtered []SessionEvent
	if params.Query == nil || len(params.Query.Types) == 0 {
		filtered = all
	} else {
		allowed := map[string]struct{}{}
		for _, value := range params.Query.Types {
			allowed[value] = struct{}{}
		}
		for _, event := range all {
			if _, ok := allowed[event.Type]; ok {
				filtered = append(filtered, event)
			}
		}
	}
	start := 0
	if params.Query != nil && params.Query.After != "" {
		parsed, err := strconv.Atoi(params.Query.After)
		if err != nil || parsed < 0 || parsed > len(filtered) {
			f.writeInvalidParams(conn, request.ID)
			return false
		}
		start = parsed
	}
	limit := 100
	if params.Query != nil && params.Query.Limit > 0 {
		limit = params.Query.Limit
	}
	end := start + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	page := HistoryPage{Events: append([]SessionEvent(nil), filtered[start:end]...)}
	if end < len(filtered) {
		page.Next = strconv.Itoa(end)
	}
	if len(all) > 0 {
		synced := all[len(all)-1].EventID
		page.SyncedEventID = &synced
	}
	f.writeResult(conn, request.ID, page)
	return false
}

func (f *fakeMASSServer) emitTurn(sessionID string, content []struct {
	Type string `json:"type"`
	Text string `json:"text"`
}, turn fakePromptTurn) {
	prompt := ""
	if len(content) > 0 {
		prompt = content[0].Text
	}
	f.mu.Lock()
	session := f.sessions[sessionID]
	session.Status.State = "running"
	f.sessions[sessionID] = session
	f.mu.Unlock()

	userEvent := f.nextSessionEvent(sessionID, "user.message", "item-user", map[string]any{
		"messageId": "item-user",
		"content":   []map[string]string{{"type": "text", "text": prompt}},
	})
	f.appendHistory(sessionID, userEvent, true)
	f.broadcast(sessionID, userEvent)
	f.broadcast(sessionID, f.nextSessionEvent(sessionID, "session.state_update", "", map[string]any{
		"previousState": "idle",
		"state":         "running",
	}))
	if turn.EmitChunks {
		chunk := f.nextSessionEvent(sessionID, "agent.message_chunk", "item-chunk", map[string]any{
			"messageId": "item-chunk",
			"content":   []map[string]string{{"type": "text", "text": "partial "}},
		})
		f.broadcast(sessionID, chunk)
	}
	if turn.EmitPartial {
		text := turn.PartialText
		if text == "" {
			text = "partial answer"
		}
		partial := f.nextSessionEvent(sessionID, "agent.message", "item-partial", map[string]any{
			"messageId": "item-partial",
			"content":   []map[string]string{{"type": "text", "text": text}},
			"phase":     "partial_answer",
		})
		f.appendHistory(sessionID, partial, true)
		f.broadcast(sessionID, partial)
	}
	if turn.EmitFinal {
		phase := turn.Phase
		payload := map[string]any{
			"messageId": "item-final",
			"content":   []map[string]string{{"type": "text", "text": turn.FinalText}},
		}
		if phase != "" {
			payload["phase"] = phase
		}
		final := f.nextSessionEvent(sessionID, "agent.message", "item-final", payload)
		final.Meta = map[string]any{"turnId": "turn-1"}
		if turn.EmitHistoryFinal {
			f.appendHistory(sessionID, final, true)
		}
		f.broadcast(sessionID, final)
	}
	if !turn.EmitFinal && turn.EmitHistoryFinal {
		phase := turn.Phase
		payload := map[string]any{
			"messageId": "item-final-history",
			"content":   []map[string]string{{"type": "text", "text": turn.FinalText}},
		}
		if phase != "" {
			payload["phase"] = phase
		}
		f.appendHistory(sessionID, f.nextSessionEvent(sessionID, "agent.message", "item-final-history", payload), false)
	}
	f.mu.Lock()
	session = f.sessions[sessionID]
	session.Status.State = "idle"
	f.sessions[sessionID] = session
	f.mu.Unlock()
	f.broadcast(sessionID, f.nextSessionEvent(sessionID, "session.state_update", "", map[string]any{
		"previousState": "running",
		"state":         "idle",
	}))
}

func (f *fakeMASSServer) nextSessionEvent(sessionID, eventType, itemID string, payload any) SessionEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextEvent++
	raw, _ := json.Marshal(payload)
	session := f.sessions[sessionID]
	return SessionEvent{
		EventID:     fmt.Sprintf("event-%d", f.nextEvent),
		Namespace:   session.Metadata.Namespace,
		SessionName: session.Metadata.Name,
		SessionID:   sessionID,
		ItemID:      itemID,
		Type:        eventType,
		Time:        time.Now().UTC(),
		Payload:     raw,
	}
}

func (f *fakeMASSServer) appendHistory(sessionID string, event SessionEvent, _ bool) {
	if event.Replay != nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.history[sessionID] = append(f.history[sessionID], cloneSessionEvent(event))
}

func (f *fakeMASSServer) broadcast(sessionID string, event SessionEvent) {
	f.mu.Lock()
	var targets []fakeSubscription
	for _, subscription := range f.subscriptions {
		if subscription.sessionID == sessionID {
			targets = append(targets, subscription)
		}
	}
	f.mu.Unlock()
	for _, target := range targets {
		f.write(target.conn, map[string]any{
			"jsonrpc": "2.0",
			"method":  "session.event",
			"params": map[string]any{
				"subscriptionId": subscriptionIDFor(f, target),
				"event":          cloneSessionEvent(event),
			},
		})
	}
}

func subscriptionIDFor(f *fakeMASSServer, target fakeSubscription) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, candidate := range f.subscriptions {
		if candidate.conn == target.conn && candidate.sessionID == target.sessionID {
			return id
		}
	}
	return ""
}

func cloneSessionEvent(event SessionEvent) SessionEvent {
	clone := event
	if event.Payload != nil {
		clone.Payload = append(json.RawMessage(nil), event.Payload...)
	}
	if event.Meta != nil {
		clone.Meta = map[string]any{}
		for key, value := range event.Meta {
			clone.Meta[key] = value
		}
	}
	return clone
}

func (f *fakeMASSServer) findSessionLocked(ref SessionRef) (Session, bool) {
	var id string
	switch {
	case ref.SessionID != "":
		id = ref.SessionID
	case ref.Namespace != "" && ref.Name != "":
		id = f.sessionIDs[ref.Namespace+"/"+ref.Name]
	}
	session, found := f.sessions[id]
	return cloneSession(session), found
}

func (f *fakeMASSServer) writeResult(conn *websocket.Conn, id json.RawMessage, result any) {
	f.write(conn, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
}

func (f *fakeMASSServer) writeInvalidParams(conn *websocket.Conn, id json.RawMessage) {
	f.write(conn, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    -32602,
			"message": "Invalid params",
		},
	})
}

func (f *fakeMASSServer) writeRPCError(conn *websocket.Conn, id json.RawMessage, value OperationError) {
	f.write(conn, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    -32000,
			"message": value.Message,
			"data":    value,
		},
	})
}

func (f *fakeMASSServer) writeProtocolError(conn *websocket.Conn, id json.RawMessage, reason string) {
	f.write(conn, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    -32001,
			"message": "Protocol state error",
			"data":    map[string]string{"reason": reason},
		},
	})
}

func (f *fakeMASSServer) write(conn *websocket.Conn, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		return
	}
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
	_ = conn.WriteMessage(websocket.TextMessage, payload)
}
