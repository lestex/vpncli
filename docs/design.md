# Design

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
user-data, and the metadata service is readable from inside the server. The
REALITY keys are generated locally and pushed over SSH after boot instead, so
the only place they exist is the server and the local state file.

**Pinned, verified installs.** Xray-core is a specific release, checked against
a SHA256 that is a constant in the source. Nothing is piped from a URL into a
root shell, and two servers provisioned a week apart are the same server.

**The camouflage site is measured, not assumed.** REALITY can only relay a
handshake that fits in 8192 bytes, and several of the most obvious sites to
hide behind no longer do. Picking one is a checked decision rather than a
matter of taste - see [Setup](setup.md#the-wizard).

## Files

| Path | Purpose |
| --- | --- |
| `~/.config/vpncli/config.yaml` | User config, written by the wizard (`0600`) |
| `~/.local/share/vpncli/state.db` | Local server state, including each server's REALITY keys |

Both honor `XDG_CONFIG_HOME` / `XDG_DATA_HOME`.

The database holds the only local copy of a server's key material. It is the
file to back up, and the file to be careful with: anyone who can read it can
connect as you.

## Layout

```
cmd/vpncli/                      entry point, signal handling
internal/cli/                    cobra command tree
internal/bootstrap/              turning a bare image into a server
internal/reality/                REALITY key material
internal/ssh/                    the connection the bootstrap runs over
internal/client/                 credentials to a client config
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
| v0.2.0 | ✅ DigitalOcean read-only: `ListInstances`, `providers do` |
| v0.3.0 | ✅ DigitalOcean create/delete, `WaitReady`, 429 backoff |
| v0.4.0 | ✅ State wired into create/delete; `list` and `sync` |
| v0.5.0 | ✅ Wizard: provider + region select |
| v0.6.0 | ✅ Wizard: size + OS select |
| v0.7.0 | ✅ Wizard: SSH key + REALITY camouflage; `provision` and `destroy` |
| v0.8.0 | ✅ Xray-core bootstrap over SSH, nginx decoy, BBR, ufw lockdown |
| v0.9.0 | ✅ Client connect: `vless://` URI, sing-box config, QR |
| **v1.0.0** | ✅ `rotate`, tun mode, `connect -o` |
