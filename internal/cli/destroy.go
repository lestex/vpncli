package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lestex/vpncli/internal/config"
	"github.com/lestex/vpncli/internal/manager"
	"github.com/lestex/vpncli/internal/state"
)

func newDestroyCommand() *cobra.Command {
	var yes bool

	cmd := &cobra.Command{
		Use:   "destroy <id>",
		Short: "Destroy a server and forget it",
		Long: `Delete a server at the provider, then drop its row from local state.

The id is the short local one from ` + "`vpncli list`" + `, not the provider's.

The provider goes first. A server already gone there is not an error - the row
is exactly what needs clearing - but a delete that genuinely fails leaves the
row alone, because a server nothing knows about is one that bills forever.

Requires DIGITALOCEAN_TOKEN or DIGITALOCEAN_ACCESS_TOKEN to be set.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("%q is not a server id: `vpncli list` shows them", args[0])
			}
			return runDestroy(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), openProvider, id, yes)
		},
	}

	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "destroy without asking")
	return cmd
}

// runDestroy deletes one server, asking first unless yes.
func runDestroy(ctx context.Context, in io.Reader, out io.Writer, open openFunc, id int64, yes bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	// Read the row before touching the API, so the confirmation names the
	// server rather than a number the user has to trust.
	srv, err := store.Get(ctx, id)
	if err != nil {
		return err
	}

	if !yes {
		ok, err := confirmDestroy(in, out, srv)
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

	if _, err := manager.New(vps, store).Destroy(ctx, id); err != nil {
		return err
	}

	fmt.Fprintf(out, "destroyed %s (%s)\n", srv.Name, orDash(srv.IPv4))
	return nil
}

// confirmDestroy asks before something irreversible. Anything but an explicit
// yes is a no, including an input that ended: a script piping nothing into
// destroy has not agreed to anything.
func confirmDestroy(in io.Reader, out io.Writer, srv state.Server) (bool, error) {
	fmt.Fprintf(out, "Destroy %s (%s, %s, id %d)? Its IP and keys are gone for good.\n",
		srv.Name, orDash(srv.IPv4), srv.Region, srv.ID)
	fmt.Fprint(out, "Type yes to confirm: ")

	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("reading answer: %w", err)
	}
	if err != nil {
		fmt.Fprintln(out)
	}

	return strings.EqualFold(strings.TrimSpace(line), "yes"), nil
}
