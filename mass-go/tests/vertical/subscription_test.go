package vertical_test

import (
	"context"
	"io"

	"github.com/zoumo/mass/mass-go/pkg/events"
)

func nextSubscription(sub *events.Subscription, ctx context.Context) (events.SessionEvent, error) {
	select {
	case e, ok := <-sub.Events():
		if ok {
			return e, nil
		}
		if err := sub.Err(); err != nil {
			return events.SessionEvent{}, err
		}
		return events.SessionEvent{}, io.EOF
	case <-ctx.Done():
		return events.SessionEvent{}, ctx.Err()
	}
}
