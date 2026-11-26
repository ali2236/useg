# 4. Identity and enrollment

`device` is keyed by MAC (stable across IP changes); the IP is a lease
property. A subject is either a `person` or a `guest device`.

| Subject | Allowance per period |
|---|---|
| Person | `weight × (pool − guest_pool)` |
| Unbound device | 1 GB from `guest_pool` |

## 4.1 How a device becomes a person's device

No extra app, no captive-portal login:

1. The person imports their subscription URL into a V2Ray client, or simply
   opens it in a browser on the device.
2. The box sees that request (the subscription fetch over HTTP, or the first
   authenticated VLESS connection) from source IP *X*.
3. IP *X* → MAC via the lease table ⇒ `device.owner_person = P`,
   `bound_at = now`, TTL 30 days, refreshed on every subsequent contact.

Both signals are accepted; the VLESS connection is the stronger one and
overrides a stale HTTP-derived binding.

## 4.2 Rules that keep the model honest

- **Quota follows the person, not the device.** Five devices buy no extra
  bytes; the person's cap is shared across all of them.
- **Binding carries over guest usage.** Bytes already spent as a guest are
  added to the person's ledger. Without this, unbind/rebind is a free reset.
- **Unbinding never refunds.** Same reason.
- **Revoking a credential or pressing "unbind" returns the device to guest.**
- **MAC randomization creates a new guest device.** Accepted; the 1 GB cap
  bounds the damage.
- **Guest allowance is per device per period**, not lifetime.
- **`guest_pool` is a hard ceiling.** Once it is exhausted, new guest devices
  get nothing until the next period. Guests are first-come, and the admin can
  see how much of the pool has been claimed.

## 4.3 Anti-bypass

- nftables drops client→`192.168.1.1` so nobody can set the router as gateway.
- `send_redirects=0` so the kernel cannot hand out a shortcut.
- Residual hole: on a shared L2 a device can ARP-spoof the router and bypass
  the box entirely. Closing that needs a separate segment (VLAN or a second
  NIC) and is out of scope for a family LAN. Documented, not pretended away.
