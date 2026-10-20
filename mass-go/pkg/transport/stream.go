package transport

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"time"

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
		case event, ok := <-sub.Events():
			if !ok {
				if err := sub.Err(); err != nil {
					_ = send("subscription.error", PublicError(err))
				} else {
					_ = send("subscription.closed", struct{}{})
				}
				return
			}
			if err := send("session.event", event); err != nil {
				_ = send("subscription.error", &Error{Code: "internal", Reason: "event_encoding_failed", Message: "Event delivery failed"})
				return
			}
		}
	}
}
