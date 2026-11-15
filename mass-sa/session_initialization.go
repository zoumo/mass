package sa

import (
	"context"
	"encoding/json"
	"time"
)

const createdSessionSettleTimeout = 30 * time.Second

var createdSessionSettleInterval = 200 * time.Millisecond

// awaitCreatedSession waits for a create-only initial input to move the same
// confirmed Session identity out of its transient unknown state. It never
// submits the initialization prompt again and never creates a replacement.
func awaitCreatedSession(ctx context.Context, client sessionClient, created Session) (Session, error) {
	session := created
	if session.Status.State != "unknown" && session.Status.State != "opening" {
		return session, nil
	}
	if len(session.InitialInputReceipt) == 0 || session.Status.SessionID == "" {
		return session, nil
	}

	receipt := append(json.RawMessage(nil), session.InitialInputReceipt...)
	deadline := time.Now().Add(createdSessionSettleTimeout)
	ticker := time.NewTicker(createdSessionSettleInterval)
	defer ticker.Stop()

	for {
		updated, err := client.GetSession(ctx, SessionRef{SessionID: session.Status.SessionID})
		if err != nil {
			return session, err
		}
		updated.InitialInputReceipt = append(json.RawMessage(nil), receipt...)
		session = updated
		switch session.Status.State {
		case "idle", "running", "requires_action", "offline", "closing":
			return session, nil
		case "unknown", "opening":
		default:
			return session, nil
		}
		if !time.Now().Before(deadline) {
			return session, operationError(
				"failed_precondition",
				"created_session_unsettled",
				"new Session did not become available after its create-only initial input",
			)
		}
		select {
		case <-ctx.Done():
			return session, ctx.Err()
		case <-ticker.C:
		}
	}
}
