package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/lestex/vpncli/internal/client"
)

func newConnectCommand() *cobra.Command {
	var (
		asQR      bool
		asSingBox bool
		asTun     bool
		out       string
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
Only what is pointed at it goes through the tunnel.

Add ` + "`--tun`" + ` for the whole machine instead. That config creates a network
interface and routes everything through it, including programs that have no
proxy setting, and DNS with it. It has to run as root.

` + "`-o`" + ` writes to a file rather than standard output, and is the better way to
keep one: the file is created 0600, because a client config carries the key to
your server, and the command then says exactly how to run it. Redirecting with
` + "`>`" + ` leaves it world readable, and leaves an old config in place under a name
that no longer describes it - which is a confusing way to find out that
nothing is being tunneled.

    vpncli connect 3 --tun -o ~/vpn.json

No API call and no SSH: everything here was recorded when the server was
bootstrapped, so this works offline and needs no token.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("%q is not a server id: `vpncli server list` shows them", args[0])
			}
			if asQR && (asSingBox || asTun) {
				return fmt.Errorf("--qr and --sing-box are two different things to print: pick one")
			}
			mode := client.Proxy
			if asTun {
				// --tun is only meaningful for a sing-box config, so it says
				// which one to write rather than needing both flags.
				asSingBox, mode = true, client.Tun
			}
			return runConnect(cmd.Context(), cmd.OutOrStdout(), id, asQR, asSingBox, mode, out)
		},
	}

	cmd.Flags().BoolVar(&asQR, "qr", false, "draw the link as a QR code")
	cmd.Flags().BoolVar(&asSingBox, "sing-box", false, "print a sing-box config (a loopback proxy)")
	cmd.Flags().BoolVar(&asTun, "tun", false, "make that config route the whole machine, as root")
	cmd.Flags().StringVarP(&out, "out", "o", "", "write to this file (0600) instead of standard output")
	return cmd
}

// runConnect prints one server's client config.
func runConnect(ctx context.Context, out io.Writer, id int64, asQR, asSingBox bool, mode client.Mode, path string) error {
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
		config, err := client.SingBox(srv, mode)
		if err != nil {
			return err
		}
		if path == "" {
			_, err = out.Write(config)
			return err
		}
		return writeConfig(out, path, config, mode)
	}

	uri, err := client.URI(srv)
	if err != nil {
		return err
	}

	if path != "" {
		return writeConfig(out, path, []byte(uri+"\n"), mode)
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

// writeConfig saves a client config and says how to use it.
//
// The mode is only known here, and it is the difference between a command that
// works and one that silently does nothing - a tun config run without root
// creates no interface, and a proxy config run with it still proxies nothing
// until something is pointed at it.
func writeConfig(out io.Writer, path string, content []byte, mode client.Mode) error {
	// 0600 because this file is the key to the server. Shell redirection would
	// have left it readable by anyone with an account here.
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}

	fmt.Fprintf(out, "wrote %s\n", path)
	if mode == client.Tun {
		fmt.Fprintf(out, "  sudo sing-box run -c %s\n", path)
		fmt.Fprintf(out, "\nIt routes the whole machine, so root is not optional: without it no\n")
		fmt.Fprintf(out, "interface is created and nothing is tunneled.\n")
		return nil
	}
	fmt.Fprintf(out, "  sing-box run -c %s\n", path)
	fmt.Fprintf(out, "\nThat is a proxy on 127.0.0.1:%d. Only what is pointed at it goes through\n", client.SocksPort)
	fmt.Fprintf(out, "the tunnel; `--tun` covers the whole machine instead.\n")
	return nil
}
