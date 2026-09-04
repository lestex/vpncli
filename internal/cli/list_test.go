package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lestex/vpncli/internal/state"
)

// withStateDir points the config helpers at a temporary directory, so a test
// never reads or writes the developer's real state database.
func withStateDir(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
}

// seedServers writes rows into the state database the commands will open.
func seedServers(t *testing.T, servers ...state.Server) {
	t.Helper()

	store, err := openStore()
	if err != nil {
		t.Fatalf("opening state: %v", err)
	}
	defer store.Close()

	for _, srv := range servers {
		if _, err := store.Insert(context.Background(), srv); err != nil {
			t.Fatalf("seeding state: %v", err)
		}
	}
}

func testServer(providerID, name, ipv4, status string) state.Server {
	return state.Server{
		Provider:   "digitalocean",
		ProviderID: providerID,
		Name:       name,
		Region:     "fra1",
		Size:       "s-1vcpu-1gb",
		Image:      "ubuntu-24-04-x64",
		IPv4:       ipv4,
		Status:     status,
		CreatedAt:  time.Now().UTC().Add(-3 * time.Hour),
	}
}

func TestListIsRegistered(t *testing.T) {
	out := run(t, "server", "--help")
	if !strings.Contains(out, "list") {
		t.Errorf("list is missing from `vpncli server` help:\n%s", out)
	}
}

// The group itself has to be findable from the top.
func TestServerGroupIsRegistered(t *testing.T) {
	out := run(t, "--help")
	if !strings.Contains(out, "server") {
		t.Errorf("server is missing from root help:\n%s", out)
	}
}

// `vpncli servers list` is what half of everyone will type.
func TestServerGroupTakesThePlural(t *testing.T) {
	withStateDir(t)

	if _, err := execute("servers", "list"); err != nil {
		t.Errorf("`vpncli servers list`: %v", err)
	}
}

func TestListEmptyState(t *testing.T) {
	withStateDir(t)

	out := run(t, "server", "list")
	if !strings.Contains(out, "no servers found") {
		t.Errorf("got %q, want the empty message", out)
	}
}

func TestListPrintsLocalIDs(t *testing.T) {
	withStateDir(t)
	seedServers(t,
		testServer("1001", "vpncli-fra1-a1b2", "203.0.113.10", "active"),
		testServer("1002", "vpncli-ams3-c3d4", "", "provisioning"),
	)

	out := run(t, "server", "list")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want a header plus 2 rows:\n%s", len(lines), out)
	}

	// The first column is the short local id, not the droplet id.
	for _, want := range []string{"vpncli-fra1-a1b2", "203.0.113.10", "active", "3h"} {
		if !strings.Contains(out, want) {
			t.Errorf("listing is missing %q:\n%s", want, out)
		}
	}
	if strings.HasPrefix(lines[1], "1001") || strings.HasPrefix(lines[2], "1001") {
		t.Errorf("a row leads with the droplet id, want the local one:\n%s", out)
	}
}

// The whole point of `list` is that it works with no token and no network.
func TestListNeedsNoToken(t *testing.T) {
	withStateDir(t)
	t.Setenv("DIGITALOCEAN_TOKEN", "")
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", "")
	seedServers(t, testServer("1001", "vpncli-fra1-a1b2", "203.0.113.10", "active"))

	out := run(t, "server", "list")
	if !strings.Contains(out, "vpncli-fra1-a1b2") {
		t.Errorf("got %q, want the seeded server", out)
	}
}

func TestListRejectsArgs(t *testing.T) {
	withStateDir(t)

	if _, err := execute("server", "list", "unexpected"); err == nil {
		t.Error("expected an error for an unexpected positional argument")
	}
}

// storedServers reads back what a command left in the state database.
func storedServers(t *testing.T) []state.Server {
	t.Helper()

	store, err := openStore()
	if err != nil {
		t.Fatalf("opening state: %v", err)
	}
	defer store.Close()

	servers, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return servers
}
