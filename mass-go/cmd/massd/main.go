package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/zoumo/mass/mass-go/internal/httpserver"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "massd-go:", err)
		os.Exit(1)
	}
}

func run() error {
	listen := flag.String("listen", "127.0.0.1:24369", "HTTP JSON-RPC/SSE listener")
	codex := flag.String("codex-bin", "", "absolute Codex executable; registers runtime codex")
	claude := flag.String("claude-bin", "", "absolute Claude executable; registers runtime claude")
	cwd := flag.String("cwd", "", "absolute working directory for the registered runtimes")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if *cwd == "" {
		var err error
		*cwd, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	if !filepath.IsAbs(*cwd) {
		return fmt.Errorf("--cwd must be absolute")
	}
	info, err := os.Stat(*cwd)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("--cwd must name an existing directory")
	}
	registrations := []rt.Registration{}
	for _, entry := range []struct{ name, path string }{{"codex", *codex}, {"claude", *claude}} {
		if entry.path == "" {
			continue
		}
		if !filepath.IsAbs(entry.path) {
			return fmt.Errorf("--%s-bin must be absolute", entry.name)
		}
		registrations = append(registrations, rt.Registration{Name: entry.name, Type: entry.name, Config: rt.Config{Executable: entry.path, Dir: *cwd, Env: os.Environ()}})
	}
	if len(registrations) == 0 {
		return fmt.Errorf("provide --codex-bin and/or --claude-bin")
	}
	service, err := httpserver.New(registrations)
	if err != nil {
		return err
	}
	defer service.Close()
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: service.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	fmt.Printf("Listening on http://%s (Go HTTP+SSE)\n", listener.Addr())
	err = server.Serve(listener)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
