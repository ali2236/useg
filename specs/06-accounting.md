# 6. Accounting and rating

## 6.1 Collection

The agent polls the sing-box Clash API `/connections` once per second. Each
connection object yields source IP, sniffed host (⇒ class), cumulative
upload/download bytes, and — for lane B — the inbound name (⇒ person and
route). Lane A has no inbound identity, so its subject comes from the source IP.
The agent keeps the last value per connection id and folds deltas into buckets.

> **P0 verification:** confirm on the pinned sing-box version that
> `/connections` exposes `metadata.inboundName`, `metadata.sourceIP`,
> `metadata.host` and `upload`/`download`. If `inboundName` is missing, the
> fallback is one outbound tag per credential (`chains` then carries the
> identity). Nothing else in this document depends on the outcome.

## 6.2 Rating

```
raw     = up + down
charged = raw × rate[route][class]
```

- `class` is `nimbaha` when the sniffed host is on the ITO domestic list
  (`domestic_site.domain`, subdomains included), otherwise `default`.
- `rate` is a `route × class` cell, default 1.0. So "nimbaha on primary = 0.3"
  and "nimbaha on psiphon = 1.0" are two cells, not two code paths.
- Unknown or unidentifiable destination ⇒ class `default` ⇒ 1.0. Rates below
  1.0 discount; rates above 1.0 surcharge, which is useful for a route that
  costs real money (a paid upstream). The admin sets a rate per class on each
  route.
- Rate is taken once per connection, at first classification, and reused for
  that connection's lifetime. A flow is never part-charged at two rates.
- Both directions are charged (`count_directions: both`), matching the ISP. Up
  and down are stored separately for graphs.

## 6.3 Limits of the measurement

Stated plainly because quota systems live or die on this:

| Limitation | Effect | Mitigation |
|---|---|---|
| Connections that open and close inside one poll interval lose their tail | slight under-charge | 1 s poll; nightly drift report |
| SNI/Host invisible (ECH, DoH, QUIC) | class `default` | QUIC dropped; DNS forced to the box; ECH means 1.0, never a discount, so there is no incentive to hide |
| The box's own traffic is not proxied | admin downloads uncounted | accepted; option later |
| Poll-based, not event-based | not exact to the byte | accepted at this scale; escalation is a patched flow-close event or eBPF |

**Drift check, not enforcement:** nftables keeps per-source-IP raw counters.
Once a night the agent compares them with the sum of accounted raw bytes per
device and reports the gap. If it exceeds 2%, the UI says so. These counters are
never used to charge or block — they cannot be weighted, so they cannot be the
truth.
