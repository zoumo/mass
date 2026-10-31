package claude

import (
	"context"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func (*Driver) LoadSession(context.Context, string, runtime.Address, string, string) error {
	return runtime.Unsupported()
}
func (*Driver) CancelHistoryLoad(context.Context, string, string) error { return runtime.Unsupported() }
