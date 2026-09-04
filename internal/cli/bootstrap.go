package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/lestex/vpncli/internal/bootstrap"
	"github.com/lestex/vpncli/internal/config"
	"github.com/lestex/vpncli/internal/reality"
	"github.com/lestex/vpncli/internal/ssh"
	"github.com/lestex/vpncli/internal/state"
)

// dialFunc opens the SSH connection a bootstrap runs over. The command passes
// ssh.Dial; tests pass a fake, so the whole path can be walked without a
// server to connect to.
type dialFunc func(context.Context, ssh.Config) (bootstrapClient, error)

// bootstrapClient is what the bootstrap needs of a connection, plus the host
// key, which is what gets pinned.
type bootstrapClient interface {
	bootstrap.Runner
	HostKey() string
	Close() error
}

// dialSSH is the real connection.
func dialSSH(ctx context.Context, cfg ssh.Config) (bootstrapClient, error) {
	return ssh.Dial(ctx, cfg)
}

func newBootstrapCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "bootstrap <id>",
		Short: "Configure a server that is not configured yet",
		Long: `Install and configure VLESS+REALITY on a server that already exists.

` + "`vpncli server provision`" + ` does this itself. This command is for the server whose
bootstrap failed halfway - a network that dropped, an apt mirror having a bad
day - where the server is fine and only the configuring needs another go.

Re-running it is safe, and it is the only way to fix a half-configured server:
fresh key material is generated and replaces whatever reached the server, so
there is never a half-written set of keys to reconcile. The old material stops
working, which is the same thing a rotation does.

Nothing is put in cloud-init user data. Provider metadata is readable from
inside the server and logged by the provider, so the keys go over SSH instead.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("%q is not a server id: `vpncli server list` shows them", args[0])
			}
			return runBootstrapCommand(cmd.Context(), cmd.OutOrStdout(), dialSSH, reality.Check, id)
		},
	}
}

// runBootstrapCommand configures one server by local id.
func runBootstrapCommand(ctx context.Context, out io.Writer, dial dialFunc, check checkFunc, id int64) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	srv, err := store.Get(ctx, id)
	if err != nil {
		return err
	}
	if srv.IPv4 == "" {
		return fmt.Errorf("server %d has no address yet: `vpncli sync` picks one up once it has booted", srv.ID)
	}

	fmt.Fprintf(out, "Configuring %s (%s)...\n", srv.Name, srv.IPv4)

	spin := startSpinner(out, "connecting")
	err = bootstrapServer(ctx, store, cfg, srv, dial, check, spin.say)
	spin.stop()
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "ready in %s\n", took(spin.elapsed()))
	fmt.Fprintf(out, "\n%s is serving VLESS+REALITY on %s:%d, camouflaged as %s.\n",
		srv.Name, srv.IPv4, bootstrap.Port, cfg.Reality.Host())
	fmt.Fprintf(out, "`vpncli server connect %d` prints the link to reach it with.\n", srv.ID)
	return nil
}

// bootstrapServer connects to a server, configures it, and records what it was
// given. It is shared by `vpncli server provision` and `vpncli server bootstrap`.
//
// The credentials are written only after the server is actually serving. A run
// that failed halfway leaves the row unconfigured, which is exactly right: the
// next attempt generates fresh material rather than trying to work out what
// reached the server and what did not.
func bootstrapServer(ctx context.Context, store *state.Store, cfg config.Config, srv state.Server, dial dialFunc, check checkFunc, progress bootstrap.Progress) error {
	host := cfg.Reality.Host()
	if host == "" {
		return fmt.Errorf("config has no camouflage: run `vpncli providers init`")
	}

	// The wizard checks this too, but a config can be edited by hand and a
	// site can grow a longer certificate between one server and the next. A
	// server built on a site REALITY cannot relay looks perfectly healthy and
	// refuses every client, so it is worth one TLS connection to find out now.
	if _, err := check(ctx, host); errors.Is(err, reality.ErrUnsuitable) {
		return fmt.Errorf("%w: run `vpncli providers init` and pick another", err)
	}

	client, err := dial(ctx, ssh.Config{
		Host:         srv.IPv4,
		KeyPath:      cfg.SSHKeyPath,
		KnownHostKey: srv.SSHHostKey,
	})
	if err != nil {
		return err
	}
	defer client.Close()

	// Trust on first use: a server created a minute ago has no key anybody
	// could have known in advance, so the first connection records what it saw
	// and every later one has to match it.
	if srv.SSHHostKey == "" {
		if err := store.SaveHostKey(ctx, srv.ID, client.HostKey()); err != nil {
			return err
		}
	}

	material, err := reality.New()
	if err != nil {
		return err
	}

	opts := bootstrap.Options{Material: material, Dest: cfg.Reality.Dest, ServerName: host}
	if err := bootstrap.Run(ctx, client, opts, progress); err != nil {
		return err
	}

	return store.SaveBootstrap(ctx, srv.ID, state.Credentials{
		UUID:       material.UUID,
		PrivateKey: material.PrivateKey,
		PublicKey:  material.PublicKey,
		ShortID:    material.ShortID,
		Dest:       opts.Dest,
		ServerName: opts.ServerName,
	})
}
