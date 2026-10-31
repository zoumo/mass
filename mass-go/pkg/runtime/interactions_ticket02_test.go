package runtime_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestInteractionAuthoritySettlement(t *testing.T) {
	unsent := errors.New("confirmed not sent")
	unknown := errors.New("dispatch outcome unknown")
	for _, mode := range []string{"live-unsent", "revoked-unsent", "unknown", "sent"} {
		t.Run(mode, func(t *testing.T) {
			var pending runtime.PendingInteractions
			source := events.NewSource().WithWork("original-work")
			live, first := true, true
			ref, err := pending.Add("original-session", source, func() bool { return live }, func(err error) bool { return errors.Is(err, unsent) }, func(context.Context, map[string]any) error {
				if !first {
					return nil
				}
				first = false
				switch mode {
				case "live-unsent":
					return unsent
				case "revoked-unsent":
					live = false
					return unsent
				case "unknown":
					return unknown
				}
				return nil
			})
			require.NoError(t, err)
			ctx := context.Background()
			for _, target := range []struct {
				session string
				source  events.Source
			}{
				{"other-session", source}, {"original-session", events.NewSource().WithWork("original-work")}, {"original-session", source.WithWork("next-work")},
			} {
				require.ErrorIs(t, pending.Answer(ctx, target.session, target.source, ref, nil), runtime.ErrFailedPrecondition)
			}
			err = pending.Answer(ctx, "original-session", source, ref, nil)
			if mode == "sent" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			err = pending.Answer(ctx, "original-session", source, ref, nil)
			if mode == "live-unsent" {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, runtime.ErrFailedPrecondition)
			}
		})
	}
}

func TestInteractionConcurrentReservationAndClose(t *testing.T) {
	for _, closeDuringAnswer := range []bool{false, true} {
		t.Run(map[bool]string{false: "one-winner", true: "close-no-resurrection"}[closeDuringAnswer], func(t *testing.T) {
			var pending runtime.PendingInteractions
			source := events.NewSource()
			entered, release := make(chan struct{}), make(chan struct{})
			var live atomic.Bool
			live.Store(true)
			unsent := errors.New("confirmed not sent")
			ref, err := pending.Add("session", source, live.Load, func(err error) bool { return errors.Is(err, unsent) }, func(context.Context, map[string]any) error {
				close(entered)
				<-release
				if closeDuringAnswer {
					return unsent
				}
				return nil
			})
			require.NoError(t, err)
			done := make(chan error, 1)
			go func() { done <- pending.Answer(context.Background(), "session", source, ref, nil) }()
			<-entered
			sibling := events.NewSource().WithWork("sibling-work")
			siblingRef, err := pending.Add("sibling-session", sibling, live.Load, func(error) bool { return false }, func(context.Context, map[string]any) error { return nil })
			require.NoError(t, err)
			require.NoError(t, pending.Answer(context.Background(), "sibling-session", sibling, siblingRef, nil), "a gated answer must not block a healthy session")
			err = pending.Answer(context.Background(), "session", source, ref, nil)
			require.ErrorIs(t, err, runtime.ErrFailedPrecondition)
			if closeDuringAnswer {
				pending.RemoveSession("session")
			}
			close(release)
			err = <-done
			if closeDuringAnswer {
				require.ErrorIs(t, err, unsent)
			} else {
				require.NoError(t, err)
			}
			require.ErrorIs(t, pending.Answer(context.Background(), "session", source, ref, nil), runtime.ErrFailedPrecondition)
		})
	}
}
