// Package massd owns the MASS Service lifecycle for standalone and embedded deployments.
package massd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/zoumo/agent-sdk-go/agent"

	eventfile "github.com/zoumo/mass/massd/pkg/eventstore/file"
	"github.com/zoumo/mass/massd/pkg/metastore"
	"github.com/zoumo/mass/massd/pkg/metastore/backend"
	"github.com/zoumo/mass/massd/pkg/prototype"
	"github.com/zoumo/mass/massd/pkg/resource"
	"github.com/zoumo/mass/massd/pkg/runtime"
	"github.com/zoumo/mass/massd/pkg/runtime/claude"
	"github.com/zoumo/mass/massd/pkg/runtime/codex"
	"github.com/zoumo/mass/massd/pkg/service"
	"github.com/zoumo/mass/massd/pkg/store"
	"github.com/zoumo/mass/massd/pkg/transport"
)

const (
	settlementTimeout = 10 * time.Second
	drainTimeout      = 5 * time.Second
	// ShutdownTimeout allows both Service settlement and RPC drain to complete.
	ShutdownTimeout     = settlementTimeout + drainTimeout
	DefaultWriteTimeout = 10 * time.Second
)

// Options configures one MASS instance. The data directory must be absolute.
type Options struct {
	DataDir                         string
	MetaStore                       metastore.Config
	SessionIdleTimeout              time.Duration
	PrototypeAutoApprovePermissions bool
	Transport                       transport.Options
}

// Instance retains exclusive ownership of its data directory and serves both
// in-process and WebSocket clients through one RPC endpoint.
type Instance struct {
	handler      *transport.Server
	service      *service.Service
	meta         backend.Store
	history      *eventfile.Store
	shutdownOnce sync.Once
	shutdownErr  error
}

func Start(ctx context.Context, opts Options) (_ *Instance, result error) {
	if opts.SessionIdleTimeout < 0 {
		return nil, errors.New("session idle timeout must not be negative")
	}
	if opts.Transport.WriteTimeout <= 0 {
		return nil, errors.New("write timeout must be positive")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dataDir, err := canonicalDataDir(opts.DataDir)
	if err != nil {
		return nil, err
	}
	meta, err := backend.Open(dataDir, opts.MetaStore, false)
	if err != nil {
		return nil, dataDirectoryError(err)
	}
	history, err := eventfile.Open(dataDir, false)
	if err != nil {
		return nil, errors.Join(dataDirectoryError(err), meta.Close())
	}
	var policy service.InteractionPolicy
	if opts.PrototypeAutoApprovePermissions {
		policy = prototype.AutoApprovePermissions
	}
	traces, metrics := opts.Transport.TracerProvider, opts.Transport.MeterProvider
	recoveryDir := filepath.Join(dataDir, "private")
	registry := runtime.NewRegistry(map[string]runtime.Factory{
		"codex":  codex.Factory{RecoveryDir: recoveryDir, TracerProvider: traces, MeterProvider: metrics},
		"claude": claude.Factory{RecoveryDir: recoveryDir, TracerProvider: traces, MeterProvider: metrics},
	})
	svc := service.NewWithOptions(meta, history, registry, service.Options{
		InteractionPolicy:  policy,
		SessionIdleTimeout: opts.SessionIdleTimeout,
		TracerProvider:     traces,
		MeterProvider:      metrics,
	})
	instance := &Instance{service: svc, meta: meta, history: history}
	defer func() {
		if result != nil {
			result = errors.Join(result, instance.Shutdown(context.Background()))
		}
	}()
	if err := svc.Prepare(ctx); err != nil {
		return nil, err
	}
	if err := registerDefaultRuntimes(svc); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	instance.handler, err = transport.New(svc, opts.Transport)
	if err != nil {
		return nil, err
	}
	return instance, nil
}

// Handler shares one connection registry across all transport entry points.
func (i *Instance) Handler() *transport.Server { return i.handler }

// Shutdown closes admission, settles Service work, drains RPC connections,
// then releases the stores if Service ownership was settled.
func (i *Instance) Shutdown(ctx context.Context) error {
	i.shutdownOnce.Do(func() {
		if i.handler != nil {
			i.handler.CloseAdmission()
		}
		settled, settleErr := settleService(ctx, i.service, settlementTimeout)
		var drainErr error
		if i.handler != nil {
			drainCtx, cancel := context.WithTimeout(ctx, drainTimeout)
			defer cancel()
			drainErr = i.handler.DrainAndClose(drainCtx)
		}
		i.shutdownErr = errors.Join(settleErr, drainErr, closeStores(settled && drainErr == nil, i.history, i.meta))
	})
	return i.shutdownErr
}

func registerDefaultRuntimes(svc *service.Service) error {
	for _, name := range []string{"claude", "codex"} {
		_, err := svc.GetRuntime(name)
		switch {
		case err == nil:
			continue
		case !errors.Is(err, store.ErrNotFound):
			return err
		}
		_, err = svc.CreateRuntime(resource.Runtime{
			Metadata: resource.Metadata{Name: name},
			Spec: resource.RuntimeSpec{
				Type:             name,
				Config:           runtime.Config{},
				PermissionPreset: agent.PermissionPresetFullAccess,
			},
		})
		if err != nil && !errors.Is(err, store.ErrConflict) {
			return err
		}
	}
	return nil
}

func settleService(parent context.Context, svc interface {
	Shutdown(context.Context) error
}, timeout time.Duration,
) (bool, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	err := svc.Shutdown(ctx)
	return err == nil, err
}

// closeStores releases the single-writer data directory only after Service
// settlement. On an abandoned settlement the process must keep ownership until
// exit rather than let another instance take over unfinished work.
func closeStores(release bool, stores ...interface{ Close() error }) error {
	if !release {
		return nil
	}
	var result error
	for _, store := range stores {
		result = errors.Join(result, store.Close())
	}
	return result
}

func canonicalDataDir(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("absolute data directory required")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func dataDirectoryError(err error) error {
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return &runtime.OperationError{
			Code:    "conflict",
			Reason:  "data_directory_in_use",
			Message: "Another MASS instance owns this data directory",
		}
	}
	return err
}
