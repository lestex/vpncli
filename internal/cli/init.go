package cli

import (
	"context"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"

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
		Long: `Ask what servers should be created, and write the answers to config.yaml.

Provider, region, size and image are what it asks today. REALITY camouflage
joins the wizard in the next version.

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

	// Which sizes exist depends on the region, so this question has to come
	// after that one.
	size, err := selectSize(ctx, p, vps, cfg.Region, cfg.Size)
	if err != nil {
		return err
	}
	cfg.Size = size

	image, err := selectImage(ctx, p, vps, cfg.Image)
	if err != nil {
		return err
	}
	cfg.Image = image

	if err := cfg.Save(); err != nil {
		return err
	}

	p.Printf("\nWrote %s\n", path)
	p.Printf("  provider  %s\n", cfg.Provider)
	p.Printf("  region    %s\n", cfg.Region)
	p.Printf("  size      %s\n", cfg.Size)
	p.Printf("  image     %s\n", cfg.Image)
	p.Printf("\nThat is everything a server needs except its camouflage, which the\n")
	p.Printf("next version asks for and `vpncli provision` will use.\n")

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

// selectRegion asks where servers should live. There is no invented default
// here: which datacenter is closest is the one thing the wizard cannot guess,
// and the alphabetically first region is no better an answer than any other.
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

// sizesShown caps the size menu. An account can create some seventy sizes and
// a VPN can use almost none of them, so the list is the cheapest few rather
// than all of them.
const sizesShown = 8

// supportedDistributions are the images the bootstrap knows how to configure,
// in the order they are offered. It is apt, nginx, ufw and a BBR sysctl, so
// offering Fedora or Rocky would only produce a server that cannot be
// finished. Ubuntu leads because that is what the bootstrap is developed
// against.
var supportedDistributions = []string{"Ubuntu", "Debian"}

// selectSize asks how big a server to create.
func selectSize(ctx context.Context, p *prompt.Prompter, vps provider.VPSProvider, region, current string) (string, error) {
	p.Printf("\nFetching sizes for %s...\n\n", region)

	sizes, err := vps.ListSizes(ctx)
	if err != nil {
		return "", err
	}

	options := make([]prompt.Option, 0, sizesShown+1)
	for _, size := range sizes {
		if !size.Available || !slices.Contains(size.Regions, region) {
			continue
		}
		// Past the cheapest few is a long list of machines nobody would put a
		// tunnel on. Whatever is already configured stays on the menu whatever
		// it costs, so re-running the wizard cannot quietly take away a size
		// that was chosen deliberately.
		if len(options) >= sizesShown && size.Slug != current {
			continue
		}
		options = append(options, prompt.Option{Key: size.Slug, Label: describeSize(size)})
	}
	if len(options) == 0 {
		return "", fmt.Errorf("%s has no sizes available in %s", vps.Name(), region)
	}

	p.Printf("A tunnel is network-bound, not CPU-bound. The cheapest size is the\n")
	p.Printf("right answer far more often than not.\n\n")

	i, err := p.Select("Size", options, defaultOf(current, options))
	if err != nil {
		return "", err
	}
	return options[i].Key, nil
}

// selectImage asks which OS to install.
func selectImage(ctx context.Context, p *prompt.Prompter, vps provider.VPSProvider, current string) (string, error) {
	p.Printf("\nFetching images...\n\n")

	images, err := vps.ListImages(ctx)
	if err != nil {
		return "", err
	}

	var options []prompt.Option
	for _, distribution := range supportedDistributions {
		for _, image := range images {
			// An image with no slug cannot be named in a create request.
			if image.Distribution != distribution || image.Slug == "" {
				continue
			}
			options = append(options, prompt.Option{Key: image.Slug, Label: describeImage(image)})
		}
	}
	if len(options) == 0 {
		return "", fmt.Errorf("%s offers no %s image", vps.Name(), strings.Join(supportedDistributions, " or "))
	}

	i, err := p.Select("Image", options, defaultOf(current, options))
	if err != nil {
		return "", err
	}
	return options[i].Key, nil
}

// describeSize renders a size as what it costs and what that buys. The tabs
// are columns: the menu aligns them along with everything else on the line.
func describeSize(size provider.Size) string {
	return fmt.Sprintf("%s\t%s RAM\t%d vCPU\t%dGB disk",
		price(size.PriceMonthly), memory(size.MemoryMB), size.VCPUs, size.DiskGB)
}

// describeImage names the distribution, since an image name on its own is
// often just a version number.
func describeImage(image provider.Image) string {
	return strings.TrimSpace(image.Distribution + " " + image.Name)
}

// price renders a monthly price, keeping the cents only when there are any.
func price(monthly float64) string {
	if monthly == math.Trunc(monthly) {
		return fmt.Sprintf("$%.0f/mo", monthly)
	}
	return fmt.Sprintf("$%.2f/mo", monthly)
}

// memory renders megabytes the way the size slugs do.
func memory(mb int) string {
	if mb < 1024 || mb%1024 != 0 {
		return fmt.Sprintf("%dMB", mb)
	}
	return fmt.Sprintf("%dGB", mb/1024)
}

// defaultOf is what an empty answer means: whatever is already configured, or
// else the first option. Both lists lead with the sensible choice - cheapest
// size, newest Ubuntu - so a first run can be walked through on the Enter key
// once the region is answered.
func defaultOf(current string, options []prompt.Option) string {
	if current != "" {
		return current
	}
	return options[0].Key
}
