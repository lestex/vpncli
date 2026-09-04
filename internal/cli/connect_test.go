package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/lestex/vpncli/internal/client"
	"github.com/lestex/vpncli/internal/state"
)

// connectable seeds a configured server and returns it.
func connectable(t *testing.T) state.Server {
	t.Helper()
	withStateDir(t)
	seedServers(t, doomedServer())

	store, err := openStore()
	if err != nil {
		t.Fatalf("opening state: %v", err)
	}
	defer store.Close()

	credentials := state.Credentials{
		UUID:       "1e089a02-6d47-40e9-a61b-16ab4dadb97d",
		PrivateKey: "cJfBGaGmB6cQpRnLxTt6qkZKmsxk4nB1FvJ8mQnZ3F4",
		PublicKey:  "_NPSjQCr1-4xWfmNOCnhPx0moZusb4ND4s-f6FpX0VM",
		ShortID:    "f2671bb145bdd37e",
		Dest:       "www.microsoft.com:443",
		ServerName: "www.microsoft.com",
	}
	if err := store.SaveBootstrap(context.Background(), 1, credentials); err != nil {
		t.Fatalf("recording the bootstrap: %v", err)
	}

	srv, err := store.Get(context.Background(), 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return srv
}

func connect(t *testing.T, id int64, asQR, asSingBox bool) (string, error) {
	t.Helper()

	var out bytes.Buffer
	err := runConnect(context.Background(), &out, id, asQR, asSingBox)
	return out.String(), err
}

func TestConnectIsRegistered(t *testing.T) {
	out := run(t, "--help")
	if !strings.Contains(out, "connect") {
		t.Errorf("connect is missing from root help:\n%s", out)
	}
}

func TestConnectNeedsExactlyOneID(t *testing.T) {
	withStateDir(t)

	for _, args := range [][]string{{"connect"}, {"connect", "1", "2"}} {
		if _, err := execute(args...); err == nil {
			t.Errorf("%v was accepted, want an error", args)
		}
	}
}

// The link is the whole output, so `vpncli connect 3 | pbcopy` copies a link
// and not a paragraph about one.
func TestConnectPrintsOnlyTheLink(t *testing.T) {
	srv := connectable(t)

	out, err := connect(t, srv.ID, false, false)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	line := strings.TrimSpace(out)
	if strings.Contains(line, "\n") {
		t.Errorf("more than one line was printed:\n%s", out)
	}

	link, err := url.Parse(line)
	if err != nil || link.Scheme != "vless" {
		t.Fatalf("output is not a vless link: %q", line)
	}
	if link.Query().Get("pbk") != srv.Credentials.PublicKey {
		t.Errorf("the link does not carry this server's key: %q", line)
	}
}

func TestConnectQR(t *testing.T) {
	srv := connectable(t)

	out, err := connect(t, srv.ID, true, false)
	if err != nil {
		t.Fatalf("connect --qr: %v", err)
	}

	if !strings.Contains(out, upperHalf) {
		t.Errorf("nothing was drawn:\n%s", out)
	}
	// The link goes under the code: a scanner that will not focus is common
	// enough that having something to copy is worth the two lines.
	if !strings.Contains(out, "vless://") {
		t.Errorf("the link is not printed alongside the code:\n%s", out)
	}
	// The SNI is the field that silently breaks a connection when it is wrong.
	if !strings.Contains(out, srv.Credentials.ServerName) {
		t.Errorf("the camouflage is not named:\n%s", out)
	}
}

func TestConnectSingBox(t *testing.T) {
	srv := connectable(t)

	out, err := connect(t, srv.ID, false, true)
	if err != nil {
		t.Fatalf("connect --sing-box: %v", err)
	}

	var config map[string]any
	if err := json.Unmarshal([]byte(out), &config); err != nil {
		t.Fatalf("the output is not valid JSON: %v\n%s", err, out)
	}
	if !strings.Contains(out, srv.Credentials.UUID) {
		t.Errorf("the config is not for this server:\n%s", out)
	}
	// Only the config, so it can be redirected straight into a file.
	if strings.Contains(out, "vless://") {
		t.Errorf("the config output carries commentary:\n%s", out)
	}
}

// Two different things to print, and no sensible way to do both at once.
func TestConnectRefusesBothFormats(t *testing.T) {
	connectable(t)

	if _, err := execute("connect", "1", "--qr", "--sing-box"); err == nil {
		t.Error("both formats were accepted, want an error")
	}
}

// A server that exists but was never configured has nothing to connect to, and
// the fix is a command rather than an edit.
func TestConnectOnAnUnconfiguredServer(t *testing.T) {
	withStateDir(t)
	seedServers(t, doomedServer())

	_, err := connect(t, 1, false, false)
	if !errors.Is(err, client.ErrNotConfigured) {
		t.Fatalf("got %v, want ErrNotConfigured", err)
	}
	if !strings.Contains(err.Error(), "vpncli bootstrap 1") {
		t.Errorf("error %q does not say how to fix it", err)
	}
}

func TestConnectUnknownServer(t *testing.T) {
	withStateDir(t)

	if _, err := connect(t, 42, false, false); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("got %v, want state.ErrNotFound", err)
	}
}

// No API call and no SSH: the moment you want a config is usually the moment
// the network is unpleasant.
func TestConnectNeedsNoToken(t *testing.T) {
	srv := connectable(t)
	t.Setenv("DIGITALOCEAN_TOKEN", "")
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", "")

	if _, err := connect(t, srv.ID, false, false); err != nil {
		t.Fatalf("connect without a token: %v", err)
	}
}

func TestConnectHelpSaysWhatItPrints(t *testing.T) {
	out := run(t, "connect", "--help")
	for _, want := range []string{"vless://", "--qr", "--sing-box", "offline"} {
		if !strings.Contains(out, want) {
			t.Errorf("connect help does not mention %q:\n%s", want, out)
		}
	}
}
