package sa

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RuntimeRead accepts only public display fields. Dynamic runtime configuration,
// credentials, command arguments, headers and MCP definitions are never decoded.
type RuntimeRead struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		Type   string `json:"type"`
		Config struct {
			Model  string `json:"model"`
			Effort string `json:"effort"`
		} `json:"config"`
	} `json:"spec"`
	Status struct {
		State string `json:"state"`
	} `json:"status"`
}

func (c *MASSClient) ListRuntimes(ctx context.Context, kind, state string) ([]RuntimeRead, error) {
	var result []RuntimeRead
	err := c.call(ctx, "runtime.list", map[string]any{"query": map[string]string{"type": kind, "state": state}}, &result, false)
	return result, err
}
func (c *MASSClient) GetRuntime(ctx context.Context, name string) (RuntimeRead, error) {
	var result RuntimeRead
	err := c.call(ctx, "runtime.get", map[string]string{"runtimeRef": name}, &result, false)
	return result, err
}

type ObserverAssociation struct {
	ProjectID    string                `json:"projectId"`
	Role         string                `json:"role"`
	TicketID     string                `json:"ticketId,omitempty"`
	AssignmentID string                `json:"assignmentId,omitempty"`
	Current      bool                  `json:"current"`
	Requested    *InitialSessionConfig `json:"requested,omitempty"`
}
type ObserverResourceSession struct {
	ObserverSession
	Associations          []ObserverAssociation `json:"associations"`
	ProviderModel         string                `json:"providerModel,omitempty"`
	ProviderModelEvidence string                `json:"providerModelEvidence"`
}
type ObserverRuntime struct {
	Name         string               `json:"name"`
	Namespace    string               `json:"namespace"`
	Type         string               `json:"type"`
	State        string               `json:"state"`
	Config       InitialSessionConfig `json:"config"`
	ConfigSource string               `json:"configSource"`
}

func validateObserverQuery(resource string, q url.Values) error {
	allowed := map[string]bool{"project": true, "limit": true, "cursor": true}
	if resource == "triggers" {
		for _, key := range []string{"source", "thread", "decision", "route", "from", "to", "search"} {
			allowed[key] = true
		}
	}
	if strings.HasPrefix(resource, "triggers/") {
		allowed = map[string]bool{"project": true}
	}
	if resource == "outputs" {
		for _, key := range []string{"trigger", "ticket", "source", "thread", "category", "relatedTrigger"} {
			allowed[key] = true
		}
	}
	if strings.HasPrefix(resource, "outputs/") {
		allowed = map[string]bool{"project": true}
	}
	if resource == "tickets" {
		for _, key := range []string{"state", "parent", "root", "search"} {
			allowed[key] = true
		}
	}
	if strings.HasPrefix(resource, "tickets/") {
		allowed = map[string]bool{"project": true}
	}
	if resource == "runtimes" || strings.HasPrefix(resource, "runtimes/") || resource == "sessions" || strings.HasPrefix(resource, "sessions/") {
		allowed["scope"] = true
		if resource == "sessions" {
			for _, key := range []string{"namespace", "runtime", "state"} {
				allowed[key] = true
			}
		}
		if resource == "runtimes" {
			for _, key := range []string{"namespace", "type", "state"} {
				allowed[key] = true
			}
		}
		if strings.HasSuffix(resource, "/history") || strings.HasSuffix(resource, "/events") {
			allowed["eventTypes"] = true
			allowed["window"] = true
		}
	}
	if window := q.Get("window"); window != "" && window != "recent" && window != "page" {
		return operationError("invalid_argument", "invalid_history_window", "window must be recent or page")
	}
	for k, v := range q {
		if !allowed[k] || len(v) != 1 {
			return operationError("invalid_argument", "invalid_observer_query", "unknown or repeated observation query parameter")
		}
	}
	return nil
}

// Wrapping preserves the native cursor while binding local-only scope/project
// filters that massd does not know about. No database position is manufactured.
type observerNativeCursor struct {
	Native string   `json:"native"`
	Filter [32]byte `json:"filter"`
}

func sessionCursorFilter(q url.Values) [32]byte {
	scope := q.Get("scope")
	if scope == "" {
		scope = "associated"
	}
	return sha256.Sum256([]byte(strings.Join([]string{q.Get("project"), scope, q.Get("namespace"), q.Get("runtime"), q.Get("state")}, "\x00")))
}
func unwrapSessionCursor(q url.Values) (string, error) {
	if q.Get("cursor") == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(q.Get("cursor"))
	var c observerNativeCursor
	if err != nil || json.Unmarshal(raw, &c) != nil || c.Native == "" || c.Filter != sessionCursorFilter(q) {
		return "", operationError("invalid_argument", "invalid_observer_cursor", "cursor does not match this resource scope and filter")
	}
	return c.Native, nil
}
func wrapSessionCursor(native string, q url.Values) string {
	if native == "" {
		return ""
	}
	raw, _ := json.Marshal(observerNativeCursor{Native: native, Filter: sessionCursorFilter(q)})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func resourceLimit(q url.Values) (int, error) {
	limit := 50
	if q.Get("limit") != "" {
		v, e := strconv.Atoi(q.Get("limit"))
		if e != nil || v < 1 || v > 200 {
			return 0, operationError("invalid_argument", "invalid_observer_limit", "limit must be between 1 and 200")
		}
		limit = v
	}
	return limit, nil
}
func (o *Observer) resourceContext(q url.Values) (string, error) {
	scope := q.Get("scope")
	if scope == "" {
		scope = "associated"
	}
	if scope != "associated" && scope != "all" {
		return "", operationError("invalid_argument", "invalid_observer_scope", "scope must be associated or all")
	}
	if id := q.Get("project"); id != "" {
		found := false
		for _, p := range o.config.Projects {
			if p.ID == id {
				found = true
			}
		}
		if !found {
			return "", operationError("not_found", "project_not_found", "configured Project was not found")
		}
	}
	return scope, nil
}

// Durable session IDs are the only join key. Namespace/name are never ownership
// evidence. Assignments and pinned deliveries retain old executions and routers.
func (o *Observer) resourceIndex(snap State, project string) (map[string][]ObserverAssociation, map[string]bool) {
	sessions := map[string][]ObserverAssociation{}
	runtimes := map[string]bool{}
	add := func(id string, a ObserverAssociation) {
		if id == "" {
			return
		}
		for _, old := range sessions[id] {
			if old.ProjectID == a.ProjectID && old.Role == a.Role && old.AssignmentID == a.AssignmentID && old.TicketID == a.TicketID {
				return
			}
		}
		sessions[id] = append(sessions[id], a)
	}
	for _, p := range o.config.Projects {
		if project != "" && project != p.ID {
			continue
		}
		runtimes[p.RuntimeRef] = true
		ps := snap.Projects[p.ID]
		if ps.Router != nil {
			runtimes[ps.Router.RuntimeRef] = true
			add(ps.Router.SessionID, ObserverAssociation{ProjectID: p.ID, Role: "router", Current: true, Requested: &InitialSessionConfig{Model: visibleText(p.Router.Model), Effort: visibleText(p.Router.Effort)}})
		}
		for _, a := range ps.Assignments {
			runtimes[a.RuntimeRef] = true
			t := ps.Tickets[a.TicketID]
			add(a.Session.SessionID, ObserverAssociation{ProjectID: p.ID, Role: "assignment", TicketID: a.TicketID, AssignmentID: a.ID, Current: t.AssignmentID == a.ID, Requested: &InitialSessionConfig{Model: visibleText(a.Model), Effort: visibleText(a.Effort)}})
		}
		for _, w := range ps.Workers {
			runtimes[w.RuntimeRef] = true
			add(w.Session.SessionID, ObserverAssociation{ProjectID: p.ID, Role: "worker_reference", TicketID: w.TicketID, AssignmentID: w.AssignmentID, Current: ps.Tickets[w.TicketID].AssignmentID == w.AssignmentID})
		}
		for _, b := range ps.Deliveries {
			role := "delivery"
			if b.RoutingPath == "router" {
				role = "router"
			}
			add(b.TargetSession.SessionID, ObserverAssociation{ProjectID: p.ID, Role: role, AssignmentID: b.TargetAssignmentID})
		}
		for _, b := range ps.Inbox {
			role := "inbox"
			if b.RoutingPath == "router" {
				role = "router"
			}
			add(b.TargetSession.SessionID, ObserverAssociation{ProjectID: p.ID, Role: role, TicketID: b.TicketID, AssignmentID: b.TargetAssignmentID})
		}
	}
	for id := range sessions {
		sort.Slice(sessions[id], func(i, j int) bool {
			a, b := sessions[id][i], sessions[id][j]
			return a.ProjectID+"\x00"+a.Role+"\x00"+a.AssignmentID+a.TicketID < b.ProjectID+"\x00"+b.Role+"\x00"+b.AssignmentID+b.TicketID
		})
	}
	return sessions, runtimes
}
func projectResourceSession(s Session, associations []ObserverAssociation) ObserverResourceSession {
	if associations == nil {
		associations = []ObserverAssociation{}
	}
	model, effort := sessionConfig(s)
	return ObserverResourceSession{ObserverSession: ObserverSession{SessionID: s.Status.SessionID, Namespace: visibleText(s.Metadata.Namespace), Name: visibleText(s.Metadata.Name), RuntimeRef: visibleText(s.Spec.RuntimeRef), State: visibleText(s.Status.State), Config: &InitialSessionConfig{Model: visibleText(model), Effort: visibleText(effort)}}, Associations: associations, ProviderModelEvidence: "未观测：初始配置和请求值均不证明 provider 实际模型"}
}
func projectRuntime(r RuntimeRead) ObserverRuntime {
	return ObserverRuntime{Name: visibleText(r.Metadata.Name), Namespace: visibleText(r.Metadata.Namespace), Type: visibleText(r.Spec.Type), State: visibleText(r.Status.State), Config: InitialSessionConfig{Model: visibleText(r.Spec.Config.Model), Effort: visibleText(r.Spec.Config.Effort)}, ConfigSource: "massd_persisted_runtime_config_allowlist"}
}
func (o *Observer) resourceQuery(ctx context.Context, snapshot State, resource string, q url.Values) (any, error) {
	scope, err := o.resourceContext(q)
	if err != nil {
		return nil, err
	}
	limit, err := resourceLimit(q)
	if err != nil {
		return nil, err
	}
	if resource == "runtimes" && q.Get("cursor") != "" {
		return o.runtimePage(nil, q, limit)
	}
	indexProject := q.Get("project")
	if scope == "all" {
		indexProject = ""
	}
	index, runtimeIndex := o.resourceIndex(snapshot, indexProject)
	client, err := OpenMASS(ctx, o.config.MASS)
	if err != nil {
		return nil, operationError("unavailable", "mass_observation_unavailable", "massd read unavailable; retain the last successful observation")
	}
	defer client.Close()
	if resource == "sessions" {
		nativeCursor, err := unwrapSessionCursor(q)
		if err != nil {
			return nil, err
		}
		page, err := client.ListSessions(ctx, SessionQuery{Namespace: q.Get("namespace"), RuntimeRef: q.Get("runtime"), State: q.Get("state"), After: nativeCursor, Limit: limit})
		if err != nil {
			return nil, safeResourceError(err)
		}
		result := ObserverPage[ObserverResourceSession]{Items: []ObserverResourceSession{}, Next: wrapSessionCursor(page.Next, q), Source: "massd_public_session_list", ObservedAt: time.Now().UTC()}
		for _, s := range page.Items {
			if scope == "all" || len(index[s.Status.SessionID]) > 0 {
				result.Items = append(result.Items, projectResourceSession(s, index[s.Status.SessionID]))
			}
		}
		return result, nil
	}
	if strings.HasPrefix(resource, "sessions/") {
		id := strings.TrimPrefix(resource, "sessions/")
		kind := ""
		for _, suffix := range []string{"/history", "/events"} {
			if strings.HasSuffix(id, suffix) {
				id = strings.TrimSuffix(id, suffix)
				kind = suffix
				break
			}
		}
		id, err = url.PathUnescape(id)
		if err != nil {
			return nil, operationError("invalid_argument", "invalid_session_id", "invalid encoded Session ID")
		}
		if id == "" {
			return nil, operationError("invalid_argument", "invalid_session_id", "Session ID is required")
		}
		if scope == "associated" && len(index[id]) == 0 {
			return nil, operationError("not_found", "session_not_associated", "Session has no durable association in this context; use scope=all")
		}
		ref := SessionRef{SessionID: id}
		if kind == "/history" {
			return readObserverHistory(ctx, client, ref, q)
		}
		if kind == "/events" {
			return nil, operationError("invalid_argument", "stream_requires_browser", "use the same-origin events SSE route for live observation")
		}
		s, err := client.GetSession(ctx, ref)
		if err != nil {
			return nil, safeResourceError(err)
		}
		if s.Status.SessionID != id {
			return nil, operationError("unavailable", "session_identity_mismatch", "massd returned a different Session identity")
		}
		return projectResourceSession(s, index[id]), nil
	}
	// Historical Router/delivery references may retain only a Session ID, with
	// no Runtime field. Resolve that durable identity before filtering Runtimes.
	if scope == "associated" {
		ids := make([]string, 0, len(index))
		for id := range index {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			session, readErr := client.GetSession(ctx, SessionRef{SessionID: id})
			if IsErrorCode(readErr, "not_found") {
				continue
			}
			if readErr != nil {
				return nil, safeResourceError(readErr)
			}
			if session.Status.SessionID != id {
				return nil, operationError("unavailable", "session_identity_mismatch", "massd returned a different Session identity")
			}
			runtimeIndex[session.Spec.RuntimeRef] = true
		}
	}
	if resource == "runtimes" {
		rows, err := client.ListRuntimes(ctx, q.Get("type"), q.Get("state"))
		if err != nil {
			return nil, safeResourceError(err)
		}
		items := []ObserverRuntime{}
		for _, r := range rows {
			if (scope == "all" || runtimeIndex[r.Metadata.Name]) && (q.Get("namespace") == "" || q.Get("namespace") == r.Metadata.Namespace) {
				items = append(items, projectRuntime(r))
			}
		}
		return o.runtimePage(items, q, limit)
	}
	name, err := url.PathUnescape(strings.TrimPrefix(resource, "runtimes/"))
	if err != nil {
		return nil, operationError("invalid_argument", "invalid_runtime_name", "invalid encoded Runtime name")
	}
	r, err := client.GetRuntime(ctx, name)
	if err != nil {
		return nil, safeResourceError(err)
	}
	if scope == "associated" && !runtimeIndex[r.Metadata.Name] {
		return nil, operationError("not_found", "runtime_not_associated", "Runtime has no durable association in this context; use scope=all")
	}
	return projectRuntime(r), nil
}

type observerRuntimeTraversal struct {
	Rows        []ObserverRuntime
	Fingerprint [32]byte
	At          time.Time
}

// runtime.list is unpaginated. Observer freezes its filtered result locally;
// these cursors never enter a MASS RPC or claim to be native positions.
func (o *Observer) runtimePage(rows []ObserverRuntime, q url.Values, limit int) (ObserverPage[ObserverRuntime], error) {
	scope := q.Get("scope")
	if scope == "" {
		scope = "associated"
	}
	fingerprint := sha256.Sum256([]byte(strings.Join([]string{q.Get("project"), scope, q.Get("namespace"), q.Get("type"), q.Get("state")}, "\x00")))
	var cursor struct {
		ID    string `json:"id"`
		After string `json:"after"`
	}
	now := time.Now().UTC()
	traversal := observerRuntimeTraversal{Rows: rows, Fingerprint: fingerprint, At: now}
	start := 0
	if raw := q.Get("cursor"); raw != "" {
		data, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.ID == "" {
			return ObserverPage[ObserverRuntime]{}, operationError("invalid_argument", "invalid_observer_cursor", "invalid Runtime traversal cursor")
		}
		o.mu.Lock()
		traversal = o.runtimePages[cursor.ID]
		o.mu.Unlock()
		if traversal.At.IsZero() || now.Sub(traversal.At) > 15*time.Minute || traversal.Fingerprint != fingerprint {
			return ObserverPage[ObserverRuntime]{}, operationError("invalid_argument", "invalid_observer_cursor", "Runtime traversal expired or does not match these filters")
		}
		start = sort.Search(len(traversal.Rows), func(i int) bool { return traversal.Rows[i].Name >= cursor.After })
		if start == len(traversal.Rows) || traversal.Rows[start].Name != cursor.After {
			return ObserverPage[ObserverRuntime]{}, operationError("invalid_argument", "invalid_observer_cursor", "Runtime traversal boundary was not found")
		}
		start++
	} else {
		sort.Slice(traversal.Rows, func(i, j int) bool { return traversal.Rows[i].Name < traversal.Rows[j].Name })
	}
	end := min(start+limit, len(traversal.Rows))
	next := ""
	if end < len(traversal.Rows) {
		if cursor.ID == "" {
			var id [18]byte
			if _, err := rand.Read(id[:]); err != nil {
				return ObserverPage[ObserverRuntime]{}, operationError("unavailable", "observer_cursor_unavailable", "cannot start Runtime traversal")
			}
			cursor.ID = base64.RawURLEncoding.EncodeToString(id[:])
			o.mu.Lock()
			if o.runtimePages == nil {
				o.runtimePages = map[string]observerRuntimeTraversal{}
			}
			for key, page := range o.runtimePages {
				if now.Sub(page.At) > 15*time.Minute {
					delete(o.runtimePages, key)
				}
			}
			if len(o.runtimePages) >= 64 {
				oldest := ""
				var at time.Time
				for key, page := range o.runtimePages {
					if oldest == "" || page.At.Before(at) {
						oldest, at = key, page.At
					}
				}
				delete(o.runtimePages, oldest)
			}
			o.runtimePages[cursor.ID] = traversal
			o.mu.Unlock()
		}
		cursor.After = traversal.Rows[end-1].Name
		data, _ := json.Marshal(cursor)
		next = base64.RawURLEncoding.EncodeToString(data)
	}
	return ObserverPage[ObserverRuntime]{Items: traversal.Rows[start:end], Next: next, ObservedAt: traversal.At, Source: "massd_public_runtime_list_observer_snapshot"}, nil
}
func safeResourceError(err error) error {
	if IsErrorCode(err, "not_found") {
		return operationError("not_found", "mass_resource_not_found", "massd resource is unavailable or was removed")
	}
	if IsErrorCode(err, "invalid_argument") {
		return operationError("invalid_argument", "mass_invalid_query", "massd rejected the filter or native cursor")
	}
	return operationError("unavailable", "mass_observation_unavailable", "massd read unavailable; retain the last successful observation")
}

// ObserverEvent strips _meta and all arbitrary configuration subtrees. Payload
// fields are selected by semantic key and displayed only as inert text.
type ObserverEvent struct {
	EventID   string         `json:"eventId"`
	SessionID string         `json:"sessionId"`
	ItemID    string         `json:"itemId,omitempty"`
	Type      string         `json:"type"`
	Time      time.Time      `json:"time"`
	Text      string         `json:"text"`
	Payload   map[string]any `json:"payload"`
	Preview   bool           `json:"preview"`
}

func observerEvent(e SessionEvent, preview bool) ObserverEvent {
	payload := map[string]json.RawMessage{}
	_ = json.Unmarshal(e.Payload, &payload)
	out := map[string]any{}
	for _, k := range []string{"messageId", "id", "toolName", "provider", "kind", "status", "message", "reason", "state", "previousState", "stopReason", "phase", "title"} {
		var v string
		if json.Unmarshal(payload[k], &v) == nil {
			out[k] = visibleText(v)
		}
	}
	var content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(payload["content"], &content)
	texts := []string{}
	for _, b := range content {
		if b.Type == "text" {
			texts = append(texts, visibleText(b.Text))
		} else {
			texts = append(texts, "["+visibleText(b.Type)+" 内容；未加载外部资源]")
		}
	}
	// Raw tool values are rendered as sanitized JSON text, never executable HTML.
	for _, k := range []string{"rawInput", "rawOutput"} {
		if raw := payload[k]; len(raw) > 0 {
			var v any
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber()
			if decoder.Decode(&v) == nil {
				clean := sanitizeToolValue(v, 0)
				data, _ := json.Marshal(clean)
				if len(data) > 32768 {
					out[k] = visibleText(string(data[:32768])) + " [display truncated]"
				} else {
					out[k] = string(data)
				}
			}
		}
	}
	if v, ok := out["message"].(string); ok {
		texts = append(texts, v)
	}
	return ObserverEvent{EventID: e.EventID, SessionID: e.SessionID, ItemID: e.ItemID, Type: e.Type, Time: e.Time, Text: boundedObserverText(strings.Join(texts, "\n")), Payload: out, Preview: preview}
}
func sanitizeToolValue(v any, depth int) any {
	if depth > 12 {
		return "[depth limit]"
	}
	switch x := v.(type) {
	case map[string]any:
		r := map[string]any{}
		for k, val := range x {
			key := strings.ToLower(k)
			if strings.Contains(key, "token") || strings.Contains(key, "secret") || strings.Contains(key, "password") || strings.Contains(key, "authorization") || strings.Contains(key, "api_key") || strings.Contains(key, "apikey") || strings.Contains(key, "header") || strings.Contains(key, "env") || strings.Contains(key, "mcp") || strings.Contains(key, "capability") {
				r[k] = "[redacted]"
			} else {
				r[k] = sanitizeToolValue(val, depth+1)
			}
		}
		return r
	case []any:
		r := make([]any, len(x))
		for i, val := range x {
			r[i] = sanitizeToolValue(val, depth+1)
		}
		return r
	case string:
		return visibleText(x)
	default:
		return v
	}
}

type ObserverHistory struct {
	Events         []ObserverEvent `json:"events"`
	Next           string          `json:"next,omitempty"`
	Cursor         string          `json:"cursor"`
	EarlierCursors []string        `json:"earlierCursors"`
	SyncedEventID  *string         `json:"syncedEventId"`
	ObservedAt     time.Time       `json:"observedAt"`
	Source         string          `json:"source"`
}

func historyTypes(q url.Values) ([]string, error) {
	if q.Get("eventTypes") == "" {
		return nil, nil
	}
	types := strings.Split(q.Get("eventTypes"), ",")
	valid := map[string]bool{"user.message": true, "agent.message": true, "agent.thinking": true, "agent.tool_call": true, "agent.tool_result": true}
	for _, v := range types {
		if !valid[v] {
			return nil, operationError("invalid_argument", "invalid_event_type", "history supports the five durable SessionEvent types")
		}
	}
	return types, nil
}
func readObserverHistory(ctx context.Context, c *MASSClient, ref SessionRef, q url.Values) (ObserverHistory, error) {
	types, err := historyTypes(q)
	if err != nil {
		return ObserverHistory{}, err
	}
	limit, err := resourceLimit(q)
	if err != nil {
		return ObserverHistory{}, err
	}
	window := q.Get("window")
	if window != "" && window != "recent" && window != "page" {
		return ObserverHistory{}, operationError("invalid_argument", "invalid_history_window", "window must be recent or page")
	}
	cursor := q.Get("cursor")
	earlier := []string{}
	recent := (window == "recent" || window == "") && cursor == ""
	for pages := 0; pages < 1024; pages++ {
		page, err := c.History(ctx, ref, HistoryQuery{Types: types, After: cursor, Limit: limit})
		if err != nil {
			return ObserverHistory{}, safeResourceError(err)
		}
		if recent && page.Next != "" {
			earlier = append(earlier, cursor)
			cursor = page.Next
			continue
		}
		events := make([]ObserverEvent, 0, len(page.Events))
		for _, e := range page.Events {
			if e.SessionID != ref.SessionID {
				return ObserverHistory{}, operationError("unavailable", "session_identity_mismatch", "history contained a different Session identity")
			}
			events = append(events, observerEvent(e, false))
		}
		return ObserverHistory{Events: events, Next: page.Next, Cursor: cursor, EarlierCursors: earlier, SyncedEventID: page.SyncedEventID, ObservedAt: time.Now().UTC(), Source: "massd_durable_append_order"}, nil
	}
	return ObserverHistory{}, operationError("resource_exhausted", "history_scan_limit", "recent history scan exceeded its bound; use window=page and native cursors")
}

func boundedObserverText(value string) string {
	if len(value) > 65536 {
		return value[:65536] + " [display truncated]"
	}
	return value
}
