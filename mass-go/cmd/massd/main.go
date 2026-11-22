// massd composes Service and independently owned metadata and event stores.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	rpcwire "github.com/zoumo/mass/mass-go/pkg/jsonrpc"
	"github.com/zoumo/mass/mass-go/pkg/massd"
	"github.com/zoumo/mass/mass-go/pkg/metastore"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type options struct {
	MetaStore          metastore.Config `mapstructure:"metaStore"`
	Data               string           `mapstructure:"data"`
	DataDir            *string          `mapstructure:"dataDir"`
	Listen             string           `mapstructure:"listen"`
	TokenFile          string           `mapstructure:"tokenFile"`
	TLSCert            string           `mapstructure:"tlsCert"`
	TLSKey             string           `mapstructure:"tlsKey"`
	AllowedOrigins     []string         `mapstructure:"allowedOrigins"`
	Approve            bool             `mapstructure:"prototypeAutoApprovePermissions"`
	SessionIdleTimeout time.Duration    `mapstructure:"sessionIdleTimeout"`
	WriteTimeout       time.Duration    `mapstructure:"writeTimeout"`
}

func newCommand() *cobra.Command {
	var o options
	var configPath string
	home, homeErr := os.UserHomeDir()
	defaultConfig := filepath.Join(home, ".mass", "config.yaml")
	cmd := &cobra.Command{
		Use:           "massd",
		Short:         "Serve MASS resources and agent sessions",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if homeErr != nil {
				return homeErr
			}
			configured, err := readOptions(cmd, configPath)
			if err != nil {
				return err
			}
			return run(cmd.Context(), configured)
		},
	}
	f := cmd.Flags()
	f.StringVar(&configPath, "config", defaultConfig, "YAML configuration file (flags override configuration)")
	f.StringVar(&o.Data, "data", filepath.Join(home, ".mass"), "absolute owned data directory")
	f.StringVar(&o.Listen, "listen", "127.0.0.1:24368", "loopback listen address (port 0 supported)")
	f.BoolVar(&o.Approve, "prototype-auto-approve-permissions", false, "explicit synthetic prototype policy; questions remain manual")
	f.StringVar(&o.TokenFile, "token-file", "", "file containing an instance Bearer token (required for non-loopback listeners)")
	f.StringVar(&o.TLSCert, "tls-cert", "", "TLS certificate for direct remote access")
	f.StringVar(&o.TLSKey, "tls-key", "", "TLS private key for direct remote access")
	f.StringSliceVar(&o.AllowedOrigins, "allowed-origin", nil, "allowed WebSocket Origin (repeatable)")
	f.DurationVar(&o.SessionIdleTimeout, "session-idle-timeout", service.DefaultSessionIdleTimeout, "idle Session close timeout (0 disables)")
	f.DurationVar(&o.WriteTimeout, "write-timeout", massd.DefaultWriteTimeout, "WebSocket write deadline")
	return cmd
}

func run(parent context.Context, o options) (result error) {
	if o.SessionIdleTimeout < 0 {
		return errors.New("session idle timeout must not be negative")
	}
	if o.WriteTimeout <= 0 {
		return errors.New("write timeout must be positive")
	}
	host, _, err := net.SplitHostPort(o.Listen)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return errors.New("listen address must be an IP literal")
	}
	var token string
	if o.TokenFile != "" {
		b, err := os.ReadFile(o.TokenFile)
		if err != nil {
			return errors.New("cannot read token file")
		}
		token = strings.TrimSpace(string(b))
		if token == "" || strings.ContainsAny(token, " \t\r\n") {
			return errors.New("token file must contain a single nonempty token")
		}
	}
	if !ip.IsLoopback() && (token == "" || o.TLSCert == "" || o.TLSKey == "") {
		return errors.New("remote access requires a token and TLS")
	}
	if (o.TLSCert == "") != (o.TLSKey == "") {
		return errors.New("TLS requires both certificate and key")
	}
	telemetry, err := newTelemetry(parent)
	if err != nil {
		return &runtime.OperationError{Code: "invalid_argument", Reason: "invalid_telemetry_config", Message: err.Error()}
	}
	defer telemetry.shutdown()
	ctx, cancel := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer cancel()
	instance, err := massd.Start(ctx, massd.Options{
		DataDir: o.Data, MetaStore: o.MetaStore,
		SessionIdleTimeout: o.SessionIdleTimeout, PrototypeAutoApprovePermissions: o.Approve,
		Transport: transport.Options{
			Token: token, AllowedOrigins: o.AllowedOrigins, WriteTimeout: o.WriteTimeout,
			TracerProvider: telemetry.traces, MeterProvider: telemetry.metrics,
		},
	})
	if err != nil {
		return err
	}
	settled := false
	defer func() {
		if !settled {
			result = errors.Join(result, instance.Shutdown(context.Background()))
		}
	}()
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", o.Listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{Handler: instance.Handler(), ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() {
		if o.TLSCert != "" {
			done <- server.ServeTLS(listener, o.TLSCert, o.TLSKey)
		} else {
			done <- server.Serve(listener)
		}
	}()
	if token == "" {
		slog.Warn("massd RPC accepts unauthenticated loopback connections because tokenFile is unset")
	}
	scheme := "ws://"
	if o.TLSCert != "" {
		scheme = "wss://"
	}
	if err = json.NewEncoder(os.Stdout).Encode(map[string]string{"url": scheme + listener.Addr().String() + rpcwire.WebSocketPath}); err != nil {
		server.Close()
		return err
	}
	var serveErr error
	select {
	case serveErr = <-done:
	case <-ctx.Done():
	}
	settleErr := instance.Shutdown(context.Background())
	settled = true
	httpCtx, httpCancel := context.WithTimeout(context.Background(), 5*time.Second)
	httpErr := server.Shutdown(httpCtx)
	httpCancel()
	if httpErr != nil {
		_ = server.Close()
	}
	if serveErr == nil {
		// The serve goroutine reports after the listener is shut down.
		serveErr = <-done
	}
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	return errors.Join(serveErr, settleErr, httpErr)
}

func main() {
	if err := newCommand().Execute(); err != nil {
		if errors.Is(err, errConflictingDataFields) {
			fmt.Fprintln(os.Stderr, errConflictingDataFields)
		} else {
			fmt.Fprintln(os.Stderr, transport.PublicError(err))
		}
		os.Exit(1)
	}
}
