package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/zoumo/mass/mass-go/pkg/massctl"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := massctl.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	cancel()
	os.Exit(code)
}
