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
	ctx, stop := interrupted(context.Background())
	defer stop()

	if err := cli.NewRootCommand().ExecuteContext(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "vpncli: canceled")
			os.Exit(130)
		}
		fmt.Fprintf(os.Stderr, "vpncli: %v\n", err)
		os.Exit(1)
	}
}

// interrupted returns a context canceled by Ctrl-C or SIGTERM.
//
// The first signal cancels rather than killing the process, so a provisioning
// wait unwinds and a half-created server is still reported. The handler is
// then removed, which puts the signal back to its default: a second Ctrl-C
// kills, because by then the user has said twice that they mean it and
// something is clearly not letting go.
func interrupted(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)

	go func() {
		select {
		case <-signals:
			cancel()
		case <-ctx.Done():
		}
		signal.Stop(signals)
	}()

	return ctx, func() {
		signal.Stop(signals)
		cancel()
	}
}
