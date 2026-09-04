package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/lestex/vpncli/internal/config"
	"github.com/lestex/vpncli/internal/prompt"
	"github.com/lestex/vpncli/internal/provider"
	"github.com/lestex/vpncli/internal/provider/digitalocean"
)

// fakeProvider is a provider that only answers catalog lookups. The interface
// is embedded rather than stubbed, so anything else the wizard calls panics
// instead of quietly returning nothing.
type fakeProvider struct {
	provider.VPSProvider

	regions []provider.Region
	sizes   []provider.Size
	images  []provider.Image
	err     error
}

func (f *fakeProvider) Name() string { return digitalocean.Name }

func (f *fakeProvider) ListRegions(context.Context) ([]provider.Region, error) {
	return f.regions, f.err
}

func (f *fakeProvider) ListSizes(context.Context) ([]provider.Size, error) {
	return f.sizes, f.err
}

func (f *fakeProvider) ListImages(context.Context) ([]provider.Image, error) {
	return f.images, f.err
}

func testRegions() []provider.Region {
	return []provider.Region{
		{Slug: "ams3", Name: "Amsterdam 3", Available: true},
		{Slug: "fra1", Name: "Frankfurt 1", Available: true},
		{Slug: "sfo1", Name: "San Francisco 1", Available: false},
	}
}

// testSizes is what a provider hands back: cheapest first, and not all of it
// available in every region.
func testSizes() []provider.Size {
	return []provider.Size{
		{Slug: "s-1vcpu-512mb-10gb", VCPUs: 1, MemoryMB: 512, DiskGB: 10, PriceMonthly: 4, Available: true, Regions: []string{"fra1", "ams3"}},
		{Slug: "s-1vcpu-1gb", VCPUs: 1, MemoryMB: 1024, DiskGB: 25, PriceMonthly: 6, Available: true, Regions: []string{"fra1"}},
		{Slug: "s-2vcpu-4gb", VCPUs: 2, MemoryMB: 4096, DiskGB: 80, PriceMonthly: 24, Available: true, Regions: []string{"ams3"}},
		{Slug: "s-8vcpu-16gb", VCPUs: 8, MemoryMB: 16384, DiskGB: 320, PriceMonthly: 96, Available: false, Regions: []string{"fra1"}},
	}
}

func testImages() []provider.Image {
	return []provider.Image{
		{Slug: "debian-13-x64", Name: "13 x64", Distribution: "Debian"},
		{Slug: "debian-12-x64", Name: "12 x64", Distribution: "Debian"},
		{Slug: "ubuntu-24-04-x64", Name: "24.04 (LTS) x64", Distribution: "Ubuntu"},
		{Slug: "ubuntu-22-04-x64", Name: "22.04 (LTS) x64", Distribution: "Ubuntu"},
		{Slug: "fedora-42-x64", Name: "42 x64", Distribution: "Fedora"},
	}
}

// testProvider answers every catalog lookup the wizard makes.
func testProvider() *fakeProvider {
	return &fakeProvider{regions: testRegions(), sizes: testSizes(), images: testImages()}
}

// wizard runs the wizard against scripted answers and a fake provider, with
// the config pointed at a temporary directory.
func wizard(t *testing.T, vps *fakeProvider, answers string) (string, error) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var out bytes.Buffer
	err := runInit(context.Background(), strings.NewReader(answers), &out,
		func(config.Config) (provider.VPSProvider, error) { return vps, nil })
	return out.String(), err
}

// offered picks the keys out of the menus in a transcript, so a test can ask
// what was on offer without matching an answer the user typed back at it.
func offered(transcript string) []string {
	var keys []string
	for _, line := range strings.Split(transcript, "\n") {
		if m := menuLine.FindStringSubmatch(line); m != nil {
			keys = append(keys, m[1])
		}
	}
	return keys
}

// menuLine matches one numbered choice: "  2)  s-1vcpu-1gb  $6/mo ...".
var menuLine = regexp.MustCompile(`^\s+\d+\)\s+(\S+)`)

// wizardOutput is wizard, for the tests that read the transcript back.
func wizardOutput(t *testing.T, vps *fakeProvider, answers string) (config.Config, string, error) {
	t.Helper()

	out, err := wizard(t, vps, answers)
	if err != nil {
		return config.Config{}, out, err
	}
	return savedConfig(t), out, nil
}

// savedConfig reads back what the wizard wrote.
func savedConfig(t *testing.T) config.Config {
	t.Helper()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	return cfg
}

func TestInitIsRegistered(t *testing.T) {
	out := run(t, "--help")
	if !strings.Contains(out, "init") {
		t.Errorf("init is missing from root help:\n%s", out)
	}
}

func TestInitRejectsArgs(t *testing.T) {
	if _, err := execute("init", "unexpected"); err == nil {
		t.Error("expected an error for an unexpected positional argument")
	}
}

// The region list is an API call, so a missing token should be named before
// any question is asked.
func TestInitRequiresToken(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("DIGITALOCEAN_TOKEN", "")
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", "")

	_, err := execute("init")
	if !errors.Is(err, digitalocean.ErrNoToken) {
		t.Fatalf("got %v, want ErrNoToken", err)
	}
}

func TestInitWritesTheAnswers(t *testing.T) {
	out, err := wizard(t, testProvider(), "2\n2\n3\n")
	if err != nil {
		t.Fatalf("runInit: %v", err)
	}

	cfg := savedConfig(t)
	if cfg.Provider != digitalocean.Name {
		t.Errorf("provider = %q, want %q", cfg.Provider, digitalocean.Name)
	}
	if cfg.Region != "fra1" {
		t.Errorf("region = %q, want fra1", cfg.Region)
	}
	if cfg.Size != "s-1vcpu-1gb" {
		t.Errorf("size = %q, want s-1vcpu-1gb", cfg.Size)
	}
	if cfg.Image != "debian-13-x64" {
		t.Errorf("image = %q, want debian-13-x64", cfg.Image)
	}

	path, _ := config.Path()
	if !strings.Contains(out, path) {
		t.Errorf("the wizard does not say where it wrote:\n%s", out)
	}
}

func TestInitAcceptsSlugs(t *testing.T) {
	if _, err := wizard(t, testProvider(), "ams3\ns-2vcpu-4gb\ndebian-12-x64\n"); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	cfg := savedConfig(t)
	if cfg.Region != "ams3" || cfg.Size != "s-2vcpu-4gb" || cfg.Image != "debian-12-x64" {
		t.Errorf("got %+v, want the answers given by slug", cfg)
	}
}

// Enter through the wizard and the result should be a server worth having:
// the cheapest size in the chosen region, running the newest Ubuntu.
func TestInitDefaultsToCheapestAndNewestUbuntu(t *testing.T) {
	if _, err := wizard(t, testProvider(), "fra1\n\n\n"); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	cfg := savedConfig(t)
	if cfg.Size != "s-1vcpu-512mb-10gb" {
		t.Errorf("size = %q, want the cheapest", cfg.Size)
	}
	if cfg.Image != "ubuntu-24-04-x64" {
		t.Errorf("image = %q, want the newest Ubuntu", cfg.Image)
	}
}

// A size that cannot be created in the chosen region is not a choice, and
// neither is one the account cannot create at all.
func TestInitOffersOnlySizesTheRegionHas(t *testing.T) {
	// The size only ams3 has is typed anyway, and has to be turned down.
	out, err := wizard(t, testProvider(), "fra1\ns-2vcpu-4gb\n2\n\n")
	if err != nil {
		t.Fatalf("runInit: %v", err)
	}

	for _, key := range offered(out) {
		if key == "s-2vcpu-4gb" {
			t.Errorf("a size from another region was offered:\n%s", out)
		}
		if key == "s-8vcpu-16gb" {
			t.Errorf("an unavailable size was offered:\n%s", out)
		}
	}
	if !strings.Contains(out, "not one of the choices") {
		t.Errorf("a size from another region was accepted:\n%s", out)
	}
	if got := savedConfig(t).Size; got != "s-1vcpu-1gb" {
		t.Errorf("size = %q, want the second of the two offered", got)
	}
}

// The menu is the cheapest few, since an account can create some seventy sizes
// and a VPN can use almost none of them.
func TestInitCapsTheSizeMenu(t *testing.T) {
	vps := testProvider()
	vps.sizes = nil
	for i := range sizesShown + 4 {
		vps.sizes = append(vps.sizes, provider.Size{
			Slug:         fmt.Sprintf("s-%dvcpu-1gb", i+1),
			PriceMonthly: float64(6 * (i + 1)),
			Available:    true,
			Regions:      []string{"fra1"},
		})
	}

	out, err := wizard(t, vps, "fra1\n\n\n")
	if err != nil {
		t.Fatalf("runInit: %v", err)
	}

	// The images are a menu of their own, so count only the size one.
	var sizes []string
	for _, key := range offered(out) {
		if strings.HasPrefix(key, "s-") {
			sizes = append(sizes, key)
		}
	}
	if len(sizes) != sizesShown {
		t.Errorf("got %d sizes offered, want %d: %v", len(sizes), sizesShown, sizes)
	}
	if sizes[0] != "s-1vcpu-1gb" {
		t.Errorf("the cheapest size is not first: %v", sizes)
	}
}

// A size chosen deliberately can be an expensive one. Re-running the wizard
// must not quietly drop it off the end of the menu.
func TestInitKeepsAConfiguredSizeOnTheMenu(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	vps := testProvider()
	vps.sizes = nil
	for i := range sizesShown + 4 {
		vps.sizes = append(vps.sizes, provider.Size{
			Slug:         fmt.Sprintf("s-%dvcpu-1gb", i+1),
			PriceMonthly: float64(6 * (i + 1)),
			Available:    true,
			Regions:      []string{"fra1"},
		})
	}

	expensive := vps.sizes[len(vps.sizes)-1].Slug
	existing := config.Config{Provider: digitalocean.Name, Region: "fra1", Size: expensive}
	if err := existing.Save(); err != nil {
		t.Fatalf("seeding config: %v", err)
	}

	var out bytes.Buffer
	err := runInit(context.Background(), strings.NewReader("\n\n\n"), &out,
		func(config.Config) (provider.VPSProvider, error) { return vps, nil })
	if err != nil {
		t.Fatalf("runInit: %v", err)
	}

	if !strings.Contains(out.String(), expensive) {
		t.Errorf("the configured size %q fell off the menu:\n%s", expensive, out.String())
	}
	if got := savedConfig(t).Size; got != expensive {
		t.Errorf("size = %q, want the configured %q", got, expensive)
	}
}

// The bootstrap is apt, nginx, ufw and a sysctl. An image it cannot configure
// would only produce a server that never gets finished.
func TestInitOffersOnlySupportedDistributions(t *testing.T) {
	out, err := wizard(t, testProvider(), "fra1\n\nfedora-42-x64\n1\n")
	if err != nil {
		t.Fatalf("runInit: %v", err)
	}

	if slices.Contains(offered(out), "fedora-42-x64") {
		t.Errorf("an image the bootstrap cannot configure was offered:\n%s", out)
	}
	if got := savedConfig(t).Image; got != "ubuntu-24-04-x64" {
		t.Errorf("image = %q, want the first offered", got)
	}
}

// Ubuntu is what the bootstrap is developed against, so it leads.
func TestInitOrdersImagesByDistribution(t *testing.T) {
	_, out, err := wizardOutput(t, testProvider(), "fra1\n\n\n")
	if err != nil {
		t.Fatalf("runInit: %v", err)
	}

	keys := offered(out)
	ubuntu := slices.Index(keys, "ubuntu-24-04-x64")
	debian := slices.Index(keys, "debian-13-x64")
	if ubuntu < 0 || debian < 0 {
		t.Fatalf("both distributions should be offered: %v", keys)
	}
	if ubuntu > debian {
		t.Errorf("Debian is offered ahead of Ubuntu: %v", keys)
	}
}

func TestInitWithNoSizesInTheRegion(t *testing.T) {
	vps := testProvider()
	vps.sizes = []provider.Size{{Slug: "s-1vcpu-1gb", Available: true, Regions: []string{"ams3"}}}

	_, err := wizard(t, vps, "fra1\n")
	if err == nil || !strings.Contains(err.Error(), "no sizes available in fra1") {
		t.Fatalf("got %v, want an error naming the region", err)
	}
}

func TestInitWithNoSupportedImages(t *testing.T) {
	vps := testProvider()
	vps.images = []provider.Image{{Slug: "fedora-42-x64", Name: "42 x64", Distribution: "Fedora"}}

	_, err := wizard(t, vps, "fra1\n\n")
	if err == nil || !strings.Contains(err.Error(), "Ubuntu or Debian") {
		t.Fatalf("got %v, want an error naming what is missing", err)
	}
}

func TestDescribeSize(t *testing.T) {
	tests := []struct {
		size provider.Size
		want []string
	}{
		{
			size: provider.Size{MemoryMB: 512, VCPUs: 1, DiskGB: 10, PriceMonthly: 4},
			want: []string{"$4/mo", "512MB RAM", "1 vCPU", "10GB disk"},
		},
		{
			size: provider.Size{MemoryMB: 4096, VCPUs: 2, DiskGB: 80, PriceMonthly: 24},
			want: []string{"$24/mo", "4GB RAM"},
		},
		// A price with cents in it must not round down to a cheaper-looking one.
		{
			size: provider.Size{MemoryMB: 1536, PriceMonthly: 10.5},
			want: []string{"$10.50/mo", "1536MB RAM"},
		},
	}

	for _, tt := range tests {
		got := describeSize(tt.size)
		for _, want := range tt.want {
			if !strings.Contains(got, want) {
				t.Errorf("describeSize(%+v) = %q, missing %q", tt.size, got, want)
			}
		}
	}
}

// An unavailable region cannot take a droplet, so choosing it would only
// produce a config that fails later.
func TestInitSkipsUnavailableRegions(t *testing.T) {
	out, err := wizard(t, testProvider(), "sfo1\n2\n\n\n")
	if err != nil {
		t.Fatalf("runInit: %v", err)
	}

	if strings.Contains(out, "San Francisco") {
		t.Errorf("an unavailable region was offered:\n%s", out)
	}
	if got := savedConfig(t).Region; got != "fra1" {
		t.Errorf("region = %q, want fra1", got)
	}
}

func TestInitWithNoAvailableRegions(t *testing.T) {
	vps := testProvider()
	vps.regions = []provider.Region{{Slug: "sfo1", Name: "San Francisco 1"}}

	_, err := wizard(t, vps, "\n")
	if err == nil {
		t.Fatal("expected an error when nothing can be chosen")
	}
	if !strings.Contains(err.Error(), "no regions available") {
		t.Errorf("error %q does not explain what is missing", err)
	}
}

func TestInitReportsACatalogFailure(t *testing.T) {
	vps := &fakeProvider{err: errors.New("401 unauthorized")}

	if _, err := wizard(t, vps, "\n"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("got %v, want the provider error", err)
	}
}

// Re-running the wizard offers the current region as the default, and leaves
// the settings it does not ask about alone.
func TestInitKeepsExistingSettings(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	existing := config.Config{
		Provider:   digitalocean.Name,
		Region:     "fra1",
		Size:       "s-1vcpu-1gb",
		Image:      "ubuntu-22-04-x64",
		SSHKeyPath: "~/.ssh/id_ed25519",
		Reality:    config.Reality{Dest: "www.cloudflare.com:443"},
	}
	if err := existing.Save(); err != nil {
		t.Fatalf("seeding config: %v", err)
	}

	var out bytes.Buffer
	err := runInit(context.Background(), strings.NewReader("\n\n\n"), &out,
		func(config.Config) (provider.VPSProvider, error) { return testProvider(), nil })
	if err != nil {
		t.Fatalf("runInit: %v", err)
	}

	for _, want := range []string{"[fra1]", "[s-1vcpu-1gb]", "[ubuntu-22-04-x64]"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the current value %s is not offered as the default:\n%s", want, out.String())
		}
	}

	got := savedConfig(t)
	if got.Region != existing.Region || got.Size != existing.Size || got.Image != existing.Image {
		t.Errorf("got %+v, want the defaults kept: %+v", got, existing)
	}
	if got.SSHKeyPath != existing.SSHKeyPath || got.Reality.Dest != existing.Reality.Dest {
		t.Errorf("unasked settings were dropped: %+v", got)
	}
}

// Ctrl-D partway through must leave no half-answered config behind.
func TestInitAbandonedWritesNothing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var out bytes.Buffer
	err := runInit(context.Background(), strings.NewReader(""), &out,
		func(config.Config) (provider.VPSProvider, error) { return testProvider(), nil })
	if !errors.Is(err, prompt.ErrNoInput) {
		t.Fatalf("got %v, want ErrNoInput", err)
	}

	if cfg := savedConfig(t); !reflect.DeepEqual(cfg, config.Config{}) {
		t.Errorf("an abandoned wizard wrote %+v", cfg)
	}
}

// Provisioning needs a region, so the wizard has to say what it is for.
func TestInitHelpNamesWhatItAsks(t *testing.T) {
	out := run(t, "init", "--help")
	for _, want := range []string{"region", "size", "image", "config.yaml", "DIGITALOCEAN_TOKEN"} {
		if !strings.Contains(out, want) {
			t.Errorf("init help does not mention %q:\n%s", want, out)
		}
	}
}
