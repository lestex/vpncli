# Connecting

## The link

Everything a client needs comes out of `vpncli server connect`, given the
local id from `vpncli server list`:

```sh
vpncli server connect 3
```

```
vless://1e089a02-...@203.0.113.10:443?encryption=none&flow=xtls-rprx-vision&fp=chrome&pbk=_NPSjQ...&security=reality&sid=f2671bb145bdd37e&sni=www.microsoft.com&type=tcp#vpncli-ams3-0a910d
```

The link is the whole output, so it pipes:

```sh
vpncli server connect 3 | pbcopy
```

## Phone

For a phone, `--qr` draws the same link in the terminal, which gets it across
without going through anything that keeps a copy:

```sh
vpncli server connect 3 --qr
```

## Proxy on a desktop

For a desktop, `--sing-box` writes a sing-box config instead - a SOCKS and HTTP
proxy on `127.0.0.1:1080`, which needs no privileges:

```sh
vpncli server connect 3 --sing-box -o ~/vpn.json
sing-box run -c ~/vpn.json
```

A proxy only carries what is pointed at it, so a browser has to be told about
it (Firefox: Settings, Network Settings, Manual, SOCKS v5 `127.0.0.1:1080`, and
tick "Proxy DNS when using SOCKS v5"), or macOS has to be, under Network,
Details, Proxies.

To route the whole machine instead, see [Tunnel](tunnel.md).

## Writing a config file

`-o` is worth using over `>`. The file is created `0600`, because it carries
the key to your server, and the command prints exactly how to run what it just
wrote - which matters because a tun config run without root creates no
interface and tunnels nothing, quietly.

## Offline, and what has to match

None of this calls an API or opens an SSH connection. Everything a client needs
was recorded when the server was bootstrapped, so `connect` works offline and
with no token - which matters, because the moment you want a config is usually
the moment the network is unpleasant.

Three fields have to match the server exactly, and getting one wrong looks the
same from the outside as a connection that works and carries nothing: the SNI,
the public key and the short id. The one field that is a free choice is
`fp=chrome`, the TLS fingerprint the client imitates - the server never sees
which one you picked.
