package cli

import (
	"github.com/spf13/cobra"
)

// newServerCommand groups the commands that make and inspect servers.
//
// They are one subject and they read as one: `vpncli server list`, then
// `vpncli server provision`. What stays at the top level is the rest of the
// workflow, which is about a server you already have rather than about the
// fleet - connecting to one, replacing one, getting rid of one.
func newServerCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "server",
		Aliases: []string{"servers"},
		Short:   "Create, configure and list servers",
		Long: `The servers themselves: what exists, and how a new one comes to exist.

    vpncli server list         what local state knows about
    vpncli server provision    create one and configure it
    vpncli server bootstrap    configure one that is not configured yet

Connecting to a server, replacing one and destroying one are their own
commands, because they are about one server rather than about the set.`,
	}

	cmd.AddCommand(
		newBootstrapCommand(),
		newListCommand(),
		newProvisionCommand(),
	)
	return cmd
}
