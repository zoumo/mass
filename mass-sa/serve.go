package sa

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const serverShutdownTimeout = 15 * time.Second

// ServerReady reports the actual loopback endpoints after listeners and router
// bootstrap are ready. It is intended for startup diagnostics and tests.
type ServerReady struct {
	ManagementEndpoint string         `json:"managementEndpoint"`
	ToolEndpoint       string         `json:"toolEndpoint"`
	Routers            []RouterStatus `json:"routers"`
}

// ServeOption customizes the runnable service.
type ServeOption func(*serveOptions)

type serveOptions struct {
	mcpCommand string
	ready      func(ServerReady)
}

// WithServeMCPCommand overrides the absolute command placed in Session MCP
// configuration. The default is the running mass-sa executable.
func WithServeMCPCommand(command string) ServeOption {
	return func(options *serveOptions) {
		options.mcpCommand = command
	}
}

// WithServeReady observes successful startup without changing service state.
func WithServeReady(ready func(ServerReady)) ServeOption {
	return func(options *serveOptions) {
		options.ready = ready
	}
}

// RunServe starts the management API, internal tool endpoint, router bootstrap,
// and background dispatcher on one authenticated loopback listener. It returns
// only after cancellation or a terminal service error and releases every owned
// process-local resource before returning.
func RunServe(ctx context.Context, config Config, options ...ServeOption) error {
	if err := config.validate(); err != nil {
		return err
	}
	managementToken, err := config.ResolveManagementToken()
	if err != nil {
		return err
	}
	var settings serveOptions
	for _, option := range options {
		option(&settings)
	}
	if settings.mcpCommand != "" && !filepath.IsAbs(settings.mcpCommand) {
		return operationError("invalid_argument", "invalid_mcp_command", "MCP command must be absolute")
	}

	store, err := OpenStateStore(config.StateFile)
	if err != nil {
		return err
	}
	bridge, err := NewTicketBridge(config, store)
	if err != nil {
		return err
	}
	service, err := NewWorkerService(config, store, bridge, WorkerServiceOptions{MCPCommand: settings.mcpCommand})
	if err != nil {
		return err
	}
	if settings.mcpCommand == "" {
		executable, executableErr := os.Executable()
		if executableErr != nil {
			return operationError("unavailable", "mcp_command_unavailable", "mass-sa executable path is unavailable")
		}
		service.mcpCommand = filepath.Clean(executable)
	}

	listener, err := net.Listen("tcp", config.Management.Listen)
	if err != nil {
		return operationError("unavailable", "management_listen_failed", "management listener could not bind")
	}
	defer listener.Close()
	toolEndpoint := "http://" + listener.Addr().String() + "/v1/tools"
	if err := service.SetToolEndpoint(toolEndpoint); err != nil {
		return err
	}
	managementHandler, err := NewManagementHandler(service, managementToken)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/v1/tools/", service.HTTPHandler())
	mux.Handle(managementPrefix+"/", managementHandler)
	httpServer := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	serveErr := make(chan error, 1)
	go func() {
		err := httpServer.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()

	shutdown := func() error {
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), serverShutdownTimeout)
		defer shutdownCancel()
		return httpServer.Shutdown(shutdownCtx)
	}

	bridge.recoverTicketTransitions(runCtx)
	routers, err := BootstrapRouters(runCtx, config, store, WithRouterTools(service))
	if err != nil {
		return errors.Join(err, shutdown())
	}
	dispatcher, err := NewEventDispatcher(service)
	if err != nil {
		return errors.Join(err, shutdown())
	}
	dispatcherErr := make(chan error, 1)
	go func() {
		dispatcherErr <- dispatcher.Run(runCtx)
	}()
	if settings.ready != nil {
		settings.ready(ServerReady{
			ManagementEndpoint: "http://" + listener.Addr().String() + managementPrefix,
			ToolEndpoint:       toolEndpoint,
			Routers:            routers,
		})
	}
	for _, router := range routers {
		slog.Info("mass-sa router ready",
			"project", router.ProjectID,
			"sessionId", router.SessionID,
			"state", router.State,
			"model", router.Model,
			"effort", router.Effort,
		)
	}

	var terminal error
	dispatcherStopped := false
	select {
	case <-ctx.Done():
	case err := <-serveErr:
		if err != nil {
			terminal = fmt.Errorf("management HTTP server: %w", err)
		}
	case err := <-dispatcherErr:
		dispatcherStopped = true
		if err != nil {
			terminal = fmt.Errorf("event dispatcher: %w", err)
		}
	}
	shutdownErr := shutdown()
	if !dispatcherStopped {
		if err := <-dispatcherErr; err != nil {
			terminal = errors.Join(terminal, fmt.Errorf("event dispatcher shutdown: %w", err))
		}
	}
	if shutdownErr != nil {
		terminal = errors.Join(terminal, fmt.Errorf("management HTTP shutdown: %w", shutdownErr))
	}
	if errors.Is(terminal, http.ErrServerClosed) {
		terminal = nil
	}
	return terminal
}
