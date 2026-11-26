# 15. Build plan, alternatives, open items

## 15.1 Build plan

Each phase is independently useful; P1 alone already gives "all traffic goes
through the box".

| Phase | Deliverable | Done when |
|---|---|---|
| **P0** spike | confirm `/connections` fields; measure TPROXY throughput on the target box | fields documented, ≥800 Mbps measured |
| **P1** gateway | dnsmasq + nftables + forwarding + bypass switch | every device reaches the internet through the box; bypass restores plain routing |
| **P2** capture & count | sing-box transparent inbound, 1 s poll, usage buckets, drift report | per-device raw bytes in the DB match the drift report within 2% |
| **P3** identity | leases → devices, binding via sub fetch / VLESS, guest allowance | a phone becomes a person's device by importing the link; binding survives a reboot |
| **P4** quotas | periods (Jalali), caps, weights, nftables blocking, portal | crossing a cap blocks within 5 s and shows the portal |
| **P5** routes | route CRUD, credentials, subscription endpoint, psiphon child | adding a route appears in the subscription without reissue; revoking kills the listener |
| **P6** polish | admin UI, graphs, audit, drift panel, backups | admin can run a month without touching the shell |

## 15.2 Alternatives rejected

| Alternative | Why not |
|---|---|
| Plain NAT + `nft meter` per-IP counters | no layer 7, so no multipliers, no per-route numbers |
| iptables `-m connbytes` accounting | same limitation, plus awkward per-flow attribution |
| Xray-core as the data plane | mature per-user stats, but no `/connections`-style flow API to give per-connection source and host |
| sing-box per-user stats | per user, not per user × class; cannot weight destinations |
| Mihomo | viable; weaker multi-user VLESS story for this shape |
| OPNsense / pfSense | no person/quota/subscription model; would still need this agent |
| Captive-portal login instead of subscriptions | needs credentials per device and a browser dance; the subscription is the credential the user already wants to install |
| L2 bridge mode | simpler rules, but no routing decision point, no NAT control, no clean bypass |

## Appendix A — open items

1. **`pool_charged`** — the ISP's per-period allowance in GB.
2. **`anchor_jalali_day`** — the exact Jalali reset day.
3. **UI locale** — Persian-only, or Persian + English toggle.
4. **`raw_ceiling`** — set after one observed cycle.
5. **P0 result** — whether `metadata.inboundName` is present (changes one
   implementation detail, not the design).
