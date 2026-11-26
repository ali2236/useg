# 2. Packet path

All traffic is forced through a single userspace proxy, because destination
weighting needs layer 7 and alternative routes need a proxy anyway.

```
client ──▶ nftables prerouting ──TPROXY──▶ sing-box ──▶ outbound ──▶ 192.168.100.1
                                              │              │
                                    sniff SNI / HTTP Host   direct | psiphon | v2ray | socks
                                              │
                              Clash API /connections (1 Hz)
                                              │
                                       usage buckets
```

**Lane A — zero-install.** The client simply uses its default gateway (the
box). nftables TPROXY-redirects internet-bound TCP and UDP into the transparent
inbound. Subject comes from source IP → DHCP lease → MAC → device → person or
guest.

**Lane B — subscription.** The client runs a V2Ray client with one of the
person's configs. Each `(person, route)` pair has its **own VLESS inbound with
its own port**, so the inbound tag alone identifies subject and route. No
per-user stats API, no UUID lookups.

Both lanes converge on the same accounting pipeline. Lane A always leaves via
`direct`; lane B leaves via whichever route its inbound is bound to. There is
exactly one connection object per flow, so nothing is counted twice.

**DNS** goes to `dnsmasq` on the box, which resolves for the LAN and forwards
upstream. DNS is never charged and never blocked: a blocked user must still be
able to resolve the portal.

**Local traffic is never metered.** Destinations on the LAN (`lan_cidr`), the
box itself, multicast/broadcast and link-local are accepted before TPROXY. mDNS,
SSDP and LAN-to-LAN therefore never reach the proxy and never enter the ledger;
the NAT table also leaves them unmangled, so discovery keeps its real source
address. The accountant re-checks the destination IP on every new flow, so a
stale ruleset or a moved box IP cannot book local bytes either.

**QUIC (UDP/443) is dropped in v1.** QUIC hides the hostname, so it can be
neither weighted nor described in the UI. Dropping it forces TCP+TLS where SNI
is visible. Cost: a little speed on some sites. Revisit when sing-box's QUIC
sniffing is good enough to trust for accounting.

**Fail-closed.** If `sing-box` dies, the house is offline. That is the correct
default for a quota system (fail-open means an unmetered, uncounted internet).
A `bypass` mode exists for maintenance: stop TPROXY, restore plain forwarding,
record an event, and show a banner in the UI.
