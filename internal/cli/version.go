package cli

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version is stamped at release time with:
//
//	go build -ldflags "-X github.com/lestex/vpncli/internal/cli.version=v0.1.0"
//
// When it is empty (a plain `go build` or `go install`), buildVersion falls
// back to what the Go toolchain recorded in the binary.
var version string

func newVersionCommand() *cobra.Command {
	var short bool

	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the vpncli version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			v, rev := buildInfo()
			if short {
				fmt.Fprintln(cmd.OutOrStdout(), v)
				return nil
			}
			out := fmt.Sprintf("vpncli %s", v)
			if rev != "" {
				out += fmt.Sprintf(" (%s)", rev)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s/%s %s\n", out, runtime.GOOS, runtime.GOARCH, runtime.Version())
			return nil
		},
	}

	cmd.Flags().BoolVar(&short, "short", false, "print just the version string")

	return cmd
}

// buildInfo returns the version and, when available, the VCS revision.
func buildInfo() (v, rev string) {
	v = version

	info, ok := debug.ReadBuildInfo()
	if !ok {
		if v == "" {
			v = "dev"
		}
		return v, ""
	}

	if v == "" {
		// Set for `go install module@version`; "(devel)" for a local build.
		if bv := info.Main.Version; bv != "" && bv != "(devel)" {
			v = bv
		} else {
			v = "dev"
		}
	}

	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if len(s.Value) > 12 {
				rev = s.Value[:12]
			} else {
				rev = s.Value
			}
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if dirty && rev != "" {
		rev += "-dirty"
	}
	return v, rev
}
