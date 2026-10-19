// massd owns Service and its file Store; the transport owns no resource directory.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/zoumo/mass/mass-go/pkg/prototype"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/claude"
	"github.com/zoumo/mass/mass-go/pkg/runtime/codex"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
	"github.com/zoumo/mass/mass-go/pkg/transport"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func run() (result error) {
	data := flag.String("data", "", "absolute owned data directory")
	listen := flag.String("listen", "127.0.0.1:8787", "loopback listen address (port 0 supported)")
	approve := flag.Bool("prototype-auto-approve-permissions", false, "explicit synthetic prototype policy; questions remain manual")
	flag.Parse()
	if flag.NArg() != 0 {
		return transport.Invalid()
	}
	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("experimental server requires loopback address")
	}
	st, err := file.Open(*data, false)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, st.Close()) }()
	var policy service.InteractionPolicy
	if *approve {
		policy = prototype.AutoApprovePermissions
	}
	svc := service.NewWithPolicy(st, st, runtime.NewRegistry(map[string]runtime.Factory{"codex": codex.Factory{RecoveryDir: filepath.Join(*data, "private")}, "claude": claude.Factory{RecoveryDir: filepath.Join(*data, "private")}}), policy)
	defer func() {
		ctx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		result = errors.Join(result, svc.Shutdown(ctx))
	}()
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	server := &http.Server{Handler: transport.New(svc), BaseContext: func(net.Listener) context.Context { return ctx }, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	if err = json.NewEncoder(os.Stdout).Encode(map[string]string{"url": "http://" + listener.Addr().String()}); err != nil {
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
	// Cancel stream waits before graceful shutdown; only Service settles Agent work.
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
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, transport.PublicError(err))
		os.Exit(1)
	}
}
