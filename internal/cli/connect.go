package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/lestex/vpncli/internal/client"
)

func newConnectCommand() *cobra.Command {
	var (
		asQR      bool
		asSingBox bool
	)

	cmd := &cobra.Command{
		Use:   "connect <id>",
		Short: "Print what a client needs to reach a server",
		Long: `Build a client config from a server's stored credentials.

By default it prints the ` + "`vless://`" + ` link that every current client imports,
one line and nothing else, so it can be piped somewhere useful:

    vpncli connect 3 | pbcopy

` + "`--qr`" + ` draws the same link as a QR code, which is how it gets onto a phone
without going through anything that keeps a copy.

` + "`--sing-box`" + ` writes a sing-box config instead: a SOCKS and HTTP proxy on
127.0.0.1:` + strconv.Itoa(client.SocksPort) + `, which needs no privileges and is what a browser wants.

No API call and no SSH: everything here was recorded when the server was
bootstrapped, so this works offline and needs no token.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("%q is not a server id: `vpncli list` shows them", args[0])
			}
			if asQR && asSingBox {
				return fmt.Errorf("--qr and --sing-box are two different things to print: pick one")
			}
			return runConnect(cmd.Context(), cmd.OutOrStdout(), id, asQR, asSingBox)
		},
	}

	cmd.Flags().BoolVar(&asQR, "qr", false, "draw the link as a QR code")
	cmd.Flags().BoolVar(&asSingBox, "sing-box", false, "print a sing-box config")
	return cmd
}

// runConnect prints one server's client config.
func runConnect(ctx context.Context, out io.Writer, id int64, asQR, asSingBox bool) error {
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	srv, err := store.Get(ctx, id)
	if err != nil {
		return err
	}

	if asSingBox {
		config, err := client.SingBox(srv)
		if err != nil {
			return err
		}
		_, err = out.Write(config)
		return err
	}

	uri, err := client.URI(srv)
	if err != nil {
		return err
	}

	if !asQR {
		// Alone on stdout, so `vpncli connect 3 | pbcopy` copies a link and
		// not a paragraph about one.
		_, err := fmt.Fprintln(out, uri)
		return err
	}

	if err := printQR(out, uri); err != nil {
		return err
	}
	// The link goes under the code: a scanner that will not focus is common
	// enough that having something to copy instead is worth two lines.
	fmt.Fprintf(out, "\n%s\n", uri)
	fmt.Fprintf(out, "\n%s, camouflaged as %s. The SNI has to match exactly.\n",
		srv.Name, srv.Credentials.ServerName)
	return nil
}
