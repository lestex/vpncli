# Servers

## Provision

Create a server from the answers [the wizard](setup.md#the-wizard) wrote:

```sh
vpncli server provision
```

```
Creating vpncli-fra1-7d3a91 (s-1vcpu-1gb, fra1) on digitalocean...

  ✓ installing packages
  ✓ turning on BBR
  ⠹ installing Xray-core v26.3.27 (48s)
    writing the server config
    putting up the decoy site
    starting Xray
    closing the firewall
    checking it came up
```

```
Creating vpncli-fra1-7d3a91 (s-1vcpu-1gb, fra1) on digitalocean...

  ✓ installing packages
  ✓ turning on BBR
  ✓ installing Xray-core v26.3.27
  ✓ writing the server config
  ✓ putting up the decoy site
  ✓ starting Xray
  ✓ closing the firewall
  ✓ checking it came up

ready in 3m11s

ID  NAME                REGION  SIZE         IMAGE             IPV4          STATUS  AGE
3   vpncli-fra1-7d3a91  fra1    s-1vcpu-1gb  ubuntu-24-04-x64  203.0.113.10  active  just now

Serving VLESS+REALITY on 203.0.113.10:443, camouflaged as www.apple.com.
```

Creating takes about a minute and configuring another two, so the steps are on
screen and tick off as they finish, with a clock against whichever is running -
a stall shows up as one step's time running away. A step that fails is marked
and left there, so what went wrong stays on screen next to the error.

Redirected into a pipe or a file, each step is printed once as it starts and
nothing is redrawn, so a log reads as a plain list of what happened. A terminal
that says it is `dumb` gets the same.

The row is written as soon as the provider accepts the request, before the wait
for the server to boot. That ordering is deliberate: a server that exists but is
in nobody's state file is invisible and still billed, so an interrupted wait
leaves something `vpncli server destroy` can clean up, and `vpncli sync` finds it from
any machine.

## What the bootstrap does

Over SSH, as root, in this order:

1. **Waits for the package manager.** A freshly booted image is still running
   cloud-init and its first unattended upgrade, both holding the dpkg lock.
   Racing that is the single most common bootstrap failure there is.
2. **Turns on BBR.** The one kernel setting worth changing: a tunnel over a
   long path with any loss on it is exactly where BBR beats the default.
3. **Installs Xray-core**, pinned to a version and verified against a SHA256
   held in the source, from the official release zip.
4. **Writes the server config** with freshly generated key material - a client
   UUID, an X25519 keypair and a short id, generated on your machine and never
   on the server. The config is written `0600`, then handed read only to the
   unprivileged account the service runs as.
5. **Puts up a decoy** on port 80. Port 443 needs no cover, because anything
   that is not our client is forwarded to the camouflage site and gets that
   site's own answer. Port 80 is what a scanner tries first, and a server that
   refuses it while answering TLS is more interesting than one that serves a
   page.
6. **Starts Xray** as `nobody`, with `CAP_NET_BIND_SERVICE` for port 443 and
   nothing else. Running it as root would be one parsing bug away from handing
   over the machine.
7. **Closes the firewall** to everything but 22, 80 and 443 - the allow rules
   first and the enable last, in that order and never the other way round.
8. **Checks it came up**, because every command exiting zero is not the same
   as a server that is listening.

Client traffic to private addresses is dropped, which is what keeps a tunnel
from being a route to the provider's own metadata service.

The SSH host key is recorded the first time vpncli connects and checked every
time after. A server created a minute ago has no key anyone could have known in
advance, so the first connection has nothing to compare against; from then on a
different key is refused rather than shrugged at. What this connection carries
is the server's private key, so it is worth the pin.

## Bootstrap again

If the bootstrap fails halfway - a dropped connection, an apt mirror having a
bad day - the server is fine and only the configuring needs another go:

```sh
vpncli server bootstrap 3
```

That generates fresh key material and replaces whatever reached the server, so
there is never a half-written set of keys to reconcile. Nothing is recorded
locally until the server is actually serving.

## List

List the servers vpncli itself tracks, from local state:

```sh
vpncli server list
```

```
ID  PROVIDER      NAME                REGION  SIZE         IMAGE             IPV4          STATUS  AGE
3   digitalocean  vpncli-ams3-0a910d  ams3    s-1vcpu-1gb  ubuntu-24-04-x64  203.0.113.10  active  2h
```

The provider column is there because the id alone stops being unique the
moment a second provider is configured: what names a server is the pair.

No API call, so it is instant, works offline, and needs no token. The `ID`
column is the short local id that other commands take. The trade is staleness:
a server created or destroyed elsewhere shows up only after a sync.

## Sync

```sh
vpncli sync
```

```
1 adopted, 2 updated, 1 removed
```

`sync` treats the provider as the source of truth. Rows for servers that no
longer exist are dropped, drifted addresses and statuses are corrected, and
servers tagged `vpncli` that local state has never seen are adopted - which is
how a run that died mid-provision is picked back up, and how a second machine
gets to see, rotate and destroy what the first one made.
Untagged servers are left alone, because that listing covers the whole account.

Adopting a server is not the same as being able to connect to it. Its REALITY
keys were generated by the machine that bootstrapped it and live only in that
machine's state file, so on any other machine an adopted server has no keys.
`vpncli server bootstrap` would give it some, but they are new keys and they
replace the old ones, which cuts off every client the first machine set up.
To use a server from two machines, connect from the one that made it and
import the link on the other.

## Rotate

Replace a server with a fresh one:

```sh
vpncli server rotate 3
```

```
Replace vpncli-ams3-0a910d (203.0.113.10, ams3, id 3)?
A new server is created and configured first; this one is destroyed only
once that has worked. Both are billed until then.
Type yes to confirm: yes
Replacing vpncli-ams3-0a910d (203.0.113.10) with vpncli-ams3-7d3a91 (s-1vcpu-1gb, ams3) on digitalocean...
rotated in 3m24s: vpncli-ams3-0a910d is gone

ID  NAME                REGION  SIZE         IMAGE             IPV4          STATUS  AGE
4   vpncli-ams3-7d3a91  ams3    s-1vcpu-1gb  ubuntu-24-04-x64  203.0.113.44  active  just now

Serving VLESS+REALITY on 203.0.113.44:443, camouflaged as www.samsung.com.
Its address and keys are new, so every client needs `vpncli server connect 4` again.
```

This is the workflow the whole program is shaped around. The replacement
shares nothing with what it replaces - new address, new keypair - so whatever
was learned about the old server describes something that no longer exists.

The order is the important part: the replacement is created and confirmed to
be serving *before* anything is destroyed, so a rotation that fails leaves the
old server exactly where it was, and says so. Both are billed for the couple
of minutes in between. The replacement is built from the current config, so it
picks up anything `vpncli providers init` has changed since, and it gets a new local id,
because it is a different server.

## Destroy

Destroy one:

```sh
vpncli server destroy 3
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
