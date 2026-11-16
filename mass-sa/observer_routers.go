package sa

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"sort"
	"time"
)

type observerRouterTraversal struct {
	Rows        []ObserverRouter
	Fingerprint [32]byte
	At          time.Time
}

// Router rows include durable references and live observations, so pagination
// freezes the whole projection rather than only the immutable Project config.
func (o *Observer) routerPage(ctx context.Context, snapshot State, q url.Values) (ObserverPage[ObserverRouter], error) {
	limit, err := resourceLimit(q)
	if err != nil {
		return ObserverPage[ObserverRouter]{}, err
	}
	if q.Get("project") != "" {
		if err := o.triggerProject(q.Get("project")); err != nil {
			return ObserverPage[ObserverRouter]{}, err
		}
	}
	fingerprint := sha256.Sum256([]byte(q.Get("project")))
	var cursor struct {
		ID    string `json:"id"`
		After string `json:"after"`
	}
	now := time.Now().UTC()
	var traversal observerRouterTraversal
	start := 0
	if raw := q.Get("cursor"); raw != "" {
		data, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.ID == "" {
			return ObserverPage[ObserverRouter]{}, invalidTriggerCursor()
		}
		o.mu.Lock()
		traversal = o.routerPages[cursor.ID]
		o.mu.Unlock()
		if traversal.At.IsZero() || now.Sub(traversal.At) > 15*time.Minute || traversal.Fingerprint != fingerprint {
			return ObserverPage[ObserverRouter]{}, invalidTriggerCursor()
		}
		start = sort.Search(len(traversal.Rows), func(i int) bool { return traversal.Rows[i].Project.ID >= cursor.After })
		if start == len(traversal.Rows) || traversal.Rows[start].Project.ID != cursor.After {
			return ObserverPage[ObserverRouter]{}, invalidTriggerCursor()
		}
		start++
	} else {
		projects := make([]ProjectConfig, 0, len(o.config.Projects))
		for _, project := range o.config.Projects {
			if q.Get("project") == "" || project.ID == q.Get("project") {
				projects = append(projects, project)
			}
		}
		sort.Slice(projects, func(i, j int) bool { return projects[i].ID < projects[j].ID })
		traversal = observerRouterTraversal{Rows: o.routers(ctx, snapshot, projects), Fingerprint: fingerprint, At: now}
	}
	end := min(start+limit, len(traversal.Rows))
	next := ""
	if end < len(traversal.Rows) {
		if cursor.ID == "" {
			var id [18]byte
			if _, err := rand.Read(id[:]); err != nil {
				return ObserverPage[ObserverRouter]{}, operationError("unavailable", "observer_cursor_unavailable", "cannot start Router traversal")
			}
			cursor.ID = base64.RawURLEncoding.EncodeToString(id[:])
			o.mu.Lock()
			if o.routerPages == nil {
				o.routerPages = map[string]observerRouterTraversal{}
			}
			for key, page := range o.routerPages {
				if now.Sub(page.At) > 15*time.Minute {
					delete(o.routerPages, key)
				}
			}
			if len(o.routerPages) >= 64 {
				oldest := ""
				var at time.Time
				for key, page := range o.routerPages {
					if oldest == "" || page.At.Before(at) {
						oldest, at = key, page.At
					}
				}
				delete(o.routerPages, oldest)
			}
			o.routerPages[cursor.ID] = traversal
			o.mu.Unlock()
		}
		cursor.After = traversal.Rows[end-1].Project.ID
		data, _ := json.Marshal(cursor)
		next = base64.RawURLEncoding.EncodeToString(data)
	}
	return ObserverPage[ObserverRouter]{Items: traversal.Rows[start:end], Next: next, ObservedAt: traversal.At, Source: "loaded_config_and_durable_mass_sa_traversal"}, nil
}
