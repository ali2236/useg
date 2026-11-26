# 5. Routes and subscriptions

A **route** is a server-side outbound chain plus a per-class rate and an access
rule. Every route terminates on the box; the subscription only names an
endpoint.

Route kinds:

| Kind | Implementation |
|---|---|
| `direct` | sing-box `direct`, NAT to `192.168.1.1` |
| `psiphon` | `psiphon-tunnel-core` child process, local SOCKS port, sing-box `socks` outbound |
| `v2ray` | sing-box outbound using the admin's VLESS/VMess/Trojan/SS settings |
| `socks` / `http` | any other upstream proxy |

## 5.1 Subscription format

`GET http://192.168.1.2/sub/<token>` (alias `http://portal.gate/sub/<token>`)
returns base64 of newline-separated URIs, one per enabled route:

```
vless://b1f0c2e4-…@192.168.1.2:10443?type=tcp&security=none#primary
vless://b1f0c2e4-…@192.168.1.2:10444?type=tcp&security=none#psiphon
vless://b1f0c2e4-…@192.168.1.2:10445?type=tcp&security=none#upstream
```

Properties this buys:

- **The admin can swap the psiphon server or the upstream V2Ray server and
  nothing changes for users.** The client config points at the box, always.
- **Revocation is one row**: disable a credential, the listener disappears, the
  person's next subscription fetch no longer lists it.
- **Per-route access** (`allow_all` or an explicit grant) simply omits routes
  the person may not use.
- One inbound listener per `(person, route)` — with 5 persons and ~6 routes that
  is 30 listeners, which sing-box handles without noticeable cost. It removes
  any dependency on per-user statistics APIs.

No TLS on the LAN hop in v1: it is a switched local network and identity comes
from the source address, so a stolen UUID buys nothing. Add REALITY later if the
LAN is ever untrusted.
