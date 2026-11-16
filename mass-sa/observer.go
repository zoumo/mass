package sa

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Observer owns only in-memory observations. It never updates coordination state
// and never calls a MASS mutation or an output consumer operation.
type Observer struct {
	config         Config
	store          *StateStore
	stateFile      string // set only by observer-only serve; never reload the active store
	mu             sync.Mutex
	last           map[string]RouterObservation
	triggerPages   map[string]observerTriggerTraversal
	outputPages    map[string]observerOutputTraversal
	ticketPages    map[string]observerTicketTraversal
	runtimePages   map[string]observerRuntimeTraversal
	routerPages    map[string]observerRouterTraversal
	ticketSessions map[string]RouterObservation
}

func newObserver(config Config, store *StateStore) *Observer {
	return &Observer{config: config, store: store, last: map[string]RouterObservation{}}
}

func (o *Observer) snapshot() (State, error) {
	if o.stateFile != "" {
		return readStateSnapshot(o.stateFile)
	}
	return o.store.Snapshot(), nil
}

type ObserverProject struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ObserverPage[T any] struct {
	Items      []T       `json:"items"`
	Next       string    `json:"next,omitempty"`
	ObservedAt time.Time `json:"observedAt"`
	Source     string    `json:"source"`
}

// ObserverSession is deliberately separate from Session: MCP configuration,
// initial input receipts and arbitrary upstream fields cannot enter the browser.
type ObserverSession struct {
	SessionID  string                `json:"sessionId"`
	Namespace  string                `json:"namespace"`
	Name       string                `json:"name"`
	RuntimeRef string                `json:"runtimeRef"`
	State      string                `json:"state"`
	Config     *InitialSessionConfig `json:"initialConfig,omitempty"`
}

type RouterObservation struct {
	Status        string           `json:"status"`
	AttemptedAt   time.Time        `json:"attemptedAt"`
	LastSuccessAt *time.Time       `json:"lastSuccessAt,omitempty"`
	Stale         bool             `json:"stale"`
	Error         string           `json:"error,omitempty"`
	Session       *ObserverSession `json:"session,omitempty"`
}

type ObserverRouterReference struct {
	SessionRef
	ObservedState string    `json:"observedState"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type ObserverWorkerDefaults struct {
	Model       string   `json:"model"`
	Effort      string   `json:"effort"`
	RuntimeRefs []string `json:"runtimeRefs"`
}

type ObserverBatching struct {
	MergeWindowMS  int `json:"mergeWindowMs"`
	MaxBatchSize   int `json:"maxBatchSize"`
	MaxPromptBytes int `json:"maxPromptBytes"`
}

type ObserverRouter struct {
	Project      ObserverProject          `json:"project"`
	Name         string                   `json:"name"`
	Namespace    string                   `json:"namespace"`
	RuntimeRef   string                   `json:"runtimeRef"`
	Requested    InitialSessionConfig     `json:"requested"`
	CWD          string                   `json:"cwd"`
	Instructions string                   `json:"instructions"`
	Worker       ObserverWorkerDefaults   `json:"worker"`
	Inbox        ObserverBatching         `json:"inbox"`
	Persisted    *ObserverRouterReference `json:"persisted,omitempty"`
	Observation  RouterObservation        `json:"observation"`
	ConfigSource string                   `json:"configSource"`
}

// Query is the shared allowlisted read seam for the browser and authenticated
// CLI. Add future resource readers here; do not dispatch arbitrary RPC names.
func (o *Observer) Query(ctx context.Context, resource string, query url.Values) (any, error) {
	if err := validateObserverQuery(resource, query); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	// These continuations already own frozen rows and need no current state.
	var snapshot State
	frozen := query.Get("cursor") != "" && (resource == "triggers" || resource == "tickets" || resource == "outputs" || resource == "runtimes" || resource == "routers")
	if !frozen {
		var err error
		snapshot, err = o.snapshot()
		if err != nil {
			return nil, err
		}
	}
	if resource == "runtimes" || strings.HasPrefix(resource, "runtimes/") || resource == "sessions" || strings.HasPrefix(resource, "sessions/") {
		return o.resourceQuery(ctx, snapshot, resource, query)
	}
	switch {
	case resource == "outputs":
		return o.outputPage(snapshot, query)
	case strings.HasPrefix(resource, "outputs/") && resource != "outputs/ack":
		return o.outputDetail(snapshot, strings.TrimPrefix(resource, "outputs/"), query.Get("project"))
	case resource == "tickets":
		return o.ticketPage(snapshot, query)
	case strings.HasPrefix(resource, "tickets/") && strings.Count(resource, "/") == 1:
		return o.ticketDetail(ctx, snapshot, strings.TrimPrefix(resource, "tickets/"), query.Get("project"))
	case resource == "triggers":
		return o.triggerPage(snapshot, query)
	case strings.HasPrefix(resource, "triggers/") && strings.Count(resource, "/") == 1:
		return o.triggerDetail(snapshot, strings.TrimPrefix(resource, "triggers/"), query.Get("project"))
	case resource == "health":
		client, err := OpenMASS(ctx, o.config.MASS)
		status := "reachable"
		if err != nil {
			status = "error"
		} else {
			_ = client.Close()
		}
		return map[string]any{
			"observedAt": time.Now().UTC(), "massSa": "reachable", "massd": status,
			"probe": "WebSocket initialize only; does not prove Agent health",
		}, nil
	case resource == "projects":
		projects, next, err := o.projectPage(query)
		if err != nil {
			return nil, err
		}
		items := make([]ObserverProject, 0, len(projects))
		for _, p := range projects {
			items = append(items, ObserverProject{ID: p.ID, Name: visibleText(p.Name)})
		}
		return ObserverPage[ObserverProject]{Items: items, Next: next, ObservedAt: time.Now().UTC(), Source: "loaded_config"}, nil
	case resource == "routers":
		return o.routerPage(ctx, snapshot, query)
	case strings.HasPrefix(resource, "routers/") && strings.Count(resource, "/") == 1:
		id := strings.TrimPrefix(resource, "routers/")
		for _, p := range o.config.Projects {
			if p.ID == id {
				return o.routers(ctx, snapshot, []ProjectConfig{p})[0], nil
			}
		}
	}
	return nil, operationError("not_found", "observation_not_found", "observation resource was not found")
}

// projectPage uses immutable loaded configuration as its traversal snapshot.
// The fingerprint binds a cursor to both its filter and ordered identities.
func (o *Observer) projectPage(query url.Values) ([]ProjectConfig, string, error) {
	limit := 50
	if value := query.Get("limit"); value != "" {
		var err error
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 200 {
			return nil, "", operationError("invalid_argument", "invalid_observer_limit", "limit must be between 1 and 200")
		}
	}
	items := make([]ProjectConfig, 0, len(o.config.Projects))
	for _, p := range o.config.Projects {
		if query.Get("project") == "" || query.Get("project") == p.ID {
			items = append(items, p)
		}
	}
	if query.Get("project") != "" && len(items) == 0 {
		return nil, "", operationError("not_found", "project_not_found", "configured Project was not found")
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	ids := []string{query.Get("project")}
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	fingerprint := sha256.Sum256([]byte(strings.Join(ids, "\x00")))
	type cursor struct {
		Fingerprint [32]byte `json:"f"`
		After       string   `json:"a"`
	}
	start := 0
	if raw := query.Get("cursor"); raw != "" {
		var c cursor
		data, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || json.Unmarshal(data, &c) != nil || c.Fingerprint != fingerprint {
			return nil, "", operationError("invalid_argument", "invalid_observer_cursor", "cursor does not match this traversal")
		}
		start = sort.Search(len(items), func(i int) bool { return items[i].ID >= c.After })
		if start == len(items) || items[start].ID != c.After {
			return nil, "", operationError("invalid_argument", "invalid_observer_cursor", "cursor boundary was not found")
		}
		start++
	}
	end := min(start+limit, len(items))
	next := ""
	if end < len(items) {
		raw, _ := json.Marshal(cursor{Fingerprint: fingerprint, After: items[end-1].ID})
		next = base64.RawURLEncoding.EncodeToString(raw)
	}
	return items[start:end], next, nil
}

func (o *Observer) routers(ctx context.Context, snapshot State, projects []ProjectConfig) []ObserverRouter {
	result := make([]ObserverRouter, 0, len(projects))
	if len(projects) == 0 {
		return result
	}
	client, openErr := OpenMASS(ctx, o.config.MASS)
	if client != nil {
		defer client.Close()
	}
	for _, p := range projects {
		persisted := snapshot.Projects[p.ID].Router
		var reference *ObserverRouterReference
		ref := SessionRef{Namespace: RouterNamespace, Name: RouterName(p)}
		if persisted != nil {
			reference = &ObserverRouterReference{
				SessionRef: persisted.SessionRef, ObservedState: visibleText(persisted.ObservedState), UpdatedAt: persisted.UpdatedAt,
			}
			ref = SessionRef{SessionID: persisted.SessionID}
		}
		attempted := time.Now().UTC()
		var session Session
		err := openErr
		if err == nil {
			session, err = client.GetSession(ctx, ref)
		}
		o.mu.Lock()
		// A changed durable identity cannot inherit the previous identity's cache.
		cacheKey := p.ID + "\x00" + ref.SessionID
		observation := o.last[cacheKey]
		observation.AttemptedAt = attempted
		observation.Stale = observation.Session != nil
		observation.Status = "error"
		observation.Error = "massd observation unavailable; retry to refresh"
		if IsErrorCode(err, "not_found") {
			observation.Status = "not_found"
			observation.Error = "Session not found in massd; no Session was created or resumed"
		}
		if err == nil {
			if session.Metadata.Namespace != RouterNamespace || session.Metadata.Name != RouterName(p) ||
				session.Status.SessionID == "" || (ref.SessionID != "" && session.Status.SessionID != ref.SessionID) {
				observation.Status = "error"
				observation.Error = "massd returned a different Session identity"
			} else {
				model, effort := sessionConfig(session)
				observation = RouterObservation{
					Status: "observed", AttemptedAt: attempted, LastSuccessAt: &attempted,
					Session: &ObserverSession{
						SessionID: session.Status.SessionID, Namespace: session.Metadata.Namespace,
						Name: session.Metadata.Name, RuntimeRef: visibleText(session.Spec.RuntimeRef),
						State:  visibleText(session.Status.State),
						Config: &InitialSessionConfig{Model: visibleText(model), Effort: visibleText(effort)},
					},
				}
			}
		}
		o.last[cacheKey] = observation
		o.mu.Unlock()
		result = append(result, ObserverRouter{
			Project: ObserverProject{ID: p.ID, Name: visibleText(p.Name)},
			Name:    RouterName(p), Namespace: RouterNamespace, RuntimeRef: visibleText(p.RuntimeRef),
			Requested: InitialSessionConfig{Model: visibleText(p.Router.Model), Effort: visibleText(p.Router.Effort)},
			CWD:       visibleText(p.CWD), Instructions: visibleText(RouterSystemPrompt(p)), Persisted: reference,
			Worker:      ObserverWorkerDefaults{Model: visibleText(p.Worker.Model), Effort: visibleText(p.Worker.Effort), RuntimeRefs: visibleTexts(p.Worker.RuntimeRefs)},
			Inbox:       ObserverBatching{MergeWindowMS: p.Inbox.MergeWindowMS, MaxBatchSize: p.Inbox.MaxBatchSize, MaxPromptBytes: p.Inbox.MaxPromptBytes},
			Observation: observation, ConfigSource: "loaded_config",
		})
	}
	return result
}

// Free text is displayed as text by the frontend. These masks cover common
// credential forms, not a promise of complete secret detection in arbitrary text.
var visibleSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bBearer\s+[^\s"'<>]+`),
	regexp.MustCompile(`(?i)\b(?:api[_-]?key|access[_-]?token|password|secret|token)["']?\s*[:=]\s*["']?[^\s"',;<>]+`),
}

func visibleText(value string) string {
	for _, pattern := range visibleSecretPatterns {
		value = pattern.ReplaceAllString(value, "[redacted]")
	}
	return value
}

func visibleTexts(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, visibleText(value))
	}
	return result
}
