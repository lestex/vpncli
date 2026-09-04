// Package bootstrap turns a bare cloud image into a working VLESS+REALITY
// server, over an SSH connection somebody else opened.
//
// Everything here is idempotent. A bootstrap that fails halfway is re-run
// rather than repaired, and re-running it on a server that is already
// configured replaces the config with fresh key material - which is what makes
// a failed provision recoverable without destroying anything.
//
// Nothing is put in cloud-init user data. Provider metadata services are
// readable from inside the server and are logged by the provider, so the key
// material goes over SSH after boot instead.
package bootstrap

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/lestex/vpncli/internal/reality"
)

// Runner is the part of an SSH connection the bootstrap needs.
type Runner interface {
	Run(ctx context.Context, command string) (string, error)
	Upload(ctx context.Context, path string, mode os.FileMode, content []byte) error
}

// Options is what one server is configured with.
type Options struct {
	// Material is the key material this server will accept exactly one client
	// with. It is generated locally, never on the server.
	Material reality.Material
	// Dest is the site the handshake impersonates, host:port. ServerName is
	// the SNI a client presents, and has to be the same host.
	Dest       string
	ServerName string
}

// Progress is called as each step starts, for a caller that wants to say what
// is happening during a wait that runs to several minutes.
type Progress func(step string)

// Paths on the server. They are the ones upstream Xray uses, so anything
// written about running Xray applies to a server vpncli made.
const (
	binaryPath = "/usr/local/bin/xray"
	configPath = "/usr/local/etc/xray/config.json"
	sharePath  = "/usr/local/share/xray"
	unitPath   = "/etc/systemd/system/xray.service"
	sysctlPath = "/etc/sysctl.d/99-vpncli-bbr.conf"
	decoyPath  = "/var/www/html/index.html"
)

// step is one thing the bootstrap does, and the name it is known by.
type step struct {
	what string
	do   func(context.Context, Runner, Options) error
}

// steps are ordered so that nothing is reachable before it is ready: the
// firewall closes last, and Xray is only started once its config is in place.
var steps = []step{
	{"installing packages", installPackages},
	{"turning on BBR", enableBBR},
	{"installing Xray-core " + XrayVersion, installXray},
	{"writing the server config", writeConfig},
	{"putting up the decoy site", installDecoy},
	{"starting Xray", startXray},
	{"closing the firewall", closeFirewall},
	{"checking it came up", verify},
}

// Steps names what Run will do, in order, so a caller can show the whole list
// before starting it. It is taken from the same table Run walks, which is what
// keeps a displayed checklist from drifting out of step with the work.
func Steps() []string {
	names := make([]string, 0, len(steps))
	for _, s := range steps {
		names = append(names, s.what)
	}
	return names
}

// Run configures a server.
func Run(ctx context.Context, c Runner, opts Options, progress Progress) error {
	if progress == nil {
		progress = func(string) {}
	}

	for _, step := range steps {
		progress(step.what)
		if err := step.do(ctx, c, opts); err != nil {
			return fmt.Errorf("%s: %w", step.what, err)
		}
	}
	return nil
}

// installPackages brings in what the rest of the steps need.
//
// A freshly booted cloud image is usually still running cloud-init and its
// first unattended upgrade, both of which hold the dpkg lock. Waiting for that
// is what apt's own lock timeout is for; racing it produces the single most
// common bootstrap failure there is.
func installPackages(ctx context.Context, c Runner, _ Options) error {
	_, err := c.Run(ctx, `set -eu
export DEBIAN_FRONTEND=noninteractive
if command -v cloud-init >/dev/null 2>&1; then
  cloud-init status --wait >/dev/null 2>&1 || true
fi
apt-get -o DPkg::Lock::Timeout=600 update -qq
apt-get -o DPkg::Lock::Timeout=600 install -y -qq --no-install-recommends \
  ca-certificates curl unzip ufw nginx-light`)
	return err
}

// enableBBR switches the congestion control algorithm.
//
// BBR is the one kernel setting worth changing here. A tunnel over a long path
// with any loss on it is exactly the case where it beats the default, and it
// is a two line file.
func enableBBR(ctx context.Context, c Runner, _ Options) error {
	sysctl := "net.core.default_qdisc=fq\nnet.ipv4.tcp_congestion_control=bbr\n"
	if err := c.Upload(ctx, sysctlPath, 0o644, []byte(sysctl)); err != nil {
		return err
	}

	if _, err := c.Run(ctx, "sysctl --system >/dev/null"); err != nil {
		return err
	}

	// A kernel without BBR would leave the setting silently ignored.
	out, err := c.Run(ctx, "sysctl -n net.ipv4.tcp_congestion_control")
	if err != nil {
		return err
	}
	if got := strings.TrimSpace(out); got != "bbr" {
		return fmt.Errorf("congestion control is %q, want bbr", got)
	}
	return nil
}

// installXray downloads a pinned release and verifies it before unpacking.
//
// The version and its checksum are constants in this program, so what lands on
// the server is what was tested, and a release rebuilt or replaced upstream
// fails the check rather than being installed.
func installXray(ctx context.Context, c Runner, _ Options) error {
	asset, checksum, err := release(ctx, c)
	if err != nil {
		return err
	}

	command := fmt.Sprintf(`set -eu
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL --retry 3 --retry-delay 2 -o "$tmp/xray.zip" %s
echo "%s  $tmp/xray.zip" | sha256sum -c - >/dev/null
unzip -o -q "$tmp/xray.zip" -d "$tmp/xray"
install -m 0755 "$tmp/xray/xray" %s
install -D -m 0644 "$tmp/xray/geoip.dat" %s/geoip.dat
install -D -m 0644 "$tmp/xray/geosite.dat" %s/geosite.dat`,
		downloadURL(asset), checksum, binaryPath, sharePath, sharePath)

	_, err = c.Run(ctx, command)
	return err
}

// writeConfig installs the server config and the service that runs it.
func writeConfig(ctx context.Context, c Runner, opts Options) error {
	config, err := serverConfig(opts)
	if err != nil {
		return err
	}

	// The config holds the server's REALITY private key. It is written 0600
	// and then handed to the account Xray runs as, read only: root writes it,
	// one service reads it, and nothing else on the box can.
	if err := c.Upload(ctx, configPath, 0o600, config); err != nil {
		return err
	}
	if _, err := c.Run(ctx, fmt.Sprintf("chown %s:%s %s && chmod 0400 %s",
		serviceUser, serviceGroup, configPath, configPath)); err != nil {
		return err
	}

	if err := c.Upload(ctx, unitPath, 0o644, []byte(serviceUnit)); err != nil {
		return err
	}
	_, err = c.Run(ctx, "systemctl daemon-reload")
	return err
}

// installDecoy puts something ordinary on port 80.
//
// Port 443 needs no cover - anything that is not one of our clients is
// forwarded to the camouflage site and sees that site's own answer. Port 80 is
// the one a scanner tries first, and a server that refuses it while answering
// TLS is more interesting than one that serves a page.
func installDecoy(ctx context.Context, c Runner, _ Options) error {
	if err := c.Upload(ctx, decoyPath, 0o644, []byte(decoyPage)); err != nil {
		return err
	}
	_, err := c.Run(ctx, "systemctl enable nginx >/dev/null 2>&1 && systemctl restart nginx")
	return err
}

// startXray enables the service and (re)starts it.
//
// It is a restart rather than `enable --now`, which does nothing at all when
// the unit is already running. Re-running the bootstrap writes a fresh config
// with fresh key material, and without a restart the server carries on serving
// the old one - while local state records the new one. Every client then fails
// with the server insisting they are strangers.
func startXray(ctx context.Context, c Runner, _ Options) error {
	_, err := c.Run(ctx, "systemctl enable xray >/dev/null 2>&1 && systemctl restart xray")
	return err
}

// closeFirewall denies everything that is not SSH, HTTP or the tunnel.
//
// The allow rules come before the enable, in that order and never the other
// way round: enabling a default-deny firewall on a machine reached over SSH,
// before SSH is allowed through it, locks the door with the key inside.
func closeFirewall(ctx context.Context, c Runner, _ Options) error {
	_, err := c.Run(ctx, fmt.Sprintf(`set -eu
ufw default deny incoming >/dev/null
ufw default allow outgoing >/dev/null
ufw allow 22/tcp >/dev/null
ufw allow 80/tcp >/dev/null
ufw allow %d/tcp >/dev/null
ufw --force enable >/dev/null`, Port))
	return err
}

// verify checks that the server is actually serving, rather than that every
// command happened to exit zero.
func verify(ctx context.Context, c Runner, _ Options) error {
	out, err := c.Run(ctx, "systemctl is-active xray || true")
	if err != nil {
		return err
	}
	if got := strings.TrimSpace(out); got != "active" {
		// The journal is where Xray says what it disliked about the config.
		reason, _ := c.Run(ctx, "journalctl -u xray -n 20 --no-pager 2>/dev/null || true")
		if reason = strings.TrimSpace(reason); reason != "" {
			return fmt.Errorf("xray is %s: %s", got, lastLine(reason))
		}
		return fmt.Errorf("xray is %s", got)
	}

	// Active is not the same as listening: a config Xray accepts but cannot
	// bind would still report active for a moment.
	if _, err := c.Run(ctx, fmt.Sprintf(
		`for i in $(seq 1 10); do ss -ltn | grep -q ':%d ' && exit 0; sleep 1; done; exit 1`, Port)); err != nil {
		return fmt.Errorf("nothing is listening on %d: %w", Port, err)
	}
	return nil
}

// lastLine is the end of a journal excerpt, which is where the reason is.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// serviceUser and serviceGroup are who Xray runs as. Binding 443 is done with
// a capability rather than by running the whole thing as root.
const (
	serviceUser  = "nobody"
	serviceGroup = "nogroup"
)

// serviceUnit is the systemd service. It mirrors the one upstream ships, minus
// the parts that assume the official installer's layout.
const serviceUnit = `[Unit]
Description=Xray Service
Documentation=https://github.com/XTLS/Xray-core
After=network.target nss-lookup.target

[Service]
User=` + serviceUser + `
Group=` + serviceGroup + `
# Enough to bind 443 and nothing else. Running as root would be one config
# parsing bug away from handing the whole machine to a stranger.
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
AmbientCapabilities=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
# Where geoip.dat lives. Without it the routing rule that blocks private
# addresses cannot load, and Xray refuses to start rather than start without it.
Environment=XRAY_LOCATION_ASSET=` + sharePath + `
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ExecStart=` + binaryPath + ` run -config ` + configPath + `
Restart=on-failure
RestartSec=5
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
`

// decoyPage is what a scanner gets on port 80. It is deliberately dull: a
// server that serves nothing is rarer, and therefore more interesting, than
// one that serves a placeholder.
const decoyPage = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>It works</title></head>
<body><p>It works.</p></body>
</html>
`
