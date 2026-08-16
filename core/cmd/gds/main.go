package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(
		context.Background(), os.Interrupt, syscall.SIGTERM,
	)
	defer stop()
	os.Exit(cli.Execute(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
