package bootstrap

import (
	"encoding/json"
	"fmt"
)

// Port is where the server accepts clients. It is 443 and not configurable:
// REALITY works by being indistinguishable from a visit to a real TLS site,
// and real TLS sites are on 443. Anywhere else is a flag in itself.
const Port = 443

// Flow is the transport the client and server agree on. Vision is what makes
// REALITY worth using - without it the inner TLS is visible as a second
// handshake inside the first.
const Flow = "xtls-rprx-vision"

// xrayConfig is the server config, in the shape Xray reads it. It is built
// from structs rather than a text template so that a change to a field name is
// a compile error rather than a server that starts and rejects everyone.
type xrayConfig struct {
	Log       xrayLog        `json:"log"`
	Inbounds  []xrayInbound  `json:"inbounds"`
	Outbounds []xrayOutbound `json:"outbounds"`
	Routing   xrayRouting    `json:"routing"`
}

type xrayLog struct {
	// Warning and no access log: a server whose whole purpose is to be
	// unremarkable should not be keeping a record of who connected and when.
	LogLevel  string `json:"loglevel"`
	AccessLog string `json:"access"`
}

type xrayInbound struct {
	Listen         string             `json:"listen"`
	Port           int                `json:"port"`
	Protocol       string             `json:"protocol"`
	Settings       xrayInboundConfig  `json:"settings"`
	StreamSettings xrayStreamSettings `json:"streamSettings"`
	Sniffing       xraySniffing       `json:"sniffing"`
}

type xrayInboundConfig struct {
	Clients    []xrayClient `json:"clients"`
	Decryption string       `json:"decryption"`
}

type xrayClient struct {
	ID   string `json:"id"`
	Flow string `json:"flow"`
}

type xrayStreamSettings struct {
	Network         string              `json:"network"`
	Security        string              `json:"security"`
	RealitySettings xrayRealitySettings `json:"realitySettings"`
}

type xrayRealitySettings struct {
	// Show logs the handshake, which is a debugging aid and a record of who
	// connected. It stays off.
	Show bool `json:"show"`
	// Dest is the real site whose handshake this server borrows, and where
	// anything that is not one of our clients is quietly forwarded.
	Dest        string   `json:"dest"`
	ServerNames []string `json:"serverNames"`
	PrivateKey  string   `json:"privateKey"`
	ShortIDs    []string `json:"shortIds"`
}

type xraySniffing struct {
	Enabled      bool     `json:"enabled"`
	DestOverride []string `json:"destOverride"`
	// RouteOnly keeps sniffing to what routing needs and stops it rewriting
	// the destination, which breaks connections to an address a client
	// resolved for itself.
	RouteOnly bool `json:"routeOnly"`
}

type xrayOutbound struct {
	Protocol string `json:"protocol"`
	Tag      string `json:"tag"`
}

type xrayRouting struct {
	DomainStrategy string     `json:"domainStrategy"`
	Rules          []xrayRule `json:"rules"`
}

type xrayRule struct {
	Type        string   `json:"type"`
	IP          []string `json:"ip,omitempty"`
	OutboundTag string   `json:"outboundTag"`
}

// serverConfig renders the config for one server.
func serverConfig(opts Options) ([]byte, error) {
	config := xrayConfig{
		Log: xrayLog{LogLevel: "warning", AccessLog: "none"},
		Inbounds: []xrayInbound{{
			Listen:   "0.0.0.0",
			Port:     Port,
			Protocol: "vless",
			Settings: xrayInboundConfig{
				// One server, one client. There is no user management here:
				// another person means another server, which costs the price
				// of a coffee and shares nothing with this one.
				Clients:    []xrayClient{{ID: opts.Material.UUID, Flow: Flow}},
				Decryption: "none",
			},
			StreamSettings: xrayStreamSettings{
				Network:  "tcp",
				Security: "reality",
				RealitySettings: xrayRealitySettings{
					Show:        false,
					Dest:        opts.Dest,
					ServerNames: []string{opts.ServerName},
					PrivateKey:  opts.Material.PrivateKey,
					ShortIDs:    []string{opts.Material.ShortID},
				},
			},
			Sniffing: xraySniffing{
				Enabled:      true,
				DestOverride: []string{"http", "tls", "quic"},
				RouteOnly:    true,
			},
		}},
		Outbounds: []xrayOutbound{
			{Protocol: "freedom", Tag: "direct"},
			{Protocol: "blackhole", Tag: "blocked"},
		},
		Routing: xrayRouting{
			DomainStrategy: "AsIs",
			Rules: []xrayRule{{
				// A tunnel that can reach the provider's metadata service is a
				// tunnel that can hand out the account's own credentials, and
				// nothing a VPN client wants is on a private address.
				Type:        "field",
				IP:          []string{"geoip:private"},
				OutboundTag: "blocked",
			}},
		},
	}

	rendered, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("rendering the Xray config: %w", err)
	}
	return append(rendered, '\n'), nil
}
