package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lestex/vpncli/internal/bootstrap"
	"github.com/lestex/vpncli/internal/config"
	"github.com/lestex/vpncli/internal/manager"
	"github.com/lestex/vpncli/internal/provider"
	"github.com/lestex/vpncli/internal/reality"
	"github.com/lestex/vpncli/internal/state"
)

func newProvisionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "provision",
		Short: "Create a server from the configured answers",
		Long: `Create a server using what ` + "`vpncli init`" + ` wrote, and wait for it to boot.

The row is recorded as soon as the provider accepts the request, before the
wait: a server that exists but is not in state is invisible and still billed.
So an interrupted wait leaves something ` + "`vpncli destroy`" + ` can clean up, and
` + "`vpncli sync`" + ` picks it up on any machine.

The server comes up as a stock OS image with the configured SSH key installed.
Installing Xray-core and the REALITY camouflage is v0.8.0.

Requires DIGITALOCEAN_TOKEN or DIGITALOCEAN_ACCESS_TOKEN to be set.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runProvision(cmd.Context(), cmd.OutOrStdout(), openProvider, dialSSH, reality.Check)
		},
	}
}

// runProvision creates one server and reports it. The provider is opened
// through a func so a test can walk the whole path without a token, which is
// the only way to exercise what happens when a create half succeeds.
func runProvision(ctx context.Context, out io.Writer, open openFunc, dial dialFunc, check checkFunc) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	opts, err := createOptions(cfg)
	if err != nil {
		return err
	}

	vps, err := open(cfg)
	if err != nil {
		return err
	}

	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	fmt.Fprintf(out, "Creating %s (%s, %s) on %s...\n", opts.Name, opts.Size, opts.Region, vps.Name())

	// Booting takes a minute or so, and a command that prints nothing for a
	// minute looks like one that has hung.
	spin := startSpinner(out, "waiting for the server to boot")
	srv, err := manager.New(vps, store).Provision(ctx, opts)
	spin.stop()

	if err != nil {
		// A wait that failed still leaves a server running and recorded. Its
		// id is how it gets cleaned up, and saying so here is cheaper than
		// finding out from a bill.
		if srv.ID != 0 {
			fmt.Fprintf(out, "the server was created and recorded as id %d: `vpncli destroy %d` removes it\n", srv.ID, srv.ID)
		}
		return err
	}

	// The server exists; now it has to be made into something worth having.
	// Its row is already written, so a bootstrap that fails leaves a server
	// `vpncli bootstrap` can finish rather than one to throw away.
	spin = startSpinner(out, "connecting")
	err = bootstrapServer(ctx, store, cfg, srv, dial, check, spin.say)
	spin.stop()
	if err != nil {
		fmt.Fprintf(out, "the server is up but not configured: `vpncli bootstrap %d` tries again\n", srv.ID)
		return err
	}

	fmt.Fprintf(out, "ready in %s\n\n", took(spin.elapsed()))
	if err := printServers(out, []state.Server{srv}); err != nil {
		return err
	}

	fmt.Fprintf(out, "\nServing VLESS+REALITY on %s:%d, camouflaged as %s.\n",
		srv.IPv4, bootstrap.Port, cfg.Reality.Host())
	return nil
}

// createOptions turns the config into one create request, naming whatever the
// wizard has not answered yet. The check is here rather than in the provider
// so the message can name the command that fills the gap.
func createOptions(cfg config.Config) (provider.CreateOptions, error) {
	var missing []string
	for _, field := range []struct {
		name  string
		empty bool
	}{
		{"region", cfg.Region == ""},
		{"size", cfg.Size == ""},
		{"image", cfg.Image == ""},
		{"ssh key", len(cfg.SSHKeyIDs) == 0},
		{"camouflage", cfg.Reality.Host() == ""},
	} {
		if field.empty {
			missing = append(missing, field.name)
		}
	}
	if len(missing) > 0 {
		return provider.CreateOptions{}, fmt.Errorf("config has no %s: run `vpncli init`", strings.Join(missing, ", "))
	}

	name, err := serverName(cfg.Region)
	if err != nil {
		return provider.CreateOptions{}, err
	}

	return provider.CreateOptions{
		Name:      name,
		Region:    cfg.Region,
		Size:      cfg.Size,
		Image:     cfg.Image,
		SSHKeyIDs: cfg.SSHKeyIDs,
	}, nil
}

// serverName builds a name that says where the server is and stays unique.
// Names are not identifiers here - state and the provider both key on IDs - so
// the suffix only has to keep two servers in one region apart in a listing.
func serverName(region string) (string, error) {
	var suffix [3]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("generating a server name: %w", err)
	}
	return fmt.Sprintf("%s-%s-%s", provider.ManagedTag, region, hex.EncodeToString(suffix[:])), nil
}
