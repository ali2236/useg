# useg — specification

A home gateway that meters every byte on the LAN, attributes it to a person or a
guest device, and enforces a monthly quota — while unknown devices work with no
software installed and V2Ray subscriptions serve as both the identity and the
routing contract.

| | |
|---|---|
| Version | 0.2 |
| Date | 2026-11-26 |
| Status | implemented (v1); this document is the design |
| Target host | Debian, Intel i5, 16 GB, single NIC |
| Scale | 5 persons, ~25 devices, one household, one WAN link |

## Specs

| Spec | Covers | Code |
|---|---|---|
| [1. Overview](01-overview.md) | summary, goals, topology, host and kernel requirements | — |
| [2. Packet path](02-packet-path.md) | TPROXY capture, lanes A/B, DNS, QUIC, fail-closed | — |
| [3. Addressing](03-addressing.md) | dnsmasq config, lease file as device feed | `internal/leases` |
| [4. Identity](04-identity.md) | subjects, binding, guest rules, anti-bypass | `internal/leases`, `internal/store` |
| [5. Routes](05-routes.md) | route kinds, subscription format, access | `internal/singbox` |
| [6. Accounting](06-accounting.md) | collection, rating, measurement limits, drift | `internal/rating`, `internal/account` |
| [7. Periods](07-periods.md) | Jalali anchor and boundary algorithm | `internal/jalali`, `internal/store` |
| [8. Quotas](08-quotas.md) | shares, blocking, household ceiling | `internal/quota`, `internal/enforce` |
| [9. Data model](09-data-model.md) | schema, subjects, flush cadence, migrations | `internal/store` |
| [10. Components](10-components.md) | process split, reconcile loop | `internal/singbox`, `internal/config` |
| [11. Config artefacts](11-config-artifacts.md) | nftables, generated sing-box, HTTP surface | `internal/enforce`, `internal/singbox` |
| [12. UI](12-ui.md) | admin, portal, subscription | — |
| [13. Security](13-security.md) | privacy, tokens, exposure, logging | — |
| [14. Operations](14-operations.md) | failure modes, backups, risks | — |
| [15. Build plan](15-build-plan.md) | phases, alternatives rejected, open items | — |
