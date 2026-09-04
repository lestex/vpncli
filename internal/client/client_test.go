package client

import (
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lestex/vpncli/internal/bootstrap"
	"github.com/lestex/vpncli/internal/state"
)

// configured is a server as it looks after a bootstrap.
func configured() state.Server {
	return state.Server{
		ID:             3,
		Name:           "vpncli-ams3-0a910d",
		IPv4:           "203.0.113.10",
		BootstrappedAt: time.Now().UTC(),
		Credentials: state.Credentials{
			UUID:       "1e089a02-6d47-40e9-a61b-16ab4dadb97d",
			PrivateKey: "cJfBGaGmB6cQpRnLxTt6qkZKmsxk4nB1FvJ8mQnZ3F4",
			PublicKey:  "_NPSjQCr1-4xWfmNOCnhPx0moZusb4ND4s-f6FpX0VM",
			ShortID:    "f2671bb145bdd37e",
			Dest:       "www.microsoft.com:443",
			ServerName: "www.microsoft.com",
		},
	}
}

func TestURI(t *testing.T) {
	srv := configured()

	raw, err := URI(srv)
	if err != nil {
		t.Fatalf("URI: %v", err)
	}

	link, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("the link does not parse: %v", err)
	}

	if link.Scheme != "vless" {
		t.Errorf("scheme = %q, want vless", link.Scheme)
	}
	if link.User.Username() != srv.Credentials.UUID {
		t.Errorf("user = %q, want the client UUID", link.User.Username())
	}
	if link.Host != "203.0.113.10:443" {
		t.Errorf("host = %q, want the address and port", link.Host)
	}
	// The fragment is the name a client shows in its server list.
	if link.Fragment != srv.Name {
		t.Errorf("fragment = %q, want %q", link.Fragment, srv.Name)
	}

	want := map[string]string{
		"encryption": "none",
		"security":   "reality",
		"sni":        "www.microsoft.com",
		"fp":         Fingerprint,
		"pbk":        srv.Credentials.PublicKey,
		"sid":        srv.Credentials.ShortID,
		"flow":       bootstrap.Flow,
		"type":       "tcp",
	}
	query := link.Query()
	for key, value := range want {
		if got := query.Get(key); got != value {
			t.Errorf("%s = %q, want %q", key, got, value)
		}
	}
}

// A key is base64url and routinely contains - and _. A link that mangles one
// fails at the handshake, where the message says nothing useful.
func TestURIKeepsTheKeyIntact(t *testing.T) {
	srv := configured()

	raw, err := URI(srv)
	if err != nil {
		t.Fatalf("URI: %v", err)
	}

	link, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("the link does not parse: %v", err)
	}
	if got := link.Query().Get("pbk"); got != srv.Credentials.PublicKey {
		t.Errorf("public key came back as %q, want %q", got, srv.Credentials.PublicKey)
	}
}

// The private key belongs on the server and in local state. A client presents
// the public half and needs nothing else.
func TestURICarriesNoPrivateKey(t *testing.T) {
	srv := configured()

	raw, err := URI(srv)
	if err != nil {
		t.Fatalf("URI: %v", err)
	}
	if strings.Contains(raw, srv.Credentials.PrivateKey) {
		t.Errorf("the link carries the server's private key:\n%s", raw)
	}
}

func TestURINeedsAConfiguredServer(t *testing.T) {
	var srv state.Server
	srv.ID = 3
	srv.IPv4 = "203.0.113.10"

	_, err := URI(srv)
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("got %v, want ErrNotConfigured", err)
	}
	if !strings.Contains(err.Error(), "vpncli server bootstrap 3") {
		t.Errorf("error %q does not say how to fix it", err)
	}
}

// A server with no address is one sync has not caught up with.
func TestURINeedsAnAddress(t *testing.T) {
	srv := configured()
	srv.IPv4 = ""

	_, err := URI(srv)
	if err == nil || !strings.Contains(err.Error(), "vpncli sync") {
		t.Fatalf("got %v, want an error pointing at sync", err)
	}
}

func TestSingBox(t *testing.T) {
	srv := configured()

	raw, err := SingBox(srv, Proxy)
	if err != nil {
		t.Fatalf("SingBox: %v", err)
	}

	var config struct {
		Inbounds []struct {
			Type       string `json:"type"`
			Listen     string `json:"listen"`
			ListenPort int    `json:"listen_port"`
		} `json:"inbounds"`
		Outbounds []struct {
			Type       string `json:"type"`
			Server     string `json:"server"`
			ServerPort int    `json:"server_port"`
			UUID       string `json:"uuid"`
			Flow       string `json:"flow"`
			TLS        struct {
				Enabled    bool   `json:"enabled"`
				ServerName string `json:"server_name"`
				UTLS       struct {
					Enabled     bool   `json:"enabled"`
					Fingerprint string `json:"fingerprint"`
				} `json:"utls"`
				Reality struct {
					Enabled   bool   `json:"enabled"`
					PublicKey string `json:"public_key"`
					ShortID   string `json:"short_id"`
				} `json:"reality"`
			} `json:"tls"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatalf("the config is not valid JSON: %v", err)
	}

	if len(config.Inbounds) != 1 {
		t.Fatalf("got %d inbounds, want one", len(config.Inbounds))
	}
	in := config.Inbounds[0]
	// Loopback and unprivileged: a proxy the browser can use without root, and
	// which nothing else on the network can reach.
	if in.Listen != "127.0.0.1" {
		t.Errorf("listening on %q, want loopback only", in.Listen)
	}
	if in.ListenPort != SocksPort || in.ListenPort < 1024 {
		t.Errorf("listening on port %d, want %d", in.ListenPort, SocksPort)
	}

	if len(config.Outbounds) != 2 {
		t.Fatalf("got %d outbounds, want the proxy and a direct one", len(config.Outbounds))
	}
	out := config.Outbounds[0]
	if out.Type != "vless" || out.Server != srv.IPv4 || out.ServerPort != bootstrap.Port {
		t.Errorf("outbound = %+v, want the server", out)
	}
	if out.UUID != srv.Credentials.UUID || out.Flow != bootstrap.Flow {
		t.Errorf("outbound = %+v, want the client identity the server was given", out)
	}
	if !out.TLS.Enabled || !out.TLS.Reality.Enabled || !out.TLS.UTLS.Enabled {
		t.Errorf("tls = %+v, want REALITY and uTLS both on", out.TLS)
	}
	// Any of these three not matching the server means a handshake that is
	// forwarded to the camouflage site and never reaches the tunnel.
	if out.TLS.ServerName != srv.Credentials.ServerName {
		t.Errorf("sni = %q, want %q", out.TLS.ServerName, srv.Credentials.ServerName)
	}
	if out.TLS.Reality.PublicKey != srv.Credentials.PublicKey {
		t.Errorf("public key = %q, want the server's", out.TLS.Reality.PublicKey)
	}
	if out.TLS.Reality.ShortID != srv.Credentials.ShortID {
		t.Errorf("short id = %q, want the server's", out.TLS.Reality.ShortID)
	}
}

func TestSingBoxCarriesNoPrivateKey(t *testing.T) {
	srv := configured()

	raw, err := SingBox(srv, Proxy)
	if err != nil {
		t.Fatalf("SingBox: %v", err)
	}
	if strings.Contains(string(raw), srv.Credentials.PrivateKey) {
		t.Errorf("the client config carries the server's private key:\n%s", raw)
	}
}

func TestSingBoxNeedsAConfiguredServer(t *testing.T) {
	if _, err := SingBox(state.Server{ID: 3, IPv4: "203.0.113.10"}, Proxy); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("got %v, want ErrNotConfigured", err)
	}
}

// Tun mode is the same tunnel with a different way in, so the credentials must
// come out identical - a difference here would be a second code path to keep
// right.
func TestSingBoxTunCarriesTheSameOutbound(t *testing.T) {
	srv := configured()

	proxy, err := SingBox(srv, Proxy)
	if err != nil {
		t.Fatalf("SingBox(Proxy): %v", err)
	}
	tun, err := SingBox(srv, Tun)
	if err != nil {
		t.Fatalf("SingBox(Tun): %v", err)
	}

	outbound := func(raw []byte) any {
		var c struct {
			Outbounds []any `json:"outbounds"`
		}
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatalf("parsing: %v", err)
		}
		return c.Outbounds[0]
	}
	if !reflect.DeepEqual(outbound(proxy), outbound(tun)) {
		t.Errorf("the two modes describe different servers:\n%s\n%s", proxy, tun)
	}

	if strings.Contains(string(tun), srv.Credentials.PrivateKey) {
		t.Error("the tun config carries the server's private key")
	}
}
