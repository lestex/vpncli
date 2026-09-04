package cli

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/lestex/vpncli/internal/config"
	"github.com/lestex/vpncli/internal/prompt"
	"github.com/lestex/vpncli/internal/provider"
	"github.com/lestex/vpncli/internal/provider/digitalocean"
	"github.com/lestex/vpncli/internal/state"
)

// executeWith runs the root command with something on stdin, which is what the
// destroy confirmation reads.
func executeWith(in string, args ...string) (string, error) {
	var buf bytes.Buffer
	root := NewRootCommand()
	root.SetIn(strings.NewReader(in))
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	err := root.Execute()
	return buf.String(), err
}

// doomedServer is the row the destroy tests work on.
func doomedServer() state.Server {
	return testServer("1001", "vpncli-fra1-a1b2c3", "203.0.113.10", string(provider.StatusActive))
}

func TestDestroyIsRegistered(t *testing.T) {
	out := run(t, "server", "--help")
	if !strings.Contains(out, "destroy") {
		t.Errorf("destroy is missing from `vpncli server` help:\n%s", out)
	}
}

func TestDestroyNeedsExactlyOneID(t *testing.T) {
	withStateDir(t)

	for _, args := range [][]string{{"server", "destroy"}, {"server", "destroy", "1", "2"}} {
		if _, err := execute(args...); err == nil {
			t.Errorf("%v was accepted, want an error", args)
		}
	}
}

// The id column is the short local one. A provider id typed by mistake is not
// a number this can look up, and saying so beats a confusing "not found".
func TestDestroyRejectsAnIDThatIsNotANumber(t *testing.T) {
	withStateDir(t)

	_, err := execute("server", "destroy", "vpncli-fra1-a1b2c3")
	if err == nil || !strings.Contains(err.Error(), "not a server id") {
		t.Fatalf("got %v, want an error explaining what an id is", err)
	}
}

func TestDestroyUnknownServer(t *testing.T) {
	withStateDir(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if _, err := execute("server", "destroy", "42"); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("got %v, want state.ErrNotFound", err)
	}
}

// Destroying is irreversible, so it is confirmed. Turning it down must not
// even reach the API - here there is no token, and no error comes back.
func TestDestroyAsksFirst(t *testing.T) {
	withStateDir(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("DIGITALOCEAN_TOKEN", "")
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", "")
	seedServers(t, doomedServer())

	out, err := executeWith("no\n", "server", "destroy", "1")
	if err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if !strings.Contains(out, "vpncli-fra1-a1b2c3") || !strings.Contains(out, "203.0.113.10") {
		t.Errorf("the confirmation does not name the server:\n%s", out)
	}
	if !strings.Contains(out, "left alone") {
		t.Errorf("a declined destroy does not say so:\n%s", out)
	}

	if servers := storedServers(t); len(servers) != 1 {
		t.Errorf("state holds %+v, want the row kept", servers)
	}
}

// --yes is what a script uses, and it must not stop to ask.
func TestDestroyWithYesGoesStraightToTheProvider(t *testing.T) {
	withStateDir(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("DIGITALOCEAN_TOKEN", "")
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", "")
	seedServers(t, doomedServer())

	out, err := executeWith("", "server", "destroy", "1", "--yes")
	if !errors.Is(err, digitalocean.ErrNoToken) {
		t.Fatalf("got %v, want the token error from the API step", err)
	}
	if strings.Contains(out, "Type yes") {
		t.Errorf("--yes still asked:\n%s", out)
	}
}

func TestConfirmDestroy(t *testing.T) {
	tests := []struct {
		answer string
		want   bool
	}{
		{answer: "yes\n", want: true},
		{answer: "YES\n", want: true},
		{answer: "  yes  \n", want: true},
		{answer: "no\n"},
		// Anything short of the word is a no, including the y that other tools
		// take: this is not the prompt to answer by reflex.
		{answer: "y\n"},
		{answer: "\n"},
		// A script piping nothing in has not agreed to anything.
		{answer: ""},
	}

	for _, tt := range tests {
		var out bytes.Buffer
		got, err := confirmDestroy(context.Background(), prompt.New(strings.NewReader(tt.answer), &out), doomedServer())
		if err != nil {
			t.Fatalf("confirmDestroy(%q): %v", tt.answer, err)
		}
		if got != tt.want {
			t.Errorf("confirmDestroy(%q) = %v, want %v", tt.answer, got, tt.want)
		}
	}
}

func TestDestroyHelpSaysWhichIDItTakes(t *testing.T) {
	out := run(t, "server", "destroy", "--help")
	for _, want := range []string{"vpncli server list", "local"} {
		if !strings.Contains(out, want) {
			t.Errorf("destroy help does not mention %q:\n%s", want, out)
		}
	}
}

// destroy runs the command against a fake provider and a real state store.
func destroy(t *testing.T, vps *fakeProvider, answer string, id int64, yes bool) (string, error) {
	t.Helper()

	var out bytes.Buffer
	err := runDestroy(context.Background(), strings.NewReader(answer), &out,
		func(config.Config) (provider.VPSProvider, error) { return vps, nil }, id, yes)
	return out.String(), err
}

func TestDestroyDeletesTheServerAndTheRow(t *testing.T) {
	withStateDir(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	seedServers(t, doomedServer())

	vps := testProvider()
	out, err := destroy(t, vps, "yes\n", 1, false)
	if err != nil {
		t.Fatalf("runDestroy: %v", err)
	}

	if !slices.Equal(vps.deleted, []string{"1001"}) {
		t.Errorf("deleted %v, want the provider id of the row", vps.deleted)
	}
	if !strings.Contains(out, "destroyed vpncli-fra1-a1b2c3") {
		t.Errorf("the outcome is not reported:\n%s", out)
	}
	if servers := storedServers(t); len(servers) != 0 {
		t.Errorf("state holds %+v, want the row gone", servers)
	}
}

// A delete that genuinely failed must leave the row: a server nothing knows
// about is one that bills forever.
func TestDestroyKeepsTheRowWhenTheProviderFails(t *testing.T) {
	withStateDir(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	seedServers(t, doomedServer())

	vps := testProvider()
	vps.deleteErr = errors.New("500 internal server error")

	if _, err := destroy(t, vps, "yes\n", 1, false); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("got %v, want the provider error", err)
	}
	if servers := storedServers(t); len(servers) != 1 {
		t.Errorf("state holds %+v, want the row kept", servers)
	}
}

// The row is what needs clearing when the server is already gone.
func TestDestroyClearsTheRowWhenTheServerIsAlreadyGone(t *testing.T) {
	withStateDir(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	seedServers(t, doomedServer())

	vps := testProvider()
	vps.deleteErr = provider.ErrNotFound

	if _, err := destroy(t, vps, "yes\n", 1, false); err != nil {
		t.Fatalf("runDestroy: %v", err)
	}
	if servers := storedServers(t); len(servers) != 0 {
		t.Errorf("state holds %+v, want the row gone", servers)
	}
}

// Declining has to reach neither the API nor the row.
func TestDestroyDeclinedTouchesNothing(t *testing.T) {
	withStateDir(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	seedServers(t, doomedServer())

	vps := testProvider()
	if _, err := destroy(t, vps, "no\n", 1, false); err != nil {
		t.Fatalf("runDestroy: %v", err)
	}

	if len(vps.deleted) != 0 {
		t.Errorf("deleted %v after the question was turned down", vps.deleted)
	}
	if servers := storedServers(t); len(servers) != 1 {
		t.Errorf("state holds %+v, want the row kept", servers)
	}
}
