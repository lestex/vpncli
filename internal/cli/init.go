package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lestex/vpncli/internal/config"
	"github.com/lestex/vpncli/internal/prompt"
	"github.com/lestex/vpncli/internal/provider"
	"github.com/lestex/vpncli/internal/reality"
)

// openFunc builds the provider for a config. The command passes openProvider;
// tests pass a fake, so the wizard can be walked end to end without a token or
// a network.
type openFunc func(config.Config) (provider.VPSProvider, error)

// checkFunc inspects a camouflage site. The commands pass reality.Check;
// tests pass a fake, because a test that reaches the internet to decide
// whether it passes is a test that fails on a train.
type checkFunc func(ctx context.Context, host string) (reality.Camouflage, error)

func newInitCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Configure vpncli interactively",
		Long: `Ask what servers should be created, and write the answers to config.yaml.

Provider, region, size, image, SSH key and REALITY camouflage - which together
are everything ` + "`vpncli server provision`" + ` needs.

Re-running it is safe: every question is offered with the current value as the
default, and settings it does not ask about are left as they are.

The menus are API calls, so DIGITALOCEAN_TOKEN or DIGITALOCEAN_ACCESS_TOKEN
must be set.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInit(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), openProvider, reality.Check)
		},
	}
}

// runInit walks the wizard and saves the result.
//
// The file is written once, at the end. A wizard abandoned halfway through
// would otherwise leave a config naming a provider and no region, which is
// harder to spot than one that was never written.
func runInit(ctx context.Context, in io.Reader, out io.Writer, open openFunc, check checkFunc) error {
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

	name, err := selectProvider(ctx, p, cfg.Provider)
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

	keys, keyName, err := selectSSHKeys(ctx, p, vps, cfg.SSHKeyIDs)
	if err != nil {
		return err
	}
	cfg.SSHKeyIDs = keys

	keyPath, err := askSSHKeyPath(ctx, p, cfg.SSHKeyPath, keyName)
	if err != nil {
		return err
	}
	cfg.SSHKeyPath = keyPath

	// Camouflage is the one question with no API behind it, so it is asked
	// last: everything that can fail on a token has already failed by here.
	host, err := selectCamouflage(ctx, p, cfg.Reality.Host(), check)
	if err != nil {
		return err
	}
	cfg.Reality = config.Camouflage(host)

	if err := cfg.Save(); err != nil {
		return err
	}

	p.Printf("\nWrote %s\n", path)
	p.Printf("  provider   %s\n", cfg.Provider)
	p.Printf("  region     %s\n", cfg.Region)
	p.Printf("  size       %s\n", cfg.Size)
	p.Printf("  image      %s\n", cfg.Image)
	p.Printf("  ssh key    %s\n", sshKeySummary(keyName, cfg.SSHKeyIDs))
	p.Printf("  key file   %s\n", cfg.SSHKeyPath)
	p.Printf("  camouflage %s\n", cfg.Reality.Dest)
	p.Printf("\nThat is everything a server needs. Create one with `vpncli server provision`.\n")

	return nil
}

// selectProvider asks which cloud to use. With one implementation there is
// nothing to ask, and saying which one was picked is enough - this becomes a
// real question as clouds are added.
func selectProvider(ctx context.Context, p *prompt.Prompter, current string) (string, error) {
	options := make([]prompt.Option, 0, len(implementations))
	for _, impl := range implementations {
		options = append(options, prompt.Option{Key: impl.name, Label: impl.label})
	}

	if len(options) == 1 {
		p.Printf("Provider: %s (%s), the only one implemented\n", options[0].Label, options[0].Key)
		return options[0].Key, nil
	}

	i, err := p.Select(ctx, "Provider", options, current)
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

	i, err := p.Select(ctx, "Region", options, current)
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

	i, err := p.Select(ctx, "Size", options, defaultOf(current, options))
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

	i, err := p.Select(ctx, "Image", options, defaultOf(current, options))
	if err != nil {
		return "", err
	}
	return options[i].Key, nil
}

// selectSSHKeys asks which registered key gets installed for root.
//
// One key is asked for and a list is stored, because that is what the create
// call takes and a config edited by hand may well name several. An account
// with no key is a dead end rather than a question, so it says how to fix it.
func selectSSHKeys(ctx context.Context, p *prompt.Prompter, vps provider.VPSProvider, current []string) ([]string, string, error) {
	p.Printf("\nFetching SSH keys...\n\n")

	keys, err := vps.ListSSHKeys(ctx)
	if err != nil {
		return nil, "", err
	}
	if len(keys) == 0 {
		return nil, "", fmt.Errorf("%s has no SSH keys registered: add one first, or a new server arrives with a mailed root password and no way in", vps.Name())
	}

	// Keys are answered by name rather than by the provider-side id, which is
	// a number nobody chose and nothing recognizes. The fingerprint is there to
	// tell two keys of the same name apart.
	options := make([]prompt.Option, 0, len(keys))
	for _, key := range keys {
		options = append(options, prompt.Option{Key: key.Name, Label: key.Fingerprint})
	}

	p.Printf("This is the key the bootstrap logs in with. Pick one whose private\n")
	p.Printf("half is on this machine.\n\n")

	i, err := p.Select(ctx, "SSH key", options, defaultOf(configuredKeyName(keys, current), options))
	if err != nil {
		return nil, "", err
	}
	// Answering with a key that is already configured is not a request to drop
	// the others a hand-written list may hold.
	if slices.Contains(current, keys[i].ID) {
		return current, keys[i].Name, nil
	}
	return []string{keys[i].ID}, keys[i].Name, nil
}

// sshKeySummary names what was written. The id is what the config holds and
// the name is what was answered, so one key shows both; a list kept from an
// existing config is only ids, since only one of them was named here.
func sshKeySummary(name string, ids []string) string {
	if len(ids) == 1 {
		return fmt.Sprintf("%s (%s)", name, ids[0])
	}
	return strings.Join(ids, ", ")
}

// configuredKeyName is the name of the first configured key still registered,
// which is what the question offers as its default. A key deleted at the
// provider leaves the question without one rather than with a stale answer.
func configuredKeyName(keys []provider.SSHKey, current []string) string {
	for _, key := range keys {
		if slices.Contains(current, key.ID) {
			return key.Name
		}
	}
	return ""
}

// defaultKeyPaths are where a private key usually is, newest algorithm first.
// The wizard offers the first one that exists, so the common case is answered
// by pressing Enter.
var defaultKeyPaths = []string{"~/.ssh/id_ed25519", "~/.ssh/id_rsa"}

// askSSHKeyPath asks where the private half of the chosen key is.
//
// The bootstrap logs in with it. A file that is not there is not refused: an
// agent may be holding the key, which is also the answer for a key with a
// passphrase, so the wizard says what it found and takes the answer either way.
func askSSHKeyPath(ctx context.Context, p *prompt.Prompter, current, keyName string) (string, error) {
	p.Printf("\nThe bootstrap logs in with the private half of %s.\n\n", keyName)

	answer, err := p.Input(ctx, "Key file", defaultOf(current, keyPathOptions()))
	if err != nil {
		return "", err
	}

	path, err := expandHome(answer)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err != nil {
		// Worth saying, not worth refusing: an agent holding the key is a
		// perfectly good answer, and is the only one for a key with a
		// passphrase.
		p.Printf("There is no file at %s. If your agent holds the key that is fine;\n", path)
		p.Printf("otherwise `vpncli server bootstrap` will not be able to log in.\n")
	}
	return answer, nil
}

// keyPathOptions is the default list as menu options, so defaultOf can pick
// the first that exists.
func keyPathOptions() []prompt.Option {
	var options []prompt.Option
	for _, path := range defaultKeyPaths {
		expanded, err := expandHome(path)
		if err != nil || !exists(expanded) {
			continue
		}
		options = append(options, prompt.Option{Key: path})
	}
	if len(options) == 0 {
		options = append(options, prompt.Option{Key: defaultKeyPaths[0]})
	}
	return options
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// expandHome resolves a leading ~, which is how a key path is written and is
// not something the shell expanded for us here.
func expandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expanding %s: %w", path, err)
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~")), nil
}

// camouflageSites are the sites offered as REALITY destinations. A good one
// speaks TLS 1.3 and HTTP/2, is hosted behind a large CDN so the traffic joins
// a crowd, is unremarkable to be seen talking to from wherever the server
// lives - and sends a certificate small enough for REALITY to relay.
//
// That last one is not a preference. www.microsoft.com used to lead this list
// and produced servers that authenticated clients and then failed every
// connection: its certificate, staple and timestamps come to more than the
// 8192 bytes REALITY can carry. Every entry here has been measured, and
// whatever is chosen is measured again by reality.Check.
var camouflageSites = []prompt.Option{
	{Key: "www.samsung.com", Label: "widely mirrored, rarely on anyone's block list"},
	{Key: "www.cloudflare.com", Label: "everywhere, though obviously a CDN"},
	{Key: "github.com", Label: "small certificate, unremarkable from a developer machine"},
	{Key: "dl.google.com", Label: "download endpoint, long connections look normal"},
	{Key: "www.apple.com", Label: "reached from everywhere, but Xray warns it draws attention in China"},
}

// customCamouflage is the escape hatch key. It is not a hostname, so it cannot
// collide with one.
const customCamouflage = "other"

// selectCamouflage asks which site the server should look like.
func selectCamouflage(ctx context.Context, p *prompt.Prompter, current string, check checkFunc) (string, error) {
	options := slices.Clone(camouflageSites)
	// A host already configured stays on the menu even when it is not one of
	// ours, so re-running the wizard cannot quietly replace a chosen site.
	if current != "" && !slices.ContainsFunc(options, func(o prompt.Option) bool { return o.Key == current }) {
		options = append(options, prompt.Option{Key: current, Label: "currently configured"})
	}
	options = append(options, prompt.Option{Key: customCamouflage, Label: "type a hostname"})

	p.Printf("\nREALITY hides the server behind a real site: the handshake is that\n")
	p.Printf("site's, so a probe sees only a visit to it. Best is somewhere near\n")
	p.Printf("the server that nobody would think twice about.\n\n")

	// A site that turns out to be unusable sends the question round again
	// rather than ending the wizard: nothing has been written yet, and losing
	// four answered questions over one bad site would be its own small
	// disaster.
	for {
		i, err := p.Select(ctx, "Camouflage", options, defaultOf(current, options))
		if err != nil {
			return "", err
		}

		host := options[i].Key
		if host == customCamouflage {
			answer, err := p.Input(ctx, "Hostname", current)
			if err != nil {
				return "", err
			}
			if host, err = camouflageHost(answer); err != nil {
				p.Printf("%v\n\n", err)
				continue
			}
		}

		host, err = checkCamouflage(ctx, p, host, check)
		if errors.Is(err, reality.ErrUnsuitable) {
			p.Printf("%v\n\nPick another one.\n\n", err)
			continue
		}
		if err != nil {
			return "", err
		}
		return host, nil
	}
}

// checkCamouflage makes sure the chosen site can actually be hidden behind.
//
// A site REALITY cannot relay produces the worst failure this program has:
// clients authenticate, the server logs nothing but a stranger being turned
// away, and every connection dies at the handshake. It costs one TLS
// connection to rule out here.
//
// A site that cannot be reached at all is a different matter - that is this
// machine's network, not the site - so it is reported and accepted.
func checkCamouflage(ctx context.Context, p *prompt.Prompter, host string, check checkFunc) (string, error) {
	p.Printf("\nChecking %s...\n", host)

	found, err := check(ctx, host)
	switch {
	case errors.Is(err, reality.ErrUnsuitable):
		return "", err
	case err != nil:
		p.Printf("Could not check it: %v\n", err)
		p.Printf("Taking it anyway. If clients cannot connect, this is the first thing to change.\n")
		return host, nil
	}

	p.Printf("Looks right: TLS 1.3, HTTP/2, %d byte certificate.\n", found.Handshake)
	return host, nil
}

// camouflageHost validates a typed hostname. It has to be a bare host: a URL
// or a host:port would be written into the config as an SNI value that no
// client can present.
func camouflageHost(answer string) (string, error) {
	host := strings.TrimSpace(answer)
	host = strings.TrimSuffix(host, ".")

	switch {
	case strings.Contains(host, "/"):
		return "", fmt.Errorf("%q is a URL: give just the hostname, like www.apple.com", answer)
	// Addresses are caught before the port check, or an IPv6 literal would be
	// turned down for the colons rather than for what is wrong with it.
	case net.ParseIP(host) != nil:
		return "", fmt.Errorf("%q is an address: REALITY needs a name a certificate is issued for", answer)
	case strings.Contains(host, ":"):
		return "", fmt.Errorf("%q carries a port: give just the hostname, %s is assumed", answer, config.RealityPort)
	case !strings.Contains(host, "."):
		return "", fmt.Errorf("%q is not a hostname: it needs a domain, like www.apple.com", answer)
	}
	return host, nil
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
