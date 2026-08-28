package cli

import (
	"github.com/spf13/cobra"
)

func newListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the servers vpncli knows about",
		Long: `List servers from local state.

No provider API is called, so this is instant, works offline, and needs no API
token. The trade is that it can be stale: a server created or destroyed
somewhere else shows up only after ` + "`vpncli sync`" + `.

The ID column is the short local id - the one other commands take.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := openStore()
			if err != nil {
				return err
			}
			defer store.Close()

			servers, err := store.List(cmd.Context())
			if err != nil {
				return err
			}

			return printServers(cmd.OutOrStdout(), servers)
		},
	}
}
