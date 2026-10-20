package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return runCommand(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
