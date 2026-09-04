package cli

import (
	"github.com/spf13/cobra"
)

// newServerCommand groups everything that acts on servers themselves.
//
// They are one subject and they read as one: `vpncli server list`, then
// `vpncli server provision`, and in time `vpncli server rotate`. What stays at
// the top level is what is not about a server's existence - connecting to one,
// reconciling state, asking a provider directly.
func newServerCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "server",
		Aliases: []string{"servers"},
		Short:   "Create, configure, replace and destroy servers",
		Long: `The servers themselves: what exists, and how a new one comes to exist.

    vpncli server list         what local state knows about
    vpncli server provision    create one and configure it
    vpncli server bootstrap    configure one that is not configured yet
    vpncli server rotate       replace one with a fresh server
    vpncli server destroy      delete one and forget it

Connecting to a server is ` + "`vpncli connect`" + `, which is about using one
rather than about its existence, and needs neither a token nor a network.`,
	}

	cmd.AddCommand(
		newBootstrapCommand(),
		newDestroyCommand(),
		newListCommand(),
		newProvisionCommand(),
		newRotateCommand(),
	)
	return cmd
}
