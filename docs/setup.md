# Setup

```sh
vpncli version
vpncli --help
```

## The wizard

Set up where servers get created:

```sh
export DIGITALOCEAN_TOKEN=dop_v1_...   # DIGITALOCEAN_ACCESS_TOKEN also works
vpncli providers init
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
the current value as the default, so `vpncli providers init` doubles as a way to change
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

  Whatever is chosen is then checked, because not every big site can be hidden
  behind. REALITY relays the site's own TLS handshake to the client through an
  8192 byte buffer, and a site whose certificate, OCSP staple and timestamps
  come to more than that produces the nastiest failure this program has: every
  client authenticates successfully and then dies at the handshake, with the
  server logging nothing but a stranger being turned away. `www.microsoft.com`
  is such a site. The check measures the certificate, and TLS 1.3 and HTTP/2
  along with it, before the answer is written.

Answer the region and the rest can be taken on the Enter key: the defaults are
the cheapest size in that region and the newest Ubuntu.

It is a numbered list rather than a cursor-driven menu on purpose: this has to
work over SSH and in a pipe, which is where a VPS is usually being set up from.

## Looking at the account

List every droplet in a DigitalOcean account, straight from the API:

```sh
export DIGITALOCEAN_TOKEN=dop_v1_...   # DIGITALOCEAN_ACCESS_TOKEN also works
vpncli providers do
```

```
ID    NAME              REGION  SIZE                IMAGE             IPV4          STATUS        AGE
1001  vpncli-fra1-a1b2  fra1    s-1vcpu-1gb         ubuntu-24-04-x64  203.0.113.10  active        2d
1002  vpncli-ams3-c3d4  ams3    s-1vcpu-512mb-10gb  debian-12-x64     -             provisioning  just now
```

This is not filtered to servers vpncli created, which is what makes it useful
for confirming a token works and for spotting drift.
