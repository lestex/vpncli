// Package client turns a server's stored credentials into something a VPN
// client can import.
//
// Nothing here talks to a server. Everything a client needs was written into
// local state when the server was bootstrapped, so building a config is
// instant, works offline, and needs no API token - which matters, because the
// moment you want a config is usually the moment the network is unpleasant.
package client

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"

	"github.com/lestex/vpncli/internal/bootstrap"
	"github.com/lestex/vpncli/internal/state"
)

// Fingerprint is the TLS fingerprint a client imitates. It is a client-side
// choice - the server never sees which one was picked, only a handshake that
// has to look like a browser's - so it is a constant here rather than a
// question the wizard asks.
const Fingerprint = "chrome"

// SocksPort is where a generated sing-box config listens in proxy mode. It is
// on loopback and above 1024, so the client needs no privileges and nothing
// else on the network can reach it.
const SocksPort = 1080

// Mode is how a generated config carries traffic.
type Mode int

const (
	// Proxy listens on loopback and tunnels whatever is pointed at it. It
	// needs no privileges, and it tunnels nothing that is not configured to
	// use it - which is either the whole point or the whole problem,
	// depending on what you are doing.
	Proxy Mode = iota
	// Tun creates a virtual interface and routes the machine's traffic
	// through it, including from programs that have no proxy setting. It
	// needs root, because creating an interface and rewriting the routing
	// table does.
	Tun
)

// Tunnel addresses for Tun mode. They are private ranges chosen not to collide
// with anything ordinary: the /30 and /126 are as small as an interface with
// one address on each side can be.
var (
	tunIPv4 = "172.19.0.1/30"
	tunIPv6 = "fdfe:dcba:9876::1/126"
)

// tunResolver is where Tun mode sends DNS, through the tunnel. Left to the
// system, lookups would go out over the local network in plain sight and undo
// most of the point of tunneling the traffic that follows them.
const tunResolver = "1.1.1.1"

// ErrNotConfigured is returned for a server whose bootstrap has not run. There
// is nothing to connect to yet, and the fix is a command rather than an edit.
var ErrNotConfigured = fmt.Errorf("server is not configured")

// URI renders the vless:// link that clients import.
//
// The parameters are the ones the server was configured with, and every one of
// them has to match: a client presenting a different SNI is forwarded to the
// camouflage site and never reaches the tunnel, which looks from the outside
// like a connection that works and carries nothing.
func URI(srv state.Server) (string, error) {
	if err := usable(srv); err != nil {
		return "", err
	}

	query := url.Values{}
	query.Set("encryption", "none")
	query.Set("security", "reality")
	query.Set("sni", srv.Credentials.ServerName)
	query.Set("fp", Fingerprint)
	query.Set("pbk", srv.Credentials.PublicKey)
	query.Set("sid", srv.Credentials.ShortID)
	query.Set("flow", bootstrap.Flow)
	query.Set("type", "tcp")

	link := url.URL{
		Scheme: "vless",
		User:   url.User(srv.Credentials.UUID),
		Host:   net.JoinHostPort(srv.IPv4, strconv.Itoa(bootstrap.Port)),
		// The fragment is what a client shows in its server list.
		Fragment: srv.Name,
	}
	// Encoded query values are escaped by url.Values, which is what keeps a
	// base64url key with a - or _ in it intact.
	link.RawQuery = query.Encode()

	return link.String(), nil
}

// usable reports whether a server can be connected to at all.
func usable(srv state.Server) error {
	switch {
	case srv.IPv4 == "":
		return fmt.Errorf("server %d has no address yet: `vpncli sync` picks one up once it has booted", srv.ID)
	case !srv.Bootstrapped() || !srv.Credentials.Complete():
		return fmt.Errorf("%w: `vpncli server bootstrap %d` configures it", ErrNotConfigured, srv.ID)
	}
	return nil
}

// inbound is where traffic comes in, which is the whole difference between
// the two modes.
func inbound(mode Mode) any {
	if mode == Tun {
		return singBoxTunInbound{
			Type:        "tun",
			Tag:         "tun-in",
			Address:     []string{tunIPv4, tunIPv6},
			AutoRoute:   true,
			StrictRoute: true,
			// gvisor is a userspace network stack: slower than handing packets
			// to the kernel, and it does not need the privileges or the
			// per-platform care that the system stack does.
			Stack: "gvisor",
		}
	}
	return singBoxInbound{
		// "mixed" is SOCKS and HTTP on the same port, which between them cover
		// everything that takes a proxy setting.
		Type:       "mixed",
		Tag:        "in",
		Listen:     "127.0.0.1",
		ListenPort: SocksPort,
	}
}

// singBoxConfig is the shape sing-box reads. Only the fields that matter are
// here: a config with less in it is a config with less to be wrong.
type singBoxConfig struct {
	Log       singBoxLog    `json:"log"`
	DNS       *singBoxDNS   `json:"dns,omitempty"`
	Inbounds  []any         `json:"inbounds"`
	Outbounds []any         `json:"outbounds"`
	Route     *singBoxRoute `json:"route,omitempty"`
}

// singBoxTunInbound is the virtual interface Tun mode creates.
type singBoxTunInbound struct {
	Type    string   `json:"type"`
	Tag     string   `json:"tag"`
	Address []string `json:"address"`
	// AutoRoute writes the routes that send traffic here; StrictRoute keeps
	// it from leaking back out of the interface it came from.
	AutoRoute   bool   `json:"auto_route"`
	StrictRoute bool   `json:"strict_route"`
	Stack       string `json:"stack"`
}

type singBoxDNS struct {
	Servers []singBoxDNSServer `json:"servers"`
}

type singBoxDNSServer struct {
	Type   string `json:"type"`
	Tag    string `json:"tag"`
	Server string `json:"server"`
	// Detour sends the lookups through the tunnel rather than the local
	// network, where they would be the one thing still in plain sight.
	Detour string `json:"detour"`
}

type singBoxRoute struct {
	// AutoDetectInterface is what keeps the connection to the server itself
	// outside the tunnel it carries. Without it the tunnel routes its own
	// traffic into itself.
	AutoDetectInterface bool          `json:"auto_detect_interface"`
	Rules               []singBoxRule `json:"rules,omitempty"`
}

type singBoxRule struct {
	IPIsPrivate bool   `json:"ip_is_private"`
	Outbound    string `json:"outbound"`
}

type singBoxLog struct {
	Level string `json:"level"`
}

type singBoxInbound struct {
	Type       string `json:"type"`
	Tag        string `json:"tag"`
	Listen     string `json:"listen"`
	ListenPort int    `json:"listen_port"`
}

type singBoxOutbound struct {
	Type       string     `json:"type"`
	Tag        string     `json:"tag"`
	Server     string     `json:"server"`
	ServerPort int        `json:"server_port"`
	UUID       string     `json:"uuid"`
	Flow       string     `json:"flow"`
	TLS        singBoxTLS `json:"tls"`
}

type singBoxTLS struct {
	Enabled    bool           `json:"enabled"`
	ServerName string         `json:"server_name"`
	UTLS       singBoxUTLS    `json:"utls"`
	Reality    singBoxReality `json:"reality"`
}

type singBoxUTLS struct {
	Enabled     bool   `json:"enabled"`
	Fingerprint string `json:"fingerprint"`
}

type singBoxReality struct {
	Enabled   bool   `json:"enabled"`
	PublicKey string `json:"public_key"`
	ShortID   string `json:"short_id"`
}

type singBoxDirect struct {
	Type string `json:"type"`
	Tag  string `json:"tag"`
}

// SingBox renders a sing-box config for one server.
//
// Proxy mode listens on loopback and needs no privileges, but tunnels only
// what is pointed at it. Tun mode creates an interface and routes everything,
// including programs with no proxy setting, and has to run as root.
func SingBox(srv state.Server, mode Mode) ([]byte, error) {
	if err := usable(srv); err != nil {
		return nil, err
	}

	config := singBoxConfig{
		Log:      singBoxLog{Level: "warn"},
		Inbounds: []any{inbound(mode)},
		Outbounds: []any{
			singBoxOutbound{
				Type:       "vless",
				Tag:        "proxy",
				Server:     srv.IPv4,
				ServerPort: bootstrap.Port,
				UUID:       srv.Credentials.UUID,
				Flow:       bootstrap.Flow,
				TLS: singBoxTLS{
					Enabled:    true,
					ServerName: srv.Credentials.ServerName,
					UTLS: singBoxUTLS{
						Enabled:     true,
						Fingerprint: Fingerprint,
					},
					Reality: singBoxReality{
						Enabled:   true,
						PublicKey: srv.Credentials.PublicKey,
						ShortID:   srv.Credentials.ShortID,
					},
				},
			},
			singBoxDirect{Type: "direct", Tag: "direct"},
		},
	}

	if mode == Tun {
		// Only Tun needs these. A proxy carries what is handed to it and
		// resolves nothing on its own, so DNS and routing stay the system's
		// business.
		config.DNS = &singBoxDNS{Servers: []singBoxDNSServer{{
			Type:   "udp",
			Tag:    "tunnel-dns",
			Server: tunResolver,
			Detour: "proxy",
		}}}
		config.Route = &singBoxRoute{
			AutoDetectInterface: true,
			// Everything on the local network stays on it. The server drops
			// traffic to private addresses on purpose - a tunnel that can
			// reach the provider's metadata service can hand out the
			// account's credentials - so without this rule a printer, a NAS
			// or a router page is not slow or blocked, it is a connection
			// refused from three countries away.
			Rules: []singBoxRule{{IPIsPrivate: true, Outbound: "direct"}},
		}
	}

	rendered, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("rendering the sing-box config: %w", err)
	}
	return append(rendered, '\n'), nil
}
