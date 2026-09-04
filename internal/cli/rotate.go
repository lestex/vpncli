package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/lestex/vpncli/internal/bootstrap"
	"github.com/lestex/vpncli/internal/config"
	"github.com/lestex/vpncli/internal/manager"
	"github.com/lestex/vpncli/internal/prompt"
	"github.com/lestex/vpncli/internal/reality"
	"github.com/lestex/vpncli/internal/state"
)

func newRotateCommand() *cobra.Command {
	var yes bool

	cmd := &cobra.Command{
		Use:   "rotate <id>",
		Short: "Replace a server with a fresh one",
		Long: `Create a replacement server, configure it, and destroy the old one.

This is the workflow the whole program is shaped around. The new server has a
new address and a new REALITY keypair, and shares nothing with the one it
replaces: whatever was learned about the old server - by an observer, a
blocklist, or a log somewhere - describes something that no longer exists.

The order is deliberate. The replacement is created and confirmed to be
serving before anything is destroyed, so a rotation that fails leaves the old
server exactly where it was. That costs both servers for the couple of minutes
in between, which is a few cents.

The replacement is built from the current config, so it also picks up anything
` + "`vpncli init`" + ` has changed since - a different region, size or camouflage.

It gets a new local id, because it is a different server. Clients have to be
reconfigured either way: the address and the keys they hold are both gone.

Requires DIGITALOCEAN_TOKEN or DIGITALOCEAN_ACCESS_TOKEN to be set.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("%q is not a server id: `vpncli server list` shows them", args[0])
			}
			return runRotate(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(),
				openProvider, dialSSH, reality.Check, id, yes)
		},
	}

	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "rotate without asking")
	return cmd
}

// runRotate replaces one server with a new one.
func runRotate(ctx context.Context, in io.Reader, out io.Writer, open openFunc, dial dialFunc, check checkFunc, id int64, yes bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	opts, err := createOptions(cfg)
	if err != nil {
		return err
	}

	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	old, err := store.Get(ctx, id)
	if err != nil {
		return err
	}

	if !yes {
		ok, err := confirmRotate(ctx, in, out, old)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(out, "left alone")
			return nil
		}
	}

	vps, err := open(cfg)
	if err != nil {
		return err
	}
	if err := sameProvider(old, vps.Name()); err != nil {
		return err
	}

	fmt.Fprintf(out, "Replacing %s (%s) with %s (%s, %s) on %s...\n",
		old.Name, orDash(old.IPv4), opts.Name, opts.Size, opts.Region, vps.Name())

	m := manager.New(vps, store)

	spin := startSpinner(out, "waiting for the replacement to boot")
	replacement, err := m.Provision(ctx, opts)
	if err == nil {
		spin.say("connecting")
		err = bootstrapServer(ctx, store, cfg, replacement, dial, check, spin.say)
	}
	spin.stop()
	if err != nil {
		// The old server is still serving, which is the point of building the
		// new one first. Whatever was created is named so it can be dealt
		// with; nothing is destroyed.
		fmt.Fprintf(out, "%s is untouched and still serving.\n", old.Name)
		if replacement.ID != 0 {
			fmt.Fprintf(out, "The replacement is id %d: `vpncli server bootstrap %d` tries again, `vpncli destroy %d` gives up on it.\n",
				replacement.ID, replacement.ID, replacement.ID)
		}
		return err
	}

	// Only now, with something to rotate to.
	if _, err := m.Destroy(ctx, old.ID); err != nil {
		fmt.Fprintf(out, "The replacement is id %d and is serving.\n", replacement.ID)
		return fmt.Errorf("destroying %s: %w", old.Name, err)
	}

	fmt.Fprintf(out, "rotated in %s: %s is gone\n\n", took(spin.elapsed()), old.Name)

	replacement, err = store.Get(ctx, replacement.ID)
	if err != nil {
		return err
	}
	if err := printServers(out, []state.Server{replacement}); err != nil {
		return err
	}

	fmt.Fprintf(out, "\nServing VLESS+REALITY on %s:%d, camouflaged as %s.\n",
		replacement.IPv4, bootstrap.Port, replacement.Credentials.ServerName)
	fmt.Fprintf(out, "Its address and keys are new, so every client needs `vpncli connect %d` again.\n",
		replacement.ID)
	return nil
}

// sameProvider refuses to rotate a server the configured provider does not
// own. Provider ids only mean anything within one provider, and the destroy at
// the end would otherwise be aimed at whatever happens to carry that id here.
func sameProvider(srv state.Server, provider string) error {
	if srv.Provider != provider {
		return fmt.Errorf("%w: server %d is on %s, the configured provider is %s",
			manager.ErrWrongProvider, srv.ID, srv.Provider, provider)
	}
	return nil
}

// confirmRotate asks before spending money and destroying a server.
func confirmRotate(ctx context.Context, in io.Reader, out io.Writer, srv state.Server) (bool, error) {
	p := prompt.New(in, out)

	p.Printf("Replace %s (%s, %s, id %d)?\n", srv.Name, orDash(srv.IPv4), srv.Region, srv.ID)
	p.Printf("A new server is created and configured first; this one is destroyed only\n")
	p.Printf("once that has worked. Both are billed until then.\n")
	p.Printf("Type yes to confirm: ")

	return confirmed(ctx, p)
}
