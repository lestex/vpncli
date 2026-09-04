// Package cli wires up the Cobra command tree.
package cli

import (
	"github.com/spf13/cobra"
)

// NewRootCommand builds the command tree. Subcommands are registered here as
// each version lands.
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "vpncli",
		Short: "Provision and rotate self-hosted VLESS+REALITY VPN servers",
		Long: `vpncli provisions single-user VPN servers on cloud VPS providers and
configures them with VLESS+REALITY (Xray-core).

Servers are connected to directly by IP - no domain and no CDN - which is what
lets a server be destroyed and replaced with a fresh IP and fresh keys at any
time.`,
		SilenceUsage:  true, // usage on a runtime error is just noise
		SilenceErrors: true, // main prints the error itself, once
	}

	root.AddCommand(
		newInitCommand(),
		newProvidersCommand(),
		newServerCommand(),
		newSyncCommand(),
		newVersionCommand(),
	)

	return root
}
