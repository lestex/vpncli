package cli

import (
	"github.com/spf13/cobra"

	"github.com/lestex/vpncli/internal/provider/digitalocean"
)

// newProvidersCommand groups what is about a cloud account rather than about
// a server: choosing one and what to create there, and looking at what it
// already holds.
func newProvidersCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "providers",
		Short: "Choose a provider, and query one directly",
		Long: `Which cloud servers are created on, and what is in the account.

    vpncli providers init    the wizard: provider, region, size, image, key
    vpncli providers do      every droplet in the DigitalOcean account

Neither touches a server. The wizard writes config.yaml, which is what
` + "`vpncli server provision`" + ` reads.`,
	}

	cmd.AddCommand(
		newDigitalOceanCommand(),
		newInitCommand(),
	)

	return cmd
}

// newDigitalOceanCommand lists the account, and is the whole of what there is
// to ask a provider directly. There is no subcommand under it because there is
// nothing else to say: everything that acts on a server goes through
// `vpncli server`, which has local state to work from.
func newDigitalOceanCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "do",
		Aliases: []string{"digitalocean"},
		Short:   "List every droplet in the DigitalOcean account",
		Long: `List every droplet in the DigitalOcean account, straight from the API.

This is not filtered to servers vpncli created. Seeing the account as it really
is, is the point: it is how a token is confirmed to work, and how a server
nobody is tracking turns up.

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
