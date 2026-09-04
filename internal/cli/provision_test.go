package cli

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/lestex/vpncli/internal/config"
	"github.com/lestex/vpncli/internal/provider"
	"github.com/lestex/vpncli/internal/provider/digitalocean"
)

// fullConfig is what a completed wizard leaves behind.
func fullConfig() config.Config {
	return config.Config{
		Provider:   digitalocean.Name,
		Region:     "fra1",
		Size:       "s-1vcpu-1gb",
		Image:      "ubuntu-24-04-x64",
		SSHKeyIDs:  []string{"11"},
		SSHKeyPath: "~/.ssh/id_ed25519",
		Reality:    config.Camouflage("www.apple.com"),
	}
}

func TestProvisionIsRegistered(t *testing.T) {
	out := run(t, "server", "--help")
	if !strings.Contains(out, "provision") {
		t.Errorf("provision is missing from `vpncli server` help:\n%s", out)
	}
}

func TestProvisionRejectsArgs(t *testing.T) {
	if _, err := execute("server", "provision", "unexpected"); err == nil {
		t.Error("expected an error for an unexpected positional argument")
	}
}

// A wizard that was never run is the likeliest reason provision cannot work,
// so the message names the command that fixes it rather than the API's 422.
func TestProvisionWithNoConfig(t *testing.T) {
	withStateDir(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	_, err := execute("server", "provision")
	if err == nil {
		t.Fatal("expected an error for an empty config")
	}
	for _, want := range []string{"region", "size", "image", "ssh key", "camouflage", "vpncli providers init"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// Everything else is answered, so the only thing left to report is the token.
func TestProvisionRequiresToken(t *testing.T) {
	withStateDir(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("DIGITALOCEAN_TOKEN", "")
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", "")

	cfg := fullConfig()
	if err := cfg.Save(); err != nil {
		t.Fatalf("seeding config: %v", err)
	}

	if _, err := execute("server", "provision"); !errors.Is(err, digitalocean.ErrNoToken) {
		t.Fatalf("got %v, want ErrNoToken", err)
	}
}

func TestCreateOptions(t *testing.T) {
	opts, err := createOptions(fullConfig())
	if err != nil {
		t.Fatalf("createOptions: %v", err)
	}

	if opts.Region != "fra1" || opts.Size != "s-1vcpu-1gb" || opts.Image != "ubuntu-24-04-x64" {
		t.Errorf("got %+v, want the configured answers", opts)
	}
	if len(opts.SSHKeyIDs) != 1 || opts.SSHKeyIDs[0] != "11" {
		t.Errorf("ssh keys = %v, want the configured key: without one the provider mails a root password", opts.SSHKeyIDs)
	}
	// Tagging belongs to the manager, which applies it to every create.
	if len(opts.Tags) != 0 {
		t.Errorf("tags = %v, want none set here", opts.Tags)
	}
}

func TestCreateOptionsNamesEveryMissingAnswer(t *testing.T) {
	_, err := createOptions(config.Config{Provider: digitalocean.Name, Region: "fra1"})
	if err == nil {
		t.Fatal("expected an error for a half-answered config")
	}
	if strings.Contains(err.Error(), "region") {
		t.Errorf("error %q names an answer that is there", err)
	}
	for _, want := range []string{"size", "image", "ssh key"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// The name says where the server is, and two servers in one region have to be
// tellable apart in a listing.
func TestServerName(t *testing.T) {
	first, err := serverName("fra1")
	if err != nil {
		t.Fatalf("serverName: %v", err)
	}
	second, err := serverName("fra1")
	if err != nil {
		t.Fatalf("serverName: %v", err)
	}

	if !strings.HasPrefix(first, provider.ManagedTag+"-fra1-") {
		t.Errorf("name = %q, want it to name vpncli and the region", first)
	}
	if first == second {
		t.Errorf("two names came out the same: %q", first)
	}
}

func TestProvisionHelpSaysWhatItDoesNotDoYet(t *testing.T) {
	out := run(t, "server", "provision", "--help")
	for _, want := range []string{"vpncli providers init", "vpncli server destroy", "Xray-core"} {
		if !strings.Contains(out, want) {
			t.Errorf("provision help does not mention %q:\n%s", want, out)
		}
	}
}

// provision runs the command against a fake provider and a fake server, with
// config and state pointed at temporary directories.
func provision(t *testing.T, vps *fakeProvider, cfg config.Config) (string, error) {
	t.Helper()
	return provisionWith(t, vps, cfg, dialing(newFakeSSH(), nil))
}

func provisionWith(t *testing.T, vps *fakeProvider, cfg config.Config, dial dialFunc) (string, error) {
	t.Helper()
	withStateDir(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if err := cfg.Save(); err != nil {
		t.Fatalf("seeding config: %v", err)
	}

	var out bytes.Buffer
	err := runProvision(context.Background(), &out,
		func(config.Config) (provider.VPSProvider, error) { return vps, nil }, dial, checksOut)
	return out.String(), err
}

func TestProvisionCreatesAndRecordsAServer(t *testing.T) {
	vps := testProvider()
	vps.ready = provider.VPSInstance{
		ID:       "1001",
		Provider: digitalocean.Name,
		IPv4:     "203.0.113.10",
		Status:   provider.StatusActive,
	}

	out, err := provision(t, vps, fullConfig())
	if err != nil {
		t.Fatalf("runProvision: %v", err)
	}

	if vps.created.Region != "fra1" || vps.created.Size != "s-1vcpu-1gb" {
		t.Errorf("created %+v, want the configured answers", vps.created)
	}
	// Without the tag, sync would never adopt what this created.
	if !slices.Contains(vps.created.Tags, provider.ManagedTag) {
		t.Errorf("created %+v, want it tagged %q", vps.created, provider.ManagedTag)
	}

	if !strings.Contains(out, "203.0.113.10") || !strings.Contains(out, string(provider.StatusActive)) {
		t.Errorf("the server that came up is not reported:\n%s", out)
	}

	servers := storedServers(t)
	if len(servers) != 1 {
		t.Fatalf("state holds %+v, want the one server", servers)
	}
	if servers[0].IPv4 != "203.0.113.10" {
		t.Errorf("row = %+v, want the address the wait found", servers[0])
	}
}

// A wait that fails leaves a server running and billing. The id it was
// recorded under is the only way to get rid of it, so it has to be said.
func TestProvisionNamesTheServerWhenTheWaitFails(t *testing.T) {
	vps := testProvider()
	vps.readyErr = errors.New("timed out waiting for sshd")

	out, err := provision(t, vps, fullConfig())
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("got %v, want the wait error", err)
	}

	if !strings.Contains(out, "vpncli server destroy") {
		t.Errorf("a created server is left unaccounted for:\n%s", out)
	}

	if servers := storedServers(t); len(servers) != 1 {
		t.Errorf("state holds %+v, want the created server recorded", servers)
	}
}

// Nothing was created, so there is nothing to report but the failure.
func TestProvisionRecordsNothingWhenCreateFails(t *testing.T) {
	vps := testProvider()
	vps.createErr = errors.New("422 unprocessable entity")

	out, err := provision(t, vps, fullConfig())
	if err == nil || !strings.Contains(err.Error(), "422") {
		t.Fatalf("got %v, want the create error", err)
	}
	if strings.Contains(out, "vpncli server destroy") {
		t.Errorf("a server that was never created was reported as billable:\n%s", out)
	}
	if servers := storedServers(t); len(servers) != 0 {
		t.Errorf("state holds %+v, want nothing", servers)
	}
}

// The point of provision is a server that works, so it configures what it
// creates rather than handing back something half done.
func TestProvisionBootstrapsWhatItCreates(t *testing.T) {
	vps := testProvider()
	vps.ready = provider.VPSInstance{
		ID:       "1001",
		Provider: digitalocean.Name,
		IPv4:     "203.0.113.10",
		Status:   provider.StatusActive,
	}
	f := newFakeSSH()

	out, err := provisionWith(t, vps, fullConfig(), dialing(f, nil))
	if err != nil {
		t.Fatalf("runProvision: %v", err)
	}

	srv := storedServers(t)[0]
	if !srv.Bootstrapped() {
		t.Error("provision left the server unconfigured")
	}
	if !strings.Contains(out, "VLESS+REALITY") {
		t.Errorf("the command does not say what the server is now:\n%s", out)
	}
	if _, ok := f.uploads["/usr/local/etc/xray/config.json"]; !ok {
		t.Error("no server config reached the server")
	}
}

// The server exists and bills whether or not it was configured, so a failure
// here has to point at the command that finishes the job.
func TestProvisionPointsAtBootstrapWhenConfiguringFails(t *testing.T) {
	vps := testProvider()
	vps.ready = provider.VPSInstance{ID: "1001", Provider: digitalocean.Name, IPv4: "203.0.113.10", Status: provider.StatusActive}

	f := newFakeSSH()
	f.failOn = "apt-get"
	f.failErr = errors.New("dpkg was interrupted")

	out, err := provisionWith(t, vps, fullConfig(), dialing(f, nil))
	if err == nil {
		t.Fatal("expected the bootstrap failure to come back")
	}
	if !strings.Contains(out, "vpncli server bootstrap") {
		t.Errorf("nothing says how to finish the server:\n%s", out)
	}

	srv := storedServers(t)[0]
	if srv.Bootstrapped() {
		t.Error("a failed bootstrap marked the server configured")
	}
}
