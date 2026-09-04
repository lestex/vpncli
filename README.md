# vpncli

[![CI](https://github.com/lestex/vpncli/actions/workflows/ci.yml/badge.svg)](https://github.com/lestex/vpncli/actions/workflows/ci.yml)

Provision, rotate and destroy single-user VPN servers on cloud VPS providers,
configured with VLESS+REALITY (Xray-core).

One static Go binary. No Terraform, no domain, no CDN.

> **Status: v0.7.0 - servers can be created.** `vpncli init` asks for
> everything a server needs, and `vpncli provision` creates one and waits for
> it to boot. What it does not do yet is install Xray-core: the server comes up
> as a stock OS image with the SSH key on it. See [Roadmap](#roadmap).

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

Set up where servers get created:

```sh
export DIGITALOCEAN_TOKEN=dop_v1_...   # DIGITALOCEAN_ACCESS_TOKEN also works
vpncli init
```

```
Answers are written to ~/.config/vpncli/config.yaml

Provider: DigitalOcean (digitalocean), the only one implemented

Fetching regions from digitalocean...

Pick one close to you. Latency is the one cost a VPN cannot make back.

  1)  ams3  Amsterdam 3
  2)  fra1  Frankfurt 1
  3)  nyc3  New York 3

Region: 2

Fetching sizes for fra1...

A tunnel is network-bound, not CPU-bound. The cheapest size is the
right answer far more often than not.

  1)  s-1vcpu-512mb-10gb  $4/mo   512MB RAM  1 vCPU  10GB disk
  2)  s-1vcpu-1gb         $6/mo   1GB RAM    1 vCPU  25GB disk
  3)  s-1vcpu-2gb         $12/mo  2GB RAM    1 vCPU  50GB disk
  4)  s-2vcpu-2gb         $18/mo  2GB RAM    2 vCPU  60GB disk

Size [s-1vcpu-512mb-10gb]:

Fetching images...

  1)  ubuntu-24-04-x64  Ubuntu 24.04 (LTS) x64
  2)  ubuntu-22-04-x64  Ubuntu 22.04 (LTS) x64
  3)  debian-13-x64     Debian 13 x64
  4)  debian-12-x64     Debian 12 x64

Image [ubuntu-24-04-x64]:

Fetching SSH keys...

This is the key the bootstrap logs in with. Pick one whose private
half is on this machine.

  1)  laptop       SHA256:2f8a...
  2)  workstation  SHA256:9c1b...

SSH key [laptop]:

REALITY hides the server behind a real site: the handshake is that
site's, so a probe sees only a visit to it. Best is somewhere near
the server that nobody would think twice about.

  1)  www.microsoft.com   large, CDN-fronted, boring to see in a log
  2)  www.apple.com       same, and reached from everywhere
  3)  www.samsung.com     widely mirrored, good outside the US
  4)  dl.google.com       download endpoint, long connections look normal
  5)  www.cloudflare.com  everywhere, though obviously a CDN
  6)  other               type a hostname

Camouflage [www.microsoft.com]:
```

An answer is either the number or the slug, and re-running the wizard offers
the current value as the default, so `vpncli init` doubles as a way to change
one setting. Nothing is written until the last question is answered - Ctrl-C or
Ctrl-D gets out of any question, and an abandoned wizard leaves no half-filled
config behind.

Ctrl-C is a cancel rather than a kill everywhere: a `provision` interrupted
mid-wait still reports the server it created and the id to destroy it by. A
second Ctrl-C kills outright.

The menus are filtered on purpose, and each filter is a decision:

- **Regions** the account cannot create in are left out.
- **Sizes** are the cheapest few available in the chosen region. An account can
  create some seventy, and a tunnel can use almost none of them. A size already
  in the config stays on the menu whatever it costs, so re-running the wizard
  never quietly takes one away.
- **Images** are Ubuntu and Debian only, newest first. The bootstrap is apt,
  nginx, ufw and a BBR sysctl, so offering Fedora would produce a server that
  never gets finished.
- **SSH keys** are the ones already registered with the provider, offered by
  name. vpncli never creates a key: one you uploaded is one whose private half
  is already where your SSH agent expects it. An account with none is a dead
  end, and the wizard says so rather than creating a server nobody can log in
  to.
- **Camouflage** is the site REALITY impersonates, written to the config as
  both `dest` and `server_names`. The offered ones are large, CDN-fronted and
  unremarkable; anything else can be typed. A good pick is near the server and
  boring to be seen talking to.

Answer the region and the rest can be taken on the Enter key: the defaults are
the cheapest size in that region and the newest Ubuntu.

It is a numbered list rather than a cursor-driven menu on purpose: this has to
work over SSH and in a pipe, which is where a VPS is usually being set up from.

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

Create a server from those answers:

```sh
vpncli provision
```

```
Creating vpncli-fra1-7d3a91 (s-1vcpu-1gb, fra1) on digitalocean...

ID  NAME                REGION  SIZE         IMAGE             IPV4          STATUS  AGE
3   vpncli-fra1-7d3a91  fra1    s-1vcpu-1gb  ubuntu-24-04-x64  203.0.113.10  active  just now
```

The row is written as soon as the provider accepts the request, before the wait
for the server to boot. That ordering is deliberate: a server that exists but is
in nobody's state file is invisible and still billed, so an interrupted wait
leaves something `vpncli destroy` can clean up, and `vpncli sync` finds it from
any machine.

Xray-core and the REALITY camouflage are not installed yet (v0.8.0). What comes
up today is a stock OS image with the chosen SSH key on it.

Destroy one:

```sh
vpncli destroy 3
```

```
Destroy vpncli-fra1-7d3a91 (203.0.113.10, fra1, id 3)? Its IP and keys are gone for good.
Type yes to confirm: yes
destroyed vpncli-fra1-7d3a91 (203.0.113.10)
```

The provider goes first, then the row. A server already gone there is not an
error, but a delete that genuinely fails leaves the row alone: a server nothing
knows about bills forever. `--yes` skips the question, and nothing else is
accepted as a confirmation - not even `y`.

The rest of the workflow lands in later versions:

```sh
vpncli connect <id>      # bring up the local sing-box client
vpncli connect <id> --qr # terminal QR for mobile clients
vpncli rotate <id>       # destroy and replace: new IP, new keys
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
internal/prompt/                 the wizard's questions
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

The catalog lookups behind it return everything, sorted but unfiltered:
unavailable regions and sizes included. Which of them are worth offering is a
vpncli decision, and it lives in the wizard where it can be read and argued
with, not scattered through the providers.

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
| v0.4.0 | ✅ State wired into create/delete; `list` and `sync` |
| v0.5.0 | ✅ Wizard: provider + region select |
| v0.6.0 | ✅ Wizard: size + OS select |
| **v0.7.0** | ✅ Wizard: SSH key + REALITY camouflage; `provision` and `destroy` |
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
