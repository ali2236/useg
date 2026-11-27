# 8. Quotas and enforcement

## 8.1 Shares

```
cap_i = weight_i × (pool_charged − guest_pool)   with Σ weight_i = 1
```

Five persons at the default weight 0.2 each ⇒ 20% each. Weights are
adjustable per person (a heavier user, a child's allowance).

`pool_charged = null` means the system meters and displays but blocks nobody —
the right default while calibrating against a real ISP cycle.

## 8.2 Blocking

Enforcement lives in **nftables, not the proxy**:

- The agent maintains a `blocked4` set of source IPs, updated by netlink.
- Dropping at prerouting is instant, survives proxy restarts, and needs no
  config reload.
- When a subject crosses its cap, the agent also flushes that device's
  conntrack entries (`conntrack -D -s <ip>`), which bounds overshoot from
  already-open flows to one enforcement tick (5 s).
- Blocked devices still reach: DNS on the box, `portal.gate`, the subscription
  endpoint, and plain HTTP is redirected to the portal so the user sees why.
  HTTPS shows a browser error — unavoidable without MITM, and the portal URL is
  printed in the message.

## 8.3 The household ceiling

Because multipliers make nimbaha traffic cheaper *for the person* than it is
*for the ISP*, the sum of person caps can exceed the real bill. The raw ceiling
is the guardrail: when household **raw** bytes reach `raw_ceiling`, everyone is
blocked and the admin is notified, regardless of individual headroom. Without
this, the multiplier system is a quiet way to overrun the plan. The ceiling
mirrors the pool: starting a cycle or adding data sets it to the new allowance,
so there is no separate knob to keep in sync.

## 8.4 Transfers between people

A bound person can give part of their **unused** share to another person from
the portal. The amount is checked against `cap − used` (including unflushed
usage) and the sender cannot transfer when unmetered or while caps are off.
The move is period-scoped and resets with the next period.

In a manual data cycle the transfer moves the two `allocation` rows, so the
admin's allocation controls and unallocated pool stay consistent. In a
weight-based period the `transfer` ledger is added to both caps at compute
time (`received − sent`), leaving the total of all caps unchanged.

## 8.5 Disabling all caps

The admin Household switch (`caps_off=1`) disables every cap — person shares,
the guest allowance, the guest pool and the household ceiling — while metering,
rating and usage history keep running. It is a runtime overlay, not a change to
the stored pools, so turning it off restores the previous limits unchanged.
Manual per-device and per-person blocks still apply; those are blocks, not caps.

