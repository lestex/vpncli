# vpncli

Provision, rotate and destroy single-user VPN servers on cloud VPS providers,
configured with VLESS+REALITY (Xray-core).

One static Go binary. No Terraform, no domain, no CDN.

> **Status: v0.1.0 — scaffold.** The command tree, provider interface, config
> handling and local state store exist. No provider is implemented yet, so
> nothing can be provisioned. See [Roadmap](#roadmap).

## Why it is built this way

**Direct IP, no DNS.** REALITY works by making the server's TLS handshake
indistinguishable from a real site's. Putting a CDN in front breaks that trick
and adds a more surveillable hop. A stable hostname would also be a permanent
correlation point — which is exactly what rotating the IP is meant to avoid.

**SQLite, not Terraform.** The core workflow is destroy-and-replace: `rotate`
tears a server down and brings up a new one with a fresh IP and a fresh REALITY
keypair. Terraform's plan/apply model fights that cycle. A single local table
plus the provider API as source of truth is enough, and it is fast.

**Pure-Go SQLite** (`modernc.org/sqlite`, no cgo), so `go build` produces a
static binary that cross-compiles cleanly.

**No key material in cloud-init.** Provider metadata APIs log and expose
user-data. Server config is pushed over SSH after boot instead (v0.8.0).

## Install

Requires Go 1.25+.

```sh
git clone https://github.com/lestex/vpncli
cd vpncli
make build      # produces ./vpncli
# or: make install
```

## Usage

```sh
vpncli version
vpncli version --short
vpncli --help
```

Provisioning commands land in later versions; the full workflow will be:

```sh
vpncli init              # interactive wizard, writes config.yaml
vpncli provision         # create + bootstrap a server
vpncli list              # servers from local state (fast)
vpncli sync              # reconcile local state against the provider API
vpncli connect <id>      # bring up the local sing-box client
vpncli connect <id> --qr # terminal QR for mobile clients
vpncli rotate <id>       # destroy and replace: new IP, new keys
vpncli destroy <id>
```

## Files

| Path | Purpose |
| --- | --- |
| `~/.config/vpncli/config.yaml` | User config, written by the wizard (`0600`) |
| `~/.local/share/vpncli/state.db` | Local server state |

Both honour `XDG_CONFIG_HOME` / `XDG_DATA_HOME`.

## Layout

```
main.go                    entry point, signal handling
internal/cli/              cobra command tree
internal/provider/         VPSProvider interface and shared types
internal/config/           config file + XDG paths
internal/state/            SQLite state store
```

`VPSProvider` is the single seam every cloud goes through. Providers differ in
ways that must stay behind it — Hetzner's SDK has native async waiters while
DigitalOcean, Vultr and Linode need manual polling — so each implementation
normalizes that inside its own `WaitReady`.

## Development

```sh
make check   # vet + test
make fmt
```

## Roadmap

| Version | Scope |
| --- | --- |
| **v0.1.0** | ✅ Scaffold: CLI, provider interface, config, SQLite schema |
| v0.2.0 | DigitalOcean read-only: `ListInstances` |
| v0.3.0 | DigitalOcean create/delete, `WaitReady`, 429 backoff |
| v0.4.0 | State wired into create/delete; `list` and `sync` |
| v0.5.0 | Wizard: provider + region select |
| v0.6.0 | Wizard: size + OS select |
| v0.7.0 | Wizard: REALITY camouflage; `provision` wiring |
| v0.8.0 | Xray-core bootstrap over SSH, nginx decoy, BBR, ufw lockdown |
| v0.9.0 | Client connect: `vless://` URI, sing-box config, QR |
| v1.0.0 | `rotate`, error-message pass, docs |

## Clients

Servers are standard VLESS+REALITY, so any current client works. On iOS,
Shadowrocket (paid, most mature REALITY support) and Streisand (free, open
source) both import from a `vless://` URI or QR code with no server-side
accommodation.

## License

MIT
