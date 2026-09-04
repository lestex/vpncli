package cli

import (
	"bytes"
	"context"
	"errors"
	"reflect"
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
	err     error
}

func (f *fakeProvider) Name() string { return digitalocean.Name }

func (f *fakeProvider) ListRegions(context.Context) ([]provider.Region, error) {
	return f.regions, f.err
}

func testRegions() []provider.Region {
	return []provider.Region{
		{Slug: "ams3", Name: "Amsterdam 3", Available: true},
		{Slug: "fra1", Name: "Frankfurt 1", Available: true},
		{Slug: "sfo1", Name: "San Francisco 1", Available: false},
	}
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
	out, err := wizard(t, &fakeProvider{regions: testRegions()}, "2\n")
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

	path, _ := config.Path()
	if !strings.Contains(out, path) {
		t.Errorf("the wizard does not say where it wrote:\n%s", out)
	}
}

func TestInitAcceptsARegionSlug(t *testing.T) {
	if _, err := wizard(t, &fakeProvider{regions: testRegions()}, "ams3\n"); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	if got := savedConfig(t).Region; got != "ams3" {
		t.Errorf("region = %q, want ams3", got)
	}
}

// An unavailable region cannot take a droplet, so choosing it would only
// produce a config that fails later.
func TestInitSkipsUnavailableRegions(t *testing.T) {
	out, err := wizard(t, &fakeProvider{regions: testRegions()}, "sfo1\n2\n")
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
	vps := &fakeProvider{regions: []provider.Region{{Slug: "sfo1", Name: "San Francisco 1"}}}

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
		Image:      "ubuntu-24-04-x64",
		SSHKeyPath: "~/.ssh/id_ed25519",
		Reality:    config.Reality{Dest: "www.cloudflare.com:443"},
	}
	if err := existing.Save(); err != nil {
		t.Fatalf("seeding config: %v", err)
	}

	var out bytes.Buffer
	vps := &fakeProvider{regions: testRegions()}
	err := runInit(context.Background(), strings.NewReader("\n"), &out,
		func(config.Config) (provider.VPSProvider, error) { return vps, nil })
	if err != nil {
		t.Fatalf("runInit: %v", err)
	}

	if !strings.Contains(out.String(), "[fra1]") {
		t.Errorf("the current region is not offered as the default:\n%s", out.String())
	}

	got := savedConfig(t)
	if got.Region != "fra1" {
		t.Errorf("region = %q, want the default fra1", got.Region)
	}
	if got.Size != existing.Size || got.Image != existing.Image {
		t.Errorf("size/image were dropped: %+v", got)
	}
	if got.SSHKeyPath != existing.SSHKeyPath || got.Reality.Dest != existing.Reality.Dest {
		t.Errorf("unasked settings were dropped: %+v", got)
	}
}

// Ctrl-D partway through must leave no half-answered config behind.
func TestInitAbandonedWritesNothing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var out bytes.Buffer
	vps := &fakeProvider{regions: testRegions()}
	err := runInit(context.Background(), strings.NewReader(""), &out,
		func(config.Config) (provider.VPSProvider, error) { return vps, nil })
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
	for _, want := range []string{"region", "config.yaml", "DIGITALOCEAN_TOKEN"} {
		if !strings.Contains(out, want) {
			t.Errorf("init help does not mention %q:\n%s", want, out)
		}
	}
}
