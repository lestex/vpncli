package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/lestex/vpncli/internal/cli"
)

func main() {
	// Ctrl-C cancels the context rather than killing the process outright, so
	// long provisioning waits can unwind cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cli.NewRootCommand().ExecuteContext(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "vpncli: cancelled")
			os.Exit(130)
		}
		fmt.Fprintf(os.Stderr, "vpncli: %v\n", err)
		os.Exit(1)
	}
}
