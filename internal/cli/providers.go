package cli

import (
	"github.com/spf13/cobra"

	"github.com/lestex/vpncli/internal/provider/digitalocean"
)

// newProvidersCommand groups the diagnostic commands that query provider
// APIs directly, with no local state involved.
func newProvidersCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "providers",
		Short: "Query cloud provider APIs directly",
	}

	cmd.AddCommand(newDigitalOceanCommand())

	return cmd
}

func newDigitalOceanCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "do",
		Aliases: []string{"digitalocean"},
		Short:   "DigitalOcean",
	}

	cmd.AddCommand(newDigitalOceanListCommand())

	return cmd
}

func newDigitalOceanListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List every droplet in the account",
		Long: `List every droplet in the DigitalOcean account, straight from the API.

This is not filtered to servers vpncli created. Seeing the account as it really
is, is the point.

Requires DIGITALOCEAN_TOKEN or DIGITALOCEAN_ACCESS_TOKEN to be set.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			token, err := digitalocean.TokenFromEnv()
			if err != nil {
				return err
			}

			do, err := digitalocean.New(token)
			if err != nil {
				return err
			}

			instances, err := do.ListInstances(cmd.Context())
			if err != nil {
				return err
			}

			return printInstances(cmd.OutOrStdout(), instances)
		},
	}
}
