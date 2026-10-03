# Tunnel

## Up and down

A proxy carries only what is pointed at it. The tunnel is for the whole
machine: it creates a network interface and routes everything through it - programs with no proxy setting
included, and DNS with them. Creating an interface and rewriting the routing
table needs root:

```sh
vpncli tun up 3
```

```
Routing this machine through vpncli-ams3-0a910d (203.0.113.10, ams3).
Ctrl-C brings it down.

Password:
```

That writes the config, runs sing-box against it, and brings the tunnel down
when you interrupt it, so there is no file or process left to remember.
`--detach` leaves it up after the command returns:

```sh
vpncli tun up 3 --detach
vpncli tun status
vpncli tun down
```

With no id it takes the most recently configured server, which after a
provision or a rotation is the one you meant.

## Status

`vpncli tun status` says what is running, where it goes, and where traffic
actually comes out:

```
up through vpncli-ams3-0a910d (203.0.113.10, ams3) for 8m
interface utun5: 172.19.0.1/30, fdfe:dcba:9876::1/126, mtu 65535, up
exit 203.0.113.10 in NL, the server's address
```

The interface is found by the tunnel's own address, because its name is the
system's choice. A sing-box running with no interface carrying that address is
reported as routing nothing - which is what one started without root looks
like, and otherwise looks exactly like a tunnel that works.

The exit line is where the internet thinks you are. Status asks Cloudflare's
trace endpoint, by IP so no DNS lookup goes anywhere, which address and country
the request arrived from, and checks the address against the server's. A
process and an interface only show that a tunnel exists; this shows it carrying
traffic. An exit that is not the server is a tunnel that is up and routing
nothing, and a lookup that times out is one that cannot reach the internet at
all. It is the one request status makes, so it takes a round trip rather than
being instant.

## Requirements

`sing-box` has to be installed and at least 1.12: the generated config uses
route rule actions and the typed DNS format, and on anything older it does not
fail to connect, it fails to parse - with a message about an unknown field that
says nothing about the version. Both are checked before a config is written.

`status` and `down` find the tunnel by the config it is running against rather
than by remembering a process id, because the id of what gets started is not
the id of what survives: sudo forks a monitor and the process that was spawned
is gone within milliseconds. That also means a tunnel started by hand is found,
reported and stoppable. It needs `sing-box` installed and
your password, because creating a network interface and rewriting the routing
table needs root.

`vpncli server connect 3 --tun -o ~/vpn.json` still writes the config if you
would rather run the client yourself.

The [proxy config](connecting.md#proxy-on-a-desktop) and the tunnel carry the
same connection with the same credentials; the difference is only where
traffic enters it.

## The local network

The tun config keeps the local network local: traffic to a private address
goes out of the normal interface rather than into the tunnel. That is not a
convenience. The server drops private destinations on purpose - a tunnel that
can reach the provider's metadata service can hand out the account's own
credentials - so without the rule a printer, a NAS or a router page is not
slow, it is `connection refused` from three countries away.

## IPv6

It also follows what the server can actually do about IPv6. Servers are created
with an IPv6 address, and the bootstrap checks whether one really arrived. On a
server that has it, the tunnel carries IPv6. On one that does not - anything
provisioned before this was true - the config refuses IPv6 locally instead:
clients try IPv6 first for anything dual stack, and every one of those attempts
would otherwise cross the world to fail before falling back. Refused rather than
sent around the tunnel, because IPv6 leaving by the normal interface is traffic
leaving the tunnel.

## Errors that are not failures

sing-box will log lines like this, and they are not a failure:

```
ERROR connection: report handshake success: connection refused
```

It means the tunnel connected and, by the time it had, the local application
was gone. Browsers open speculative connections and cancel the losers, macOS
races IPv4 against IPv6 and drops whichever answers second, and anything with
a short connect timeout gives up before a round trip to another country
finishes. The tunnel is working; something local stopped waiting. It is worth
recognising rather than chasing, which is what a quarter of a second of
latency does to software written for a local network.
