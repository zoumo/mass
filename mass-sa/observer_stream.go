package sa

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// streamSession observes only. The dedicated upstream subscription is established
// before history and remains bounded by MASSEventSubscription's queue.
func (o *Observer) streamSession(w http.ResponseWriter, r *http.Request, resource string) {
	q := r.URL.Query()
	if err := validateObserverQuery(resource, q); err != nil {
		writeManagementError(w, managementStatus(err), err, nil)
		return
	}
	scope, err := o.resourceContext(q)
	if err != nil {
		writeManagementError(w, managementStatus(err), err, nil)
		return
	}
	if _, err = resourceLimit(q); err != nil {
		writeManagementError(w, managementStatus(err), err, nil)
		return
	}
	if _, err = historyTypes(q); err != nil {
		writeManagementError(w, managementStatus(err), err, nil)
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(resource, "sessions/"), "/events")
	id, err = url.PathUnescape(id)
	if err != nil {
		writeManagementError(w, 400, operationError("invalid_argument", "invalid_session_id", "invalid encoded Session ID"), nil)
		return
	}
	indexProject := q.Get("project")
	if scope == "all" {
		indexProject = ""
	}
	snapshot, err := o.snapshot()
	if err != nil {
		writeManagementError(w, managementStatus(err), err, nil)
		return
	}
	index, _ := o.resourceIndex(snapshot, indexProject)
	if id == "" || (scope == "associated" && len(index[id]) == 0) {
		err = operationError("not_found", "session_not_associated", "Session has no durable association in this context; use scope=all")
		writeManagementError(w, managementStatus(err), err, nil)
		return
	}
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	setup, setupCancel := context.WithTimeout(ctx, 4*time.Second)
	client, err := OpenMASS(setup, o.config.MASS)
	if err != nil {
		setupCancel()
		writeManagementError(w, 503, safeResourceError(err), nil)
		return
	}
	defer client.Close()
	ref := SessionRef{SessionID: id}
	sub, err := client.Subscribe(setup, ref)
	setupCancel()
	if err != nil {
		writeManagementError(w, 503, safeResourceError(err), nil)
		return
	}
	defer sub.Close()
	// Clear only this streaming response's inherited WriteTimeout; each write gets
	// a fresh bounded deadline. Other HTTP routes retain the server's protection.
	controller := http.NewResponseController(w)
	if err = controller.SetWriteDeadline(time.Time{}); err != nil {
		writeManagementError(w, 500, operationError("unavailable", "stream_unsupported", "streaming deadlines are unavailable"), nil)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(kind string, value any) bool {
		raw, e := json.Marshal(value)
		if e != nil {
			return false
		}
		if controller.SetWriteDeadline(time.Now().Add(5*time.Second)) != nil {
			return false
		}
		if _, e = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, raw); e != nil {
			return false
		}
		return controller.Flush() == nil
	}
	if !send("status", map[string]any{"state": "connected", "at": time.Now().UTC(), "previewGap": true, "message": "仅持久历史可补读；实时预览可能缺失"}) {
		return
	}
	// Keep only a bounded overlap identity window. Never deduplicate live previews
	// against a durable ID: the same ID can later carry a complete observation.
	seen := map[string]bool{}
	order := []string{}
	remember := func(key string) {
		if seen[key] {
			return
		}
		seen[key] = true
		order = append(order, key)
		if len(order) > 2048 {
			delete(seen, order[0])
			order = order[1:]
		}
	}
	checkpoint := q.Get("cursor")
	first := true
	history := func() bool {
		hq := url.Values{"limit": {q.Get("limit")}, "eventTypes": {q.Get("eventTypes")}, "cursor": {checkpoint}, "window": {"page"}}
		if first && checkpoint == "" && q.Get("window") != "page" {
			hq.Set("window", "recent")
		}
		first = false
		for pages := 0; pages < 1024; pages++ {
			readCtx, stop := context.WithTimeout(ctx, 4*time.Second)
			page, e := readObserverHistory(readCtx, client, ref, hq)
			stop()
			if e != nil {
				send("status", map[string]any{"state": "disconnected", "at": time.Now().UTC(), "reason": "history_unavailable", "previewGap": true, "message": "持久历史补读失败；保留旧记录并重连"})
				return false
			}
			pending := []ObserverEvent{}
			for _, event := range page.Events {
				if !seen[event.EventID] {
					pending = append(pending, event)
					remember(event.EventID)
				}
			}
			page.Events = pending
			if !send("history", page) {
				return false
			}
			checkpoint = page.Cursor
			if page.Next == "" {
				break
			}
			checkpoint = page.Next
			hq.Set("cursor", checkpoint)
			hq.Set("window", "page")
			if pages == 1023 {
				send("status", map[string]any{"state": "disconnected", "at": time.Now().UTC(), "reason": "history_scan_limit", "previewGap": true})
				return false
			}
		}
		return send("checkpoint", map[string]any{"cursor": checkpoint, "at": time.Now().UTC(), "state": "live", "message": "持久历史已补读；实时预览可能缺失"})
	}
	if !history() {
		return
	}
	type nextResult struct {
		event SessionEvent
		err   error
	}
	next := make(chan nextResult)
	go func() {
		for {
			event, e := sub.Next(ctx)
			select {
			case next <- nextResult{event, e}:
			case <-ctx.Done():
				return
			}
			if e != nil {
				return
			}
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case result := <-next:
			if result.err != nil {
				reason := "observation_disconnected"
				if IsErrorCode(result.err, "resource_exhausted") {
					reason = "slow_subscriber"
				}
				send("status", map[string]any{"state": "disconnected", "reason": reason, "at": time.Now().UTC(), "previewGap": true, "message": "观察已断开；重连补读持久历史，实时预览可能缺失"})
				return
			}
			if result.event.SessionID != id {
				send("status", map[string]any{"state": "disconnected", "reason": "session_identity_mismatch", "at": time.Now().UTC(), "previewGap": true})
				return
			}
			if !send("preview", observerEvent(result.event, true)) {
				return
			}
		case <-ticker.C:
			if !history() {
				return
			}
		}
	}
}
