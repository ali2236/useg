# 10. Components

| Piece | Choice | Rationale |
|---|---|---|
| DHCP + DNS | `dnsmasq` | leases give MAC↔IP↔hostname for free; one config file |
| Data plane | `sing-box`, pinned | TPROXY, VLESS multi-user, Clash API in one process |
| Agent, UI, sub server | one Go binary | netlink/nftables, JSON, embedded static UI, single deployable |
| Storage | SQLite | one file, trivial backup, ample at this scale |
| Upstream proxies | `psiphon-tunnel-core`, sing-box outbounds | spawned and supervised by the agent |

The agent supervises `sing-box` and any psiphon children, regenerates the
sing-box config whenever routes or credentials change (rare, atomic
write + reload), and reconciles every second:

```
leases ──▶ devices
/connections ──▶ usage deltas ──▶ buckets
buckets + caps ──▶ block set ──▶ nftables + conntrack
DB ──▶ sing-box config (only when routes/credentials change)
```

Because routes map to inbounds one-to-one, **the data plane has no domain
routing rules at all**. Domain groups exist only for rating. That is what keeps
the generated config small and stable, and reloads rare.
