package cli

import (
	"github.com/spf13/cobra"
)

// newServerCommand groups everything that acts on a server.
//
// They are one subject and they read as one: `vpncli server list`, then
// `vpncli server provision`, then `vpncli server connect`, and in time
// `vpncli server rotate`. What stays at the top level is what is not about a
// server at all - the wizard, reconciling state, asking a provider directly.
func newServerCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "server",
		Aliases: []string{"servers"},
		Short:   "Create, configure, connect to, replace and destroy servers",
		Long: `The servers themselves: what exists, and how a new one comes to exist.

    vpncli server list         what local state knows about
    vpncli server provision    create one and configure it
    vpncli server bootstrap    configure one that is not configured yet
    vpncli server connect      the link, QR or client config to reach one
    vpncli server rotate       replace one with a fresh server
    vpncli server destroy      delete one and forget it

All but ` + "`connect`" + ` talk to a provider and need a token. ` + "`connect`" + ` reads local
state and nothing else, so it works offline.`,
	}

	cmd.AddCommand(
		newBootstrapCommand(),
		newConnectCommand(),
		newDestroyCommand(),
		newListCommand(),
		newProvisionCommand(),
		newRotateCommand(),
	)
	return cmd
}
