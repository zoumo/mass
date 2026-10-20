package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"time"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/service"
)

func (s *Server) observe(w http.ResponseWriter, r *http.Request, sub *service.Subscription) {
	defer sub.Close()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	write := func(text string) error {
		_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := io.WriteString(w, text); err != nil {
			return err
		}
		return rc.Flush()
	}
	send := func(kind string, value any) error {
		if err := validateOutput(reflect.ValueOf(value)); err != nil {
			return err
		}
		b, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return write("event: " + kind + "\ndata: " + string(b) + "\n\n")
	}
	if send("subscription.ready", struct{}{}) != nil {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	type delivery struct {
		event events.SessionEvent
		err   error
	}
	next := make(chan delivery, 1)
	go func() {
		for {
			event, err := sub.Next(ctx)
			select {
			case next <- delivery{event, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.life.Done():
			_ = send("subscription.error", &Error{Code: "unavailable", Reason: "server_shutdown", Message: "Observation interrupted by server shutdown"})
			return
		case <-heartbeat.C:
			if write(": heartbeat\n\n") != nil {
				return
			}
		case value := <-next:
			if value.err != nil {
				if errors.Is(value.err, io.EOF) {
					_ = send("subscription.closed", struct{}{})
				} else {
					_ = send("subscription.error", PublicError(value.err))
				}
				return
			}
			if err := send("session.event", value.event); err != nil {
				_ = send("subscription.error", &Error{Code: "internal", Reason: "event_encoding_failed", Message: "Event delivery failed"})
				return
			}
		}
	}
}
