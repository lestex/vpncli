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

// SocksPort is where a generated sing-box config listens. It is on loopback
// and above 1024, so the client needs no privileges and nothing else on the
// network can reach it.
const SocksPort = 1080

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
		return fmt.Errorf("%w: `vpncli bootstrap %d` configures it", ErrNotConfigured, srv.ID)
	}
	return nil
}

// singBoxConfig is the shape sing-box reads. Only the fields that matter are
// here: a config with less in it is a config with less to be wrong.
type singBoxConfig struct {
	Log       singBoxLog       `json:"log"`
	Inbounds  []singBoxInbound `json:"inbounds"`
	Outbounds []any            `json:"outbounds"`
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
// It listens as a SOCKS and HTTP proxy on loopback rather than creating a tun
// device: a proxy needs no root, and pointing one browser at it is the usual
// reason to want a config at all. Someone who wants the whole machine tunneled
// can swap the inbound for a tun one.
func SingBox(srv state.Server) ([]byte, error) {
	if err := usable(srv); err != nil {
		return nil, err
	}

	config := singBoxConfig{
		Log: singBoxLog{Level: "warn"},
		Inbounds: []singBoxInbound{{
			// "mixed" is SOCKS and HTTP on the same port, which between them
			// cover everything that takes a proxy setting.
			Type:       "mixed",
			Tag:        "in",
			Listen:     "127.0.0.1",
			ListenPort: SocksPort,
		}},
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

	rendered, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("rendering the sing-box config: %w", err)
	}
	return append(rendered, '\n'), nil
}
