package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lestex/vpncli/internal/config"
	"github.com/lestex/vpncli/internal/manager"
	"github.com/lestex/vpncli/internal/provider"
)

func newSyncCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "sync",
		Short: "Reconcile local state against the provider API",
		Long: `Reconcile local state against what the provider actually has.

The provider is the source of truth. Rows for servers that no longer exist are
dropped, addresses and statuses that have drifted are corrected, and servers
tagged "` + provider.ManagedTag + `" that local state has never seen are adopted.

Servers without that tag are left alone. The listing covers the whole account,
and most of what is in it may have nothing to do with vpncli.

Requires DIGITALOCEAN_TOKEN or DIGITALOCEAN_ACCESS_TOKEN to be set.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			vps, err := openProvider(cfg)
			if err != nil {
				return err
			}

			store, err := openStore()
			if err != nil {
				return err
			}
			defer store.Close()

			result, err := manager.New(vps, store).Sync(cmd.Context())
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			if err := printSyncResult(out, result); err != nil {
				return err
			}
			if !result.Changed() {
				return nil
			}

			// Show the state that was just settled on, so the summary can be
			// checked against something.
			servers, err := store.List(cmd.Context())
			if err != nil {
				return err
			}

			fmt.Fprintln(out)
			return printServers(out, servers)
		},
	}
}

// printSyncResult writes a one-line summary of what the pass changed.
func printSyncResult(w io.Writer, result manager.SyncResult) error {
	if !result.Changed() {
		_, err := fmt.Fprintf(w, "already up to date (%s)\n", count(result.Unchanged, "server"))
		return err
	}

	var parts []string
	for _, part := range []struct {
		n     int
		label string
	}{
		{len(result.Adopted), "adopted"},
		{len(result.Updated), "updated"},
		{len(result.Removed), "removed"},
		{result.Unchanged, "unchanged"},
	} {
		if part.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", part.n, part.label))
		}
	}

	_, err := fmt.Fprintln(w, strings.Join(parts, ", "))
	return err
}

// count renders "1 server" but "2 servers".
func count(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
