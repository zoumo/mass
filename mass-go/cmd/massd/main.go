// massd composes Service and independently owned metadata and event stores.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	eventfile "github.com/zoumo/mass/mass-go/pkg/eventstore/file"
	"github.com/zoumo/mass/mass-go/pkg/metastore"
	"github.com/zoumo/mass/mass-go/pkg/metastore/backend"
	"github.com/zoumo/mass/mass-go/pkg/prototype"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/claude"
	"github.com/zoumo/mass/mass-go/pkg/runtime/codex"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type options struct {
	MetaStore          metastore.Config `mapstructure:"metaStore"`
	Data               string           `mapstructure:"data"`
	Listen             string           `mapstructure:"listen"`
	TokenFile          string           `mapstructure:"tokenFile"`
	TLSCert            string           `mapstructure:"tlsCert"`
	TLSKey             string           `mapstructure:"tlsKey"`
	AllowedOrigins     []string         `mapstructure:"allowedOrigins"`
	Approve            bool             `mapstructure:"prototypeAutoApprovePermissions"`
	Local              bool             `mapstructure:"local"`
	SessionIdleTimeout time.Duration    `mapstructure:"sessionIdleTimeout"`
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
	f.BoolVar(&o.Local, "local", false, "explicit trusted single-user loopback mode without authentication")
	f.StringVar(&o.TokenFile, "token-file", "", "file containing an instance Bearer token")
	f.StringVar(&o.TLSCert, "tls-cert", "", "TLS certificate for direct remote access")
	f.StringVar(&o.TLSKey, "tls-key", "", "TLS private key for direct remote access")
	f.StringSliceVar(&o.AllowedOrigins, "allowed-origin", nil, "allowed WebSocket Origin (repeatable)")
	f.DurationVar(&o.SessionIdleTimeout, "session-idle-timeout", service.DefaultSessionIdleTimeout, "idle Session close timeout (0 disables)")
	return cmd
}

func run(parent context.Context, o options) (result error) {
	if o.SessionIdleTimeout < 0 {
		return errors.New("session idle timeout must not be negative")
	}
	host, _, err := net.SplitHostPort(o.Listen)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return errors.New("listen address must be an IP literal")
	}
	token := ""
	if o.TokenFile != "" {
		b, e := os.ReadFile(o.TokenFile)
		if e != nil {
			return errors.New("cannot read token file")
		}
		token = strings.TrimSpace(string(b))
		if token == "" {
			return errors.New("empty token file")
		}
	}
	if o.Local && !ip.IsLoopback() {
		return errors.New("local mode requires loopback address")
	}
	if !ip.IsLoopback() && (token == "" || o.TLSCert == "" || o.TLSKey == "") {
		return errors.New("remote access requires a token and TLS")
	}
	if (o.TLSCert == "") != (o.TLSKey == "") {
		return errors.New("TLS requires both certificate and key")
	}
	if (o.Local && token != "") || (!o.Local && token == "") {
		return errors.New("select local mode or token authentication")
	}
	dataDir, err := canonicalDataDir(o.Data)
	if err != nil {
		return err
	}
	meta, err := backend.Open(dataDir, o.MetaStore, false)
	if err != nil {
		return dataDirectoryError(err)
	}
	defer func() { result = errors.Join(result, meta.Close()) }()
	history, err := eventfile.Open(dataDir, false)
	if err != nil {
		return dataDirectoryError(err)
	}
	defer func() { result = errors.Join(result, history.Close()) }()
	var policy service.InteractionPolicy
	if o.Approve {
		policy = prototype.AutoApprovePermissions
	}
	svc := service.NewWithOptions(meta, history, runtime.NewRegistry(map[string]runtime.Factory{"codex": codex.Factory{RecoveryDir: filepath.Join(dataDir, "private")}, "claude": claude.Factory{RecoveryDir: filepath.Join(dataDir, "private")}}), service.Options{
		InteractionPolicy:  policy,
		SessionIdleTimeout: o.SessionIdleTimeout,
	})
	settled := false
	defer func() {
		if !settled {
			ctx, c := context.WithTimeout(context.Background(), 10*time.Second)
			defer c()
			result = errors.Join(result, svc.Shutdown(ctx))
		}
	}()
	if err := svc.Prepare(parent); err != nil {
		return err
	}
	listener, err := (&net.ListenConfig{}).Listen(parent, "tcp", o.Listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	ctx, cancel := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer cancel()
	handler, err := transport.New(svc, transport.Options{Token: token, LocalOnly: o.Local, AllowedOrigins: o.AllowedOrigins})
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			handler.Shutdown()
		}
	}()
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() {
		if o.TLSCert != "" {
			done <- server.ServeTLS(listener, o.TLSCert, o.TLSKey)
		} else {
			done <- server.Serve(listener)
		}
	}()
	scheme := "ws://"
	if o.TLSCert != "" {
		scheme = "wss://"
	}
	if err = json.NewEncoder(os.Stdout).Encode(map[string]string{"url": scheme + listener.Addr().String() + "/rpc"}); err != nil {
		server.Close()
		return err
	}
	var serveErr error
	select {
	case serveErr = <-done:
	case <-ctx.Done():
	}
	// Stop admission, settle Service final events while observations remain
	// writable, drain those observations, then close HTTP and the Stores.
	handler.CloseAdmission()
	settleCtx, settleCancel := context.WithTimeout(context.Background(), 10*time.Second)
	settleErr := svc.Shutdown(settleCtx)
	settleCancel()
	settled = true
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 5*time.Second)
	drainErr := handler.DrainAndClose(drainCtx)
	drainCancel()
	closed = true
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
	return errors.Join(serveErr, settleErr, drainErr, httpErr)
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
			Message: "Another massd process owns this data directory",
		}
	}
	return err
}

func main() {
	if err := newCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, transport.PublicError(err))
		os.Exit(1)
	}
}
