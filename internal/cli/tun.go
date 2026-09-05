package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/lestex/vpncli/internal/client"
	"github.com/lestex/vpncli/internal/state"
)

// newTunCommand groups the tunnel on this machine, as opposed to the servers
// it goes to.
//
// It is a wrapper around sing-box rather than a VPN client of its own: writing
// one would mean a tun device, a routing table and a userspace network stack,
// all of which sing-box already does well. What this adds is not having to
// keep a config file, a path and a sudo line in your head.
func newTunCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tun",
		Short: "Bring the tunnel on this machine up and down",
		Long: `The tunnel on this machine: the thing that routes your traffic through a server.

    vpncli tun up 3      route this machine through server 3
    vpncli tun status    what is running, and since when
    vpncli tun down      stop it

It runs ` + "`sing-box`" + `, which has to be installed. Everything it needs is
generated from local state, so bringing a tunnel up needs no API token.

Routing a whole machine means creating a network interface and rewriting the
routing table, so ` + "`up`" + ` and ` + "`down`" + ` ask for your password.`,
	}

	cmd.AddCommand(newTunUpCommand(), newTunDownCommand(), newTunStatusCommand())
	return cmd
}

func newTunUpCommand() *cobra.Command {
	var detach bool

	cmd := &cobra.Command{
		Use:   "up [id]",
		Short: "Route this machine through a server",
		Long: `Generate a client config and run sing-box against it.

With no id, the most recently configured server is used, which is almost
always the one you want after a provision or a rotation.

It runs in the foreground by default, so Ctrl-C brings the tunnel down and
nothing is left behind to forget about. ` + "`--detach`" + ` leaves it running after
the command returns, and ` + "`vpncli tun down`" + ` stops it.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var id int64
			if len(args) == 1 {
				parsed, err := strconv.ParseInt(args[0], 10, 64)
				if err != nil {
					return fmt.Errorf("%q is not a server id: `vpncli server list` shows them", args[0])
				}
				id = parsed
			}
			t, err := newTunnel()
			if err != nil {
				return err
			}
			return runTunUp(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), t, id, detach)
		},
	}

	cmd.Flags().BoolVar(&detach, "detach", false, "keep the tunnel up after this command returns")
	return cmd
}

func newTunDownCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "down",
		Short: "Stop the tunnel",
		Long: `Stop a tunnel started with ` + "`--detach`" + `.

A tunnel running in the foreground is stopped with Ctrl-C instead; there is
nothing here to stop.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			t, err := newTunnel()
			if err != nil {
				return err
			}
			return runTunDown(cmd.Context(), cmd.OutOrStdout(), t)
		},
	}
}

func newTunStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Say whether the tunnel is up",
		Long: `Report whether a tunnel is running, through which server, and since when.

It reads local state and one process, so it is instant and works offline. It
does not check that traffic is actually flowing - for that, ask something on
the internet where it thinks you are.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			t, err := newTunnel()
			if err != nil {
				return err
			}
			return runTunStatus(cmd.Context(), cmd.OutOrStdout(), t)
		},
	}
}

// runTunUp brings the tunnel up.
func runTunUp(ctx context.Context, in io.Reader, out io.Writer, t *tunnel, id int64, detach bool) error {
	// The client is somebody else's program and has to be there before
	// anything else is done: a config written for a client that is not
	// installed is a file nobody asked for.
	if _, err := t.run.Look(singBox); err != nil {
		return err
	}

	if _, running, err := t.running(ctx); err == nil {
		if running.Server != 0 {
			return fmt.Errorf("a tunnel through server %d is already up: `vpncli tun down` stops it", running.Server)
		}
		return errors.New("a tunnel is already up: `vpncli tun down` stops it")
	}

	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	srv, err := chooseServer(ctx, store, id)
	if err != nil {
		return err
	}

	generated, err := client.SingBox(srv, client.Tun)
	if err != nil {
		return err
	}
	if err := os.WriteFile(t.configPath(), generated, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", t.configPath(), err)
	}

	fmt.Fprintf(out, "Routing this machine through %s (%s, %s).\n", srv.Name, srv.IPv4, srv.Region)

	args := []string{singBox, "run", "-c", t.configPath()}
	if !detach {
		fmt.Fprintf(out, "Ctrl-C brings it down.\n\n")
		// Foreground: sudo owns the terminal, and its own signal handling
		// ends the tunnel. A context canceled by Ctrl-C would kill sudo
		// before sing-box had unwound, leaving the routes behind.
		err := t.run.Run(context.WithoutCancel(ctx), in, out, "sudo", args...)
		if err != nil && ctx.Err() == nil {
			return fmt.Errorf("running %s: %w", singBox, err)
		}
		fmt.Fprintf(out, "\ntunnel down\n")
		return nil
	}

	if err := t.run.Start(ctx, out, "sudo", args...); err != nil {
		return fmt.Errorf("starting %s: %w", singBox, err)
	}
	if err := t.save(record{Server: srv.ID, Started: time.Now()}); err != nil {
		return err
	}

	fmt.Fprintf(out, "tunnel up, `vpncli tun down` stops it\n")
	return nil
}

// runTunDown stops a detached tunnel.
func runTunDown(ctx context.Context, out io.Writer, t *tunnel) error {
	pids, running, err := t.running(ctx)
	if errors.Is(err, ErrNotRunning) {
		fmt.Fprintln(out, "no tunnel is running")
		return nil
	}
	if err != nil {
		return err
	}

	if err := t.run.Stop(ctx, out, pids); err != nil {
		return fmt.Errorf("stopping the tunnel: %w", err)
	}
	if err := t.forget(); err != nil {
		return err
	}

	if running.Started.IsZero() {
		fmt.Fprintln(out, "tunnel down")
		return nil
	}
	fmt.Fprintf(out, "tunnel down after %s\n", took(time.Since(running.Started)))
	return nil
}

// runTunStatus says what is up.
func runTunStatus(ctx context.Context, out io.Writer, t *tunnel) error {
	_, running, err := t.running(ctx)
	if errors.Is(err, ErrNotRunning) {
		fmt.Fprintln(out, "down")
		return nil
	}
	if err != nil {
		return err
	}

	if running.Server == 0 {
		// Running, but not started by this command - so there is nothing to
		// say about which server or since when.
		fmt.Fprintf(out, "up, from a tunnel this command did not start (%s)\n", t.configPath())
		return nil
	}

	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	srv, err := store.Get(ctx, running.Server)
	if err != nil {
		// The tunnel is up to a server the state file no longer has, which
		// is what destroying one from another terminal looks like.
		fmt.Fprintf(out, "up through server %d, which is no longer in local state, for %s\n",
			running.Server, took(time.Since(running.Started)))
		return nil
	}

	fmt.Fprintf(out, "up through %s (%s, %s) for %s\n",
		srv.Name, srv.IPv4, srv.Region, took(time.Since(running.Started)))
	return nil
}

// chooseServer resolves the id a tunnel is for. With none given it takes the
// most recently configured server, which after a provision or a rotation is
// the one meant.
func chooseServer(ctx context.Context, store *state.Store, id int64) (state.Server, error) {
	if id != 0 {
		return store.Get(ctx, id)
	}

	servers, err := store.List(ctx)
	if err != nil {
		return state.Server{}, err
	}
	for _, srv := range servers {
		if srv.Bootstrapped() {
			return srv, nil
		}
	}
	return state.Server{}, errors.New("no configured server to connect to: `vpncli server provision` makes one")
}
