# vpncli

[![CI](https://github.com/lestex/vpncli/actions/workflows/ci.yml/badge.svg)](https://github.com/lestex/vpncli/actions/workflows/ci.yml)

Provision, rotate and destroy single-user VPN servers on cloud VPS providers,
configured with VLESS+REALITY (Xray-core).

One static Go binary. No Terraform, no domain, no CDN.

> **Status: v0.4.0 - state wired in.** `vpncli list` and `vpncli sync` work
> against the local store, and creating and destroying servers now records
> what it did. There is still no `provision` command to drive it: choosing a
> region, size and image is the wizard's job, and that starts at v0.5.0. See
> [Roadmap](#roadmap).

## Why it is built this way

**Direct IP, no DNS.** REALITY works by making the server's TLS handshake
indistinguishable from a real site's. Putting a CDN in front breaks that trick
and adds a more surveillable hop. A stable hostname would also be a permanent
correlation point - which is exactly what rotating the IP is meant to avoid.

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
vpncli --help
```

List every droplet in a DigitalOcean account, straight from the API:

```sh
export DIGITALOCEAN_TOKEN=dop_v1_...   # DIGITALOCEAN_ACCESS_TOKEN also works
vpncli providers do list
```

```
ID    NAME              REGION  SIZE                IMAGE             IPV4          STATUS        AGE
1001  vpncli-fra1-a1b2  fra1    s-1vcpu-1gb         ubuntu-24-04-x64  203.0.113.10  active        2d
1002  vpncli-ams3-c3d4  ams3    s-1vcpu-512mb-10gb  debian-12-x64     -             provisioning  just now
```

This is not filtered to servers vpncli created, which is what makes it useful
for confirming a token works and for spotting drift.

List the servers vpncli itself tracks, from local state:

```sh
vpncli list
```

No API call, so it is instant, works offline, and needs no token. The `ID`
column is the short local id that other commands take. The trade is staleness:
a server created or destroyed elsewhere shows up only after a sync.

```sh
vpncli sync
```

```
1 adopted, 2 updated, 1 removed
```

`sync` treats the provider as the source of truth. Rows for servers that no
longer exist are dropped, drifted addresses and statuses are corrected, and
servers tagged `vpncli` that local state has never seen are adopted - which is
how a second machine, or a run that died mid-provision, is picked back up.
Untagged servers are left alone, because that listing covers the whole account.

Provisioning commands land in later versions; the full workflow will be:

```sh
vpncli init              # interactive wizard, writes config.yaml
vpncli provision         # create + bootstrap a server
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

Both honor `XDG_CONFIG_HOME` / `XDG_DATA_HOME`.

## Layout

```
cmd/vpncli/                      entry point, signal handling
internal/cli/                    cobra command tree
internal/manager/                provider + state, joined
internal/provider/               VPSProvider interface and shared types
internal/provider/digitalocean/  DigitalOcean implementation
internal/config/                 config file + XDG paths
internal/state/                  SQLite state store
```

`manager` is where the two halves meet, and it holds one rule: the provider is
the source of truth and state follows it, never the other way round. That is
what makes `sync` a reconciliation rather than a merge, and why `Provision`
records a server before it waits on it - an untracked server is one that keeps
billing where nobody can see it.

`VPSProvider` is the single seam every cloud goes through. Providers differ in
ways that must stay behind it - Hetzner's SDK has native async waiters while
DigitalOcean, Vultr and Linode need manual polling - so each implementation
normalizes that inside its own `WaitReady`.

## Development

```sh
make check   # vet + lint + race tests - what CI runs
make test    # race tests only
make lint    # golangci-lint (v2.12.2, pinned to match CI)
make fmt
make dist    # cross-compiled release archives into dist/
```

CI runs on every push: tests on Linux and macOS, lint, a `go mod tidy` check,
and a cgo-free cross-compile of all four release targets. That last job is
what protects the static-binary promise - if a cgo dependency ever displaces
the pure-Go SQLite driver, it fails there rather than at release time.

Releases are cut by pushing a `v*` tag. `make dist` runs the identical build
locally, so packaging can be rehearsed before the tag goes out.

## Roadmap

| Version | Scope |
| --- | --- |
| v0.1.0 | ✅ Scaffold: CLI, provider interface, config, SQLite schema |
| v0.2.0 | ✅ DigitalOcean read-only: `ListInstances`, `providers do list` |
| v0.3.0 | ✅ DigitalOcean create/delete, `WaitReady`, 429 backoff |
| **v0.4.0** | ✅ State wired into create/delete; `list` and `sync` |
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
