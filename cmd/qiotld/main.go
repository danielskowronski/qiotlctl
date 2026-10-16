package main

import (
	"context"
	"fmt"
	"github.com/danielskowronski/qiotlctl/internal/cli"
	"os"
	"os/signal"
	"syscall"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := cli.New(true, version).ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(cli.ExitCode(err))
	}
}
