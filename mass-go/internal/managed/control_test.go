package managed

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

// This transport holds Submit's result until EOF has reached the publisher.
// It reproduces the ordering without timing the SDK/mock process scheduler.
type eofDuringSubmit struct {
	NativeSession
	started chan struct{}
	release chan struct{}
	ack     bool
}

func (n *eofDuringSubmit) Key() rt.SessionKey {
	return rt.SessionKey{RuntimeName: "worker", SessionID: "native"}
}
func (n *eofDuringSubmit) Capabilities() rt.Capabilities {
	return rt.Capabilities{NativeTurnID: true, NativeAcknowledgement: true}
}
func (n *eofDuringSubmit) Snapshot() rt.SessionSnapshot {
	return rt.SessionSnapshot{Key: n.Key(), Attachment: "attached"}
}
func (n *eofDuringSubmit) Next(ctx context.Context) (rt.Event, error) {
	select {
	case <-n.started:
		return rt.Event{}, io.EOF
	case <-ctx.Done():
		return rt.Event{}, ctx.Err()
	}
}
func (n *eofDuringSubmit) Submit(context.Context, string) (rt.Submission, error) {
	close(n.started)
	<-n.release
	r := rt.UnknownSubmission()
	r.HandedToSDK = rt.Known(true)
	if !n.ack {
		return r, rt.ErrOutcomeUnknown
	}
	r.Outcome = "admitted"
	r.WorkRef = "native-work"
	r.NativeAcknowledged = rt.Known(true)
	return r, nil
}
func (n *eofDuringSubmit) InvalidateInteractions(string) {}
func (n *eofDuringSubmit) Close() error                  { return nil }

func TestEOFDuringSubmitPreservesIndependentAcceptance(t *testing.T) {
	for _, ack := range []bool{true, false} {
		t.Run(map[bool]string{true: "acknowledged", false: "unknown"}[ack], func(t *testing.T) {
			n := &eofDuringSubmit{started: make(chan struct{}), release: make(chan struct{}), ack: ack}
			s := NewSession(n).(*publishedSession)
			defer s.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			type result struct {
				receipt rt.Submission
				err     error
			}
			done := make(chan result, 1)
			go func() { r, err := s.Submit(ctx, rt.TextInput("hello")); done <- result{r, err} }()
			// finished closes only after EOF has updated the retained state.
			select {
			case <-s.finished:
			case <-ctx.Done():
				close(n.release)
				t.Fatal("EOF did not reach publisher")
			}
			close(n.release)
			got := <-done
			if ack {
				if got.err != nil || got.receipt.Outcome != "admitted" || !isKnown(got.receipt.NativeAcknowledged) || got.receipt.Failure != nil {
					t.Fatalf("ACK lost after EOF: %+v: %v", got.receipt, got.err)
				}
			} else if !errors.Is(got.err, rt.ErrOutcomeUnknown) || got.receipt.Outcome != "unknown" {
				t.Fatalf("unacknowledged input claimed accepted: %+v: %v", got.receipt, got.err)
			}
			retained, err := s.GetSubmission(got.receipt.OperationRef)
			if err != nil || retained.Completed.State != "unknown" || s.Snapshot().Foreground.State != "unknown" {
				t.Fatalf("EOF claimed completion: %+v: %v", retained, err)
			}
		})
	}
}
