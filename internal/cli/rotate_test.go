package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lestex/vpncli/internal/config"
	"github.com/lestex/vpncli/internal/manager"
	"github.com/lestex/vpncli/internal/provider"
	"github.com/lestex/vpncli/internal/provider/digitalocean"
	"github.com/lestex/vpncli/internal/state"
)

// rotate runs the command against a fake provider and a fake server, with an
// existing server already in state.
func rotate(t *testing.T, vps *fakeProvider, f *fakeSSH, answer string, yes bool) (string, error) {
	t.Helper()

	var out bytes.Buffer
	err := runRotate(context.Background(), strings.NewReader(answer), &out,
		func(config.Config) (provider.VPSProvider, error) { return vps, nil },
		dialing(f, nil), checksOut, 1, yes)
	return out.String(), err
}

// rotatable seeds a configured server and a provider that will hand back a
// second one.
func rotatable(t *testing.T) (*fakeProvider, *fakeSSH) {
	t.Helper()
	connectable(t)
	bootstrapReady(t)

	vps := testProvider()
	// The old server holds 1001, so the replacement has to arrive as something
	// else, the way a real provider would hand it back.
	vps.createID = "2002"
	vps.ready = provider.VPSInstance{
		ID:       "2002",
		Provider: digitalocean.Name,
		IPv4:     "203.0.113.99",
		Status:   provider.StatusActive,
	}
	return vps, newFakeSSH()
}

func TestRotateIsRegistered(t *testing.T) {
	out := run(t, "server", "--help")
	if !strings.Contains(out, "rotate") {
		t.Errorf("rotate is missing from `vpncli server` help:\n%s", out)
	}
}

func TestRotateNeedsExactlyOneID(t *testing.T) {
	withStateDir(t)

	for _, args := range [][]string{{"server", "rotate"}, {"server", "rotate", "1", "2"}} {
		if _, err := execute(args...); err == nil {
			t.Errorf("%v was accepted, want an error", args)
		}
	}
}

func TestRotate(t *testing.T) {
	vps, f := rotatable(t)

	out, err := rotate(t, vps, f, "yes\n", false)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}

	// The old server is gone, provider side and locally.
	if len(vps.deleted) != 1 || vps.deleted[0] != "1001" {
		t.Errorf("deleted %v, want the old server's provider id", vps.deleted)
	}

	servers := storedServers(t)
	if len(servers) != 1 {
		t.Fatalf("state holds %+v, want only the replacement", servers)
	}
	replacement := servers[0]
	if replacement.ProviderID != "2002" || replacement.IPv4 != "203.0.113.99" {
		t.Errorf("the row left behind is not the replacement: %+v", replacement)
	}
	if !replacement.Bootstrapped() {
		t.Error("the replacement was left unconfigured")
	}

	// New address, new keys: nothing carried over from the server it replaced.
	if replacement.Credentials.UUID == connectableCredentials.UUID {
		t.Error("the replacement reuses the old client UUID")
	}
	if replacement.Credentials.PublicKey == connectableCredentials.PublicKey {
		t.Error("the replacement reuses the old REALITY keypair")
	}

	if !strings.Contains(out, "vpncli connect") {
		t.Errorf("nothing says clients have to be reconfigured:\n%s", out)
	}
}

// Building the replacement first is the whole point: a rotation that fails
// must leave the working server alone.
func TestRotateKeepsTheOldServerWhenTheReplacementFails(t *testing.T) {
	vps, f := rotatable(t)
	f.failOn = "apt-get"
	f.failErr = errors.New("dpkg was interrupted")

	out, err := rotate(t, vps, f, "yes\n", false)
	if err == nil {
		t.Fatal("expected the bootstrap failure to come back")
	}

	if len(vps.deleted) != 0 {
		t.Errorf("deleted %v after a failed rotation", vps.deleted)
	}
	if !strings.Contains(out, "untouched and still serving") {
		t.Errorf("nothing says the old server is still there:\n%s", out)
	}
	// The half-built replacement is billing, so its id has to be said.
	if !strings.Contains(out, "vpncli server destroy") {
		t.Errorf("the replacement is left unaccounted for:\n%s", out)
	}

	servers := storedServers(t)
	if len(servers) != 2 {
		t.Fatalf("state holds %+v, want the old server and the replacement", servers)
	}
}

// Nothing is created before the question is answered.
func TestRotateDeclinedTouchesNothing(t *testing.T) {
	vps, f := rotatable(t)

	out, err := rotate(t, vps, f, "no\n", false)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}

	if vps.created.Name != "" {
		t.Errorf("created %+v after the question was turned down", vps.created)
	}
	if len(vps.deleted) != 0 {
		t.Errorf("deleted %v after the question was turned down", vps.deleted)
	}
	if !strings.Contains(out, "left alone") {
		t.Errorf("a declined rotation does not say so:\n%s", out)
	}
	if servers := storedServers(t); len(servers) != 1 {
		t.Errorf("state holds %+v, want the one server", servers)
	}
}

// The confirmation says what it will cost, because for a couple of minutes it
// is two servers.
func TestRotateConfirmationSaysBothAreBilled(t *testing.T) {
	vps, f := rotatable(t)

	out, _ := rotate(t, vps, f, "no\n", false)
	for _, want := range []string{"vpncli-fra1-a1b2c3", "billed", "destroyed only"} {
		if !strings.Contains(out, want) {
			t.Errorf("the confirmation does not mention %q:\n%s", want, out)
		}
	}
}

func TestRotateWithYesDoesNotAsk(t *testing.T) {
	vps, f := rotatable(t)

	out, err := rotate(t, vps, f, "", true)
	if err != nil {
		t.Fatalf("rotate --yes: %v", err)
	}
	if strings.Contains(out, "Type yes") {
		t.Errorf("--yes still asked:\n%s", out)
	}
	if len(vps.deleted) != 1 {
		t.Errorf("deleted %v, want the old server gone", vps.deleted)
	}
}

// Provider ids only mean anything within one provider, so the destroy at the
// end would otherwise be aimed at whatever carries that id here.
func TestRotateRefusesAServerFromAnotherProvider(t *testing.T) {
	vps, f := rotatable(t)

	store, err := openStore()
	if err != nil {
		t.Fatalf("opening state: %v", err)
	}
	if _, err := store.Insert(context.Background(), state.Server{
		Provider: "hetzner", ProviderID: "9001", Name: "elsewhere",
		Region: "fsn1", Size: "cx11", Image: "ubuntu-24.04",
	}); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	store.Close()

	var out bytes.Buffer
	err = runRotate(context.Background(), strings.NewReader("yes\n"), &out,
		func(config.Config) (provider.VPSProvider, error) { return vps, nil },
		dialing(f, nil), checksOut, 2, false)
	if !errors.Is(err, manager.ErrWrongProvider) {
		t.Fatalf("got %v, want ErrWrongProvider", err)
	}
	if vps.created.Name != "" {
		t.Errorf("a replacement was created anyway: %+v", vps.created)
	}
}

func TestRotateUnknownServer(t *testing.T) {
	withStateDir(t)
	bootstrapReady(t)

	vps := testProvider()
	var out bytes.Buffer
	err := runRotate(context.Background(), strings.NewReader("yes\n"), &out,
		func(config.Config) (provider.VPSProvider, error) { return vps, nil },
		dialing(newFakeSSH(), nil), checksOut, 42, false)
	if !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("got %v, want state.ErrNotFound", err)
	}
}

func TestRotateHelpExplainsTheOrder(t *testing.T) {
	out := run(t, "server", "rotate", "--help")
	for _, want := range []string{"destroy", "new local id", "current config"} {
		if !strings.Contains(out, want) {
			t.Errorf("rotate help does not mention %q:\n%s", want, out)
		}
	}
}
