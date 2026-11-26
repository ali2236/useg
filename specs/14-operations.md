# 14. Failure modes, operations, risks

## 14.1 Failure modes and operations

| Failure | Behaviour | Recovery |
|---|---|---|
| `sing-box` dies | house offline (fail-closed) | systemd restarts; after 3 failures the agent flips `bypass` and alerts |
| Agent dies | metering stops, proxy keeps running, nobody is blocked | systemd restarts; enforcement state is re-derived from the DB |
| DB corrupt | cannot start | nightly `.backup` copy; documented restore |
| Box dies | house offline | re-enable the router's DHCP; the router still works |
| Clock wrong | periods misaligned | NTP check in the health panel |
| Router DHCP accidentally on | two gateways, random bypass | agent detects a lease offer from another DHCP server and alarms |
| psiphon child dies | that route fails, everything else fine | supervision + exponential backoff |

Maintenance `bypass` is a first-class feature: it stops TPROXY, keeps plain
forwarding, records an event, and shows a banner until cleared.

Backups: nightly `sqlite3 .backup` to a local directory, 14 copies, plus the
generated configs.

## 14.2 Risks

| Risk | Impact | Mitigation |
|---|---|---|
| Poll-based accounting loses short-flow tails | small under-charge | 1 s poll; nightly drift report; escalate to flow-close events if it matters |
| Box is a single point of failure | house offline | fail-closed by design, `bypass` switch, systemd, documented revert |
| Shared-L2 ARP bypass | quota evasion | drop client→router and ICMP redirects; physical split in v2 |
| Multiplier ledger vs ISP's raw meter | bill overrun | household raw ceiling |
| SNI classification gaps | wrong rates | class `default` = 1.0; QUIC dropped; no discount for unidentifiable traffic |
| 30+ listeners and per-credential config growth | config churn | regenerated only on change; trivial at 5 persons |
| Jalali boundary bugs | wrong reset day | unit tests on Esfand, anchor 31, Nowruz |
