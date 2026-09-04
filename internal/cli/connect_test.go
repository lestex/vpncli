package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lestex/vpncli/internal/client"
	"github.com/lestex/vpncli/internal/state"
)

// connectableCredentials is what a bootstrap left behind, for the tests that
// need a server somebody could actually connect to.
var connectableCredentials = state.Credentials{
	UUID:       "1e089a02-6d47-40e9-a61b-16ab4dadb97d",
	PrivateKey: "cJfBGaGmB6cQpRnLxTt6qkZKmsxk4nB1FvJ8mQnZ3F4",
	PublicKey:  "_NPSjQCr1-4xWfmNOCnhPx0moZusb4ND4s-f6FpX0VM",
	ShortID:    "f2671bb145bdd37e",
	Dest:       "www.apple.com:443",
	ServerName: "www.apple.com",
}

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

	if err := store.SaveBootstrap(context.Background(), 1, connectableCredentials); err != nil {
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
	return connectAs(t, id, asQR, asSingBox, client.Proxy)
}

func connectAs(t *testing.T, id int64, asQR, asSingBox bool, mode client.Mode) (string, error) {
	t.Helper()
	return connectTo(t, id, asQR, asSingBox, mode, "")
}

func connectTo(t *testing.T, id int64, asQR, asSingBox bool, mode client.Mode, path string) (string, error) {
	t.Helper()

	var out bytes.Buffer
	err := runConnect(context.Background(), &out, id, asQR, asSingBox, mode, path)
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

// A proxy tunnels only what is pointed at it. --tun is for the rest of the
// machine, and the difference is the inbound.
func TestConnectSingBoxTun(t *testing.T) {
	srv := connectable(t)

	out, err := connectAs(t, srv.ID, false, true, client.Tun)
	if err != nil {
		t.Fatalf("connect --tun: %v", err)
	}

	var config struct {
		DNS struct {
			Servers []struct {
				Detour string `json:"detour"`
			} `json:"servers"`
		} `json:"dns"`
		Inbounds []struct {
			Type      string `json:"type"`
			AutoRoute bool   `json:"auto_route"`
		} `json:"inbounds"`
		Route struct {
			AutoDetectInterface bool `json:"auto_detect_interface"`
		} `json:"route"`
	}
	if err := json.Unmarshal([]byte(out), &config); err != nil {
		t.Fatalf("the output is not valid JSON: %v\n%s", err, out)
	}

	if len(config.Inbounds) != 1 || config.Inbounds[0].Type != "tun" {
		t.Fatalf("inbounds = %+v, want one tun", config.Inbounds)
	}
	if !config.Inbounds[0].AutoRoute {
		t.Error("the interface is created but nothing is routed through it")
	}
	// Without this the tunnel routes its own connection to the server into
	// itself, and nothing works at all.
	if !config.Route.AutoDetectInterface {
		t.Error("the route to the server is not kept outside the tunnel")
	}
	// Lookups going out over the local network would be the one thing still in
	// plain sight.
	if len(config.DNS.Servers) == 0 || config.DNS.Servers[0].Detour != "proxy" {
		t.Errorf("DNS = %+v, want it sent through the tunnel", config.DNS)
	}
}

// Proxy mode is the one that needs no privileges, so it stays free of the
// routing and DNS a tun config has to take over.
func TestConnectSingBoxProxyStaysMinimal(t *testing.T) {
	srv := connectable(t)

	out, err := connect(t, srv.ID, false, true)
	if err != nil {
		t.Fatalf("connect --sing-box: %v", err)
	}

	for _, absent := range []string{"tun", "auto_route", "\"dns\"", "auto_detect_interface"} {
		if strings.Contains(out, absent) {
			t.Errorf("the proxy config carries %q, which only tun mode needs:\n%s", absent, out)
		}
	}
}

// A client config is the key to the server. Shell redirection leaves it
// readable by anyone with an account on the machine; this does not.
func TestConnectWritesTheConfigUnreadableByOthers(t *testing.T) {
	srv := connectable(t)
	path := filepath.Join(t.TempDir(), "vpn.json")

	out, err := connectTo(t, srv.ID, false, true, client.Proxy, path)
	if err != nil {
		t.Fatalf("connect -o: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("%s is %04o, want 0600", path, perm)
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading it back: %v", err)
	}
	var config map[string]any
	if err := json.Unmarshal(written, &config); err != nil {
		t.Fatalf("what was written is not valid JSON: %v", err)
	}

	// Nothing but the report goes to stdout: the config is in the file.
	if strings.Contains(out, "\"outbounds\"") {
		t.Errorf("the config was printed as well as written:\n%s", out)
	}
	if !strings.Contains(out, path) {
		t.Errorf("the command does not say where it wrote:\n%s", out)
	}
}

// Running a tun config without root creates no interface and tunnels nothing,
// which is a hard failure to recognize. The command that writes it says so.
func TestConnectSaysHowToRunWhatItWrote(t *testing.T) {
	srv := connectable(t)
	dir := t.TempDir()

	tun, err := connectTo(t, srv.ID, false, true, client.Tun, filepath.Join(dir, "tun.json"))
	if err != nil {
		t.Fatalf("connect --tun -o: %v", err)
	}
	if !strings.Contains(tun, "sudo sing-box run") {
		t.Errorf("a tun config is not shown with root:\n%s", tun)
	}

	proxy, err := connectTo(t, srv.ID, false, true, client.Proxy, filepath.Join(dir, "proxy.json"))
	if err != nil {
		t.Fatalf("connect --sing-box -o: %v", err)
	}
	if strings.Contains(proxy, "sudo") {
		t.Errorf("a proxy config does not need root:\n%s", proxy)
	}
	if !strings.Contains(proxy, "127.0.0.1") {
		t.Errorf("a proxy config is not shown with its address:\n%s", proxy)
	}
}

// A path that was written before with the other mode has to end up as what was
// asked for now, not as whatever is already there.
func TestConnectReplacesAConfigOfTheOtherMode(t *testing.T) {
	srv := connectable(t)
	path := filepath.Join(t.TempDir(), "vpn.json")

	if _, err := connectTo(t, srv.ID, false, true, client.Proxy, path); err != nil {
		t.Fatalf("writing the proxy config: %v", err)
	}
	if _, err := connectTo(t, srv.ID, false, true, client.Tun, path); err != nil {
		t.Fatalf("writing the tun config: %v", err)
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading it back: %v", err)
	}
	if !strings.Contains(string(written), `"tun"`) || strings.Contains(string(written), `"mixed"`) {
		t.Errorf("the file still holds the old mode:\n%s", written)
	}
}

func TestConnectWritesTheLinkToAFileToo(t *testing.T) {
	srv := connectable(t)
	path := filepath.Join(t.TempDir(), "link.txt")

	if _, err := connectTo(t, srv.ID, false, false, client.Proxy, path); err != nil {
		t.Fatalf("connect -o: %v", err)
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading it back: %v", err)
	}
	if !strings.HasPrefix(string(written), "vless://") {
		t.Errorf("the file holds %q, want the link", written)
	}
}
