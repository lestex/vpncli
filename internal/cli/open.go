package cli

import (
	"fmt"

	"github.com/lestex/vpncli/internal/config"
	"github.com/lestex/vpncli/internal/provider"
	"github.com/lestex/vpncli/internal/provider/digitalocean"
	"github.com/lestex/vpncli/internal/state"
)

// openStore opens the state database at its configured path, creating it on
// first use.
func openStore() (*state.Store, error) {
	path, err := config.DatabasePath()
	if err != nil {
		return nil, err
	}
	return state.Open(path)
}

// openProvider builds the provider named in the config file. Until the wizard
// lands in v0.5.0 nothing writes that field, so an empty one means the only
// implementation there is rather than an error.
func openProvider(cfg config.Config) (provider.VPSProvider, error) {
	name := cfg.Provider
	if name == "" {
		name = digitalocean.Name
	}

	if name != digitalocean.Name {
		return nil, fmt.Errorf("provider %q is configured but not implemented yet: only %q is", name, digitalocean.Name)
	}

	token, err := digitalocean.TokenFromEnv()
	if err != nil {
		return nil, err
	}
	return digitalocean.New(token)
}
