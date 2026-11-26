# useg — summary, goals, topology

A home gateway that meters every byte on the LAN, attributes it to a person or a
guest device, and enforces a monthly quota — while unknown devices work with no
software installed and V2Ray subscriptions serve as both the identity and the
routing contract.

## 1. Summary

The box becomes the LAN's only gateway: it hands out DHCP leases that point
clients at itself, captures all TCP and UDP into a userspace proxy, classifies
each flow by destination, forwards it over a chosen upstream route, and records
what it cost. Cost is not raw bytes: each flow is charged at
`rate[route][class]`, so a partner site on the primary route can count at 0.3.

People have shares of a monthly pool. Unknown devices get a small guest
allowance. A device becomes "someone's" by using that person's subscription,
which the proxy observes — no login page, no per-device app.

Everything runs as three processes on one Debian host: `dnsmasq`, `sing-box`,
and one Go binary (`useg`) that owns the database, the admin UI, the
subscription endpoint, and the quota loop.

## 2. Goals and non-goals

**Goals**

1. **G1** Every LAN device traverses the box with no client software.
2. **G2** Bytes are attributed to a subject, a route, and a destination class.
3. **G3** Persons get a weighted share of one monthly quota; over-quota means
   blocked, with a clear message.
4. **G4** The admin can add upstream routes (direct, psiphon, upstream V2Ray,
   SOCKS) that appear in the affected subscriptions without reissuing them.
5. **G5** Unknown devices get 1 GB and become a person's device by using that
   person's subscription.
6. **G6** Periods follow the Jalali calendar with an admin-chosen anchor day.
7. **G7** A non-technical household member can see their remaining quota.

**Non-goals (v1)**

- Content filtering, censorship circumvention policy, or any MITM/TLS
  interception.
- IPv6.
- Per-app or per-hour policy.
- High availability, multi-WAN, multi-site.
- ISP API integration or automatic billing reconciliation.
- Mobile apps; a web page is enough.

## 3. Topology and deployment

The ISP router stays where it is. The box is a normal host on the same LAN.

```
        ISP
         │
  home router 192.168.100.1
  NAT · DHCP OFF · unchanged
         │
  ───────┴───────── L2 switch ────────────────
     │                  │                  │
  useg box            APs (bridge)      devices
  192.168.100.2
  DHCP · DNS · gateway · proxy
```

Consequences of keeping the router in the path:

- Double NAT. Harmless here; the box NATs to `192.168.100.1`, the router NATs to
  the WAN.
- The box has **one NIC and one IP**. It forwards packets in and back out the
  same interface, which is legal with `ip_forward=1`.
- The router's DHCP server must be off. Two DHCP servers on one segment is the
  most common way this class of setup half-works.
- Reverting the whole system is "turn the router's DHCP back on". This is the
  designed escape hatch and must stay true.
- The box meters the LAN side; the ISP meters the WAN side after the router's
  NAT. They drift a little (NAT overhead, the router's own traffic). The
  household raw ceiling ([spec 8](08-quotas.md#84-the-household-ceiling)) is
  set a few percent below the ISP's number and the drift is displayed, not
  hidden.

The edge variant — modem bridged, box doing PPPoE — is a later option and
changes one field (upstream interface/gateway), not the architecture.

### 3.1 Host requirements

| Item | Value |
|---|---|
| OS | Debian 12/13, systemd |
| CPU | i5, mostly idle; ~1.5 Gbps realistic through TPROXY + sing-box |
| RAM | 16 GB (the data plane needs ~200 MB; the rest is headroom) |
| NIC | single, gigabit minimum |
| Time | NTP enabled — accounting depends on wall-clock period boundaries |
| Units | `dnsmasq`, `sing-box`, `useg` |

### 3.2 Kernel settings

```
net.ipv4.ip_forward = 1
net.ipv4.conf.all.send_redirects = 0     # ICMP redirects would tell clients to bypass the box
net.ipv4.conf.eth0.send_redirects = 0
net.ipv6.conf.all.disable_ipv6 = 1       # IPv6 out of scope for v1
```

`send_redirects = 0` matters: with the box and the router on the same segment,
the kernel would otherwise send ICMP redirects pointing clients straight at
`192.168.100.1`, silently defeating the whole system.
