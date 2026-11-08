package transport

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestPublicErrorPrefersDeterministicRejectionOverResourceRelease(t *testing.T) {
	invalid := errors.Join(runtime.ErrResourcesReleased, runtime.ErrInvalidArgument, errors.New("invalid session option"))
	public := PublicError(invalid)
	require.Equal(t, "invalid_argument", public.Code)
	require.Equal(t, 400, statusFor(public.Code))

	unsupported := errors.Join(runtime.ErrResourcesReleased, runtime.UnsupportedOption("unsupported_option", "Session option is not supported"))
	public = PublicError(unsupported)
	require.Equal(t, "unsupported", public.Code)
	require.Equal(t, "unsupported_option", public.Reason)
	require.Equal(t, 422, statusFor(public.Code))
}
