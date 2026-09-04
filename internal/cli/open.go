package cli

import (
	"fmt"
	"strings"

	"github.com/lestex/vpncli/internal/config"
	"github.com/lestex/vpncli/internal/provider"
	"github.com/lestex/vpncli/internal/provider/digitalocean"
	"github.com/lestex/vpncli/internal/state"
)

// implementation is one cloud the CLI can talk to. The label is what the
// wizard shows; the name is what goes in the config file and in state.
type implementation struct {
	name  string
	label string
	open  func() (provider.VPSProvider, error)
}

// implementations are the providers that exist today, in the order the wizard
// offers them. Adding a cloud is a line here plus the package it names.
var implementations = []implementation{
	{
		name:  digitalocean.Name,
		label: "DigitalOcean",
		open: func() (provider.VPSProvider, error) {
			token, err := digitalocean.TokenFromEnv()
			if err != nil {
				return nil, err
			}
			return digitalocean.New(token)
		},
	},
}

// openStore opens the state database at its configured path, creating it on
// first use.
func openStore() (*state.Store, error) {
	path, err := config.DatabasePath()
	if err != nil {
		return nil, err
	}
	return state.Open(path)
}

// openProvider builds the provider named in the config file. An empty field
// means `vpncli providers init` has not been run, which is a normal state rather than an
// error: it falls back to the first implementation.
func openProvider(cfg config.Config) (provider.VPSProvider, error) {
	name := cfg.Provider
	if name == "" {
		name = implementations[0].name
	}

	for _, impl := range implementations {
		if impl.name == name {
			return impl.open()
		}
	}

	return nil, fmt.Errorf("provider %q is configured but not implemented yet: only %s", name, implementedNames())
}

// implementedNames lists the provider slugs for an error message.
func implementedNames() string {
	names := make([]string, 0, len(implementations))
	for _, impl := range implementations {
		names = append(names, fmt.Sprintf("%q", impl.name))
	}
	return strings.Join(names, ", ")
}
