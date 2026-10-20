// massd owns Service and its file Store; the transport owns no resource directory.
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

	"github.com/zoumo/mass/mass-go/pkg/prototype"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/claude"
	"github.com/zoumo/mass/mass-go/pkg/runtime/codex"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type options struct {
	Data      string `mapstructure:"data"`
	Listen    string `mapstructure:"listen"`
	TokenFile string `mapstructure:"tokenFile"`
	TLSCert   string `mapstructure:"tlsCert"`
	TLSKey    string `mapstructure:"tlsKey"`
	Approve   bool   `mapstructure:"prototypeAutoApprovePermissions"`
	Local     bool   `mapstructure:"local"`
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
	f.StringVar(&o.Data, "data", filepath.Join(home, ".mass", "data"), "absolute owned data directory")
	f.StringVar(&o.Listen, "listen", "127.0.0.1:24368", "loopback listen address (port 0 supported)")
	f.BoolVar(&o.Approve, "prototype-auto-approve-permissions", false, "explicit synthetic prototype policy; questions remain manual")
	f.BoolVar(&o.Local, "local", false, "explicit trusted single-user loopback mode without authentication")
	f.StringVar(&o.TokenFile, "token-file", "", "file containing an instance Bearer token")
	f.StringVar(&o.TLSCert, "tls-cert", "", "TLS certificate for direct remote access")
	f.StringVar(&o.TLSKey, "tls-key", "", "TLS private key for direct remote access")
	cmd.AddCommand(newOpenAPICommand())
	return cmd
}

func run(parent context.Context, o options) (result error) {
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
	st, err := file.Open(o.Data, false)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, st.Close()) }()
	var policy service.InteractionPolicy
	if o.Approve {
		policy = prototype.AutoApprovePermissions
	}
	svc := service.NewWithPolicy(st, st, runtime.NewRegistry(map[string]runtime.Factory{"codex": codex.Factory{RecoveryDir: filepath.Join(o.Data, "private")}, "claude": claude.Factory{RecoveryDir: filepath.Join(o.Data, "private")}}), policy)
	defer func() {
		ctx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		result = errors.Join(result, svc.Shutdown(ctx))
	}()
	listener, err := (&net.ListenConfig{}).Listen(parent, "tcp", o.Listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	ctx, cancel := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer cancel()
	handler, err := transport.New(svc, transport.Options{Token: token, LocalOnly: o.Local})
	if err != nil {
		return err
	}
	defer handler.Shutdown()
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() {
		if o.TLSCert != "" {
			done <- server.ServeTLS(listener, o.TLSCert, o.TLSKey)
		} else {
			done <- server.Serve(listener)
		}
	}()
	scheme := "http://"
	if o.TLSCert != "" {
		scheme = "https://"
	}
	if err = json.NewEncoder(os.Stdout).Encode(map[string]string{"url": scheme + listener.Addr().String()}); err != nil {
		server.Close()
		return err
	}
	select {
	case err = <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}
	// Close admission and streams before HTTP shutdown; Service settles Agent work.
	handler.Shutdown()
	stop, c := context.WithTimeout(context.Background(), 10*time.Second)
	defer c()
	err = server.Shutdown(stop)
	if err != nil {
		_ = server.Close()
	}
	serveErr := <-done
	if !errors.Is(serveErr, http.ErrServerClosed) {
		err = errors.Join(err, serveErr)
	}
	return err
}

func main() {
	if err := newCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, transport.PublicError(err))
		os.Exit(1)
	}
}
