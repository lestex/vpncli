package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/lestex/vpncli/internal/provider/digitalocean"
)

// execute runs the command tree and returns its output and error, unlike
// run(), which fails the test on error.
func execute(args ...string) (string, error) {
	var buf bytes.Buffer
	root := NewRootCommand()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	err := root.Execute()
	return buf.String(), err
}

func TestProvidersCommandIsRegistered(t *testing.T) {
	out := run(t, "--help")
	if !strings.Contains(out, "providers") {
		t.Errorf("providers is missing from root help:\n%s", out)
	}
}

func TestDigitalOceanRequiresToken(t *testing.T) {
	// The command should name the variables, not fail inside an API call.
	t.Setenv("DIGITALOCEAN_TOKEN", "")
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", "")

	_, err := execute("providers", "do")
	if !errors.Is(err, digitalocean.ErrNoToken) {
		t.Fatalf("got %v, want ErrNoToken", err)
	}
	for _, want := range []string{"DIGITALOCEAN_TOKEN", "DIGITALOCEAN_ACCESS_TOKEN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message does not name %s: %v", want, err)
		}
	}
}

func TestDigitalOceanAlias(t *testing.T) {
	// The full name should work as well as the `do` short form.
	out := run(t, "providers", "digitalocean", "--help")
	if !strings.Contains(out, "droplet") {
		t.Errorf("digitalocean alias did not resolve to the provider command:\n%s", out)
	}
}

// The listing is the command itself now, not something under it.
func TestDigitalOceanHasNoSubcommands(t *testing.T) {
	if _, err := execute("providers", "do", "list"); err == nil {
		t.Error("`providers do list` was accepted, want the listing to be `providers do`")
	}
}

func TestDigitalOceanRejectsArgs(t *testing.T) {
	if _, err := execute("providers", "do", "unexpected"); err == nil {
		t.Error("expected an error for an unexpected positional argument")
	}
}
