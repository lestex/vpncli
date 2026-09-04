package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/lestex/vpncli/internal/config"
	"github.com/lestex/vpncli/internal/prompt"
	"github.com/lestex/vpncli/internal/provider"
)

// openFunc builds the provider for a config. The command passes openProvider;
// tests pass a fake, so the wizard can be walked end to end without a token or
// a network.
type openFunc func(config.Config) (provider.VPSProvider, error)

func newInitCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Configure vpncli interactively",
		Long: `Ask where servers should be created, and write the answers to config.yaml.

Provider and region are what it asks today. Size, image and REALITY camouflage
join the wizard in later versions.

Re-running it is safe: every question is offered with the current value as the
default, and settings it does not ask about are left as they are.

Listing regions is an API call, so DIGITALOCEAN_TOKEN or
DIGITALOCEAN_ACCESS_TOKEN must be set.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInit(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), openProvider)
		},
	}
}

// runInit walks the wizard and saves the result.
//
// The file is written once, at the end. A wizard abandoned halfway through
// would otherwise leave a config naming a provider and no region, which is
// harder to spot than one that was never written.
func runInit(ctx context.Context, in io.Reader, out io.Writer, open openFunc) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	path, err := config.Path()
	if err != nil {
		return err
	}

	p := prompt.New(in, out)
	p.Printf("Answers are written to %s\n\n", path)

	name, err := selectProvider(p, cfg.Provider)
	if err != nil {
		return err
	}
	cfg.Provider = name

	// The regions come from whichever provider was just chosen, so the API
	// client is built after that answer rather than before it.
	vps, err := open(cfg)
	if err != nil {
		return err
	}

	// A region slug means nothing outside the provider it came from. Taking
	// the answer rather than keeping the old value is what makes switching
	// providers produce a config that can actually create a server.
	region, err := selectRegion(ctx, p, vps, cfg.Region)
	if err != nil {
		return err
	}
	cfg.Region = region

	if err := cfg.Save(); err != nil {
		return err
	}

	p.Printf("\nWrote %s\n", path)
	p.Printf("  provider  %s\n", cfg.Provider)
	p.Printf("  region    %s\n", cfg.Region)
	p.Printf("\nSize and image selection land in the next version; until then\n")
	p.Printf("`vpncli sync` and `vpncli list` are what this config is good for.\n")

	return nil
}

// selectProvider asks which cloud to use. With one implementation there is
// nothing to ask, and saying which one was picked is enough - this becomes a
// real question as clouds are added.
func selectProvider(p *prompt.Prompter, current string) (string, error) {
	options := make([]prompt.Option, 0, len(implementations))
	for _, impl := range implementations {
		options = append(options, prompt.Option{Key: impl.name, Label: impl.label})
	}

	if len(options) == 1 {
		p.Printf("Provider: %s (%s), the only one implemented\n", options[0].Label, options[0].Key)
		return options[0].Key, nil
	}

	i, err := p.Select("Provider", options, current)
	if err != nil {
		return "", err
	}
	return options[i].Key, nil
}

// selectRegion asks where servers should live.
func selectRegion(ctx context.Context, p *prompt.Prompter, vps provider.VPSProvider, current string) (string, error) {
	p.Printf("\nFetching regions from %s...\n\n", vps.Name())

	regions, err := vps.ListRegions(ctx)
	if err != nil {
		return "", err
	}

	options := make([]prompt.Option, 0, len(regions))
	for _, region := range regions {
		// A region that takes no new servers would only produce a config that
		// fails at provision time.
		if !region.Available {
			continue
		}
		options = append(options, prompt.Option{Key: region.Slug, Label: region.Name})
	}
	if len(options) == 0 {
		return "", fmt.Errorf("%s has no regions available to this account", vps.Name())
	}

	p.Printf("Pick one close to you. Latency is the one cost a VPN cannot make back.\n\n")

	i, err := p.Select("Region", options, current)
	if err != nil {
		return "", err
	}
	return options[i].Key, nil
}
