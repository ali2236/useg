# useg

A home gateway that meters every byte on the LAN, attributes it to a person or a
guest device, and enforces a monthly quota — with no software on the client and
V2Ray subscriptions as both identity and routing contract.

The design lives in [the specs](specs/README.md). This README is the operator's view:
what runs, how to try it on a Mac, how to install it on the Debian box, and where
this implementation deliberately differs from the document.

```
client ─▶ nftables prerouting ─TPROXY─▶ sing-box ─▶ outbound ─▶ ISP router
                                              │
                                    Clash API /connections (1 Hz)
                                              │
                             usage buckets ─▶ quota ─▶ nft blocked4
```

Three processes on one host: `dnsmasq` (DHCP/DNS), `sing-box` (data plane, pinned
1.14.4, supervised by the agent), and one Go binary `useg` (database, accounting,
quota loop, portal, subscription endpoint, admin UI).

## Status: v1

Working and verified end to end:

| | |
|---|---|
| P1 gateway | nftables TPROXY, one NIC, double NAT, `send_redirects=0`, bypass switch |
| P2 capture & count | 250 ms `/connections` poll, per-connection deltas, 5-minute flush, nft raw counters + drift line in admin |
| P3 identity | dnsmasq lease file → devices, binding by subscription fetch and by VLESS source IP, 30-day TTL, guest allowance |
| P4 quotas | Jalali periods from a fixed anchor, weighted shares, guest pool, nft block set + conntrack flush, Persian portal |
| P5 routes | route CRUD (direct / v2ray / socks / http), one VLESS listener per (person, route), subscription endpoint, revocation by disabling a credential |
| P6 polish | server-rendered admin, audit log, nightly backup, `/admin/api/summary` |

Not in v1: the psiphon child process, per-route grants (`allow_all=false` routes
are hidden from everyone), IPv6, content filtering, TLS on the LAN hop.

## Try it on a Mac

The data plane is Linux-only (TPROXY, nftables, conntrack), so the Mac runs the
same Linux binary in Docker, with two container "LAN clients". Docker Desktop
works; [Kiri](https://kiri.dev) is what this was developed on.

```sh
make test        # unit tests, native, no network
make dev         # build the Linux binary + docker compose up
```

Then:

| | |
|---|---|
| portal | http://localhost:8080 — from a browser this is an *unknown* device, which is correct |
| admin | http://localhost:8081/admin — password `dev` |
| a real client | `make a-curl` (client-a → box → internet, TPROXY) |
| lane B (VLESS) | disable with a real V2Ray client, or see below |
| P0 probe | `make probe` while a transfer is open |

A "device" only appears when its source IP has a lease. The dev rig writes two
static leases for the two client containers, so:

```sh
# portal as client-a sees it (Persian, guest 5 MB)
docker compose exec client-a curl -s http://10.66.0.2:8080/ | grep -o 'قطع شده.*'
# make client-a a person's device by fetching that person's subscription
TOKEN=$(sqlite3 dev/data/useg.db "select sub_token from person where id=1")
docker compose exec client-a curl -s "http://10.66.0.2:8080/sub/$TOKEN"
docker compose exec client-a curl -s http://10.66.0.2:8080/    # now shows person: ali
```

Seeded dev numbers are deliberately tiny (50 MB charged pool, 20 MB guest pool,
5 MB per guest device) so quota behaviour is visible in minutes instead of weeks.
`make dev-fresh` wipes the database and reseeds.

The dev network is `10.66.0.0/24`, not your home `192.168.1.0/24`, so the two
never fight over routes.

### Docker and the network

The compose project reproduces the box's topology inside one bridge: the gateway
at `10.66.0.2` is the clients' default route, and `10.66.0.1` (the bridge's own
gateway) plays the ISP router. **Docker Hub is blocked on some networks**; the
Dockerfiles pull `debian` and `alpine` from `docker.m.daocloud.io`. On a network
where Hub is reachable, drop that prefix.

## Install on the Debian box

The box builds from a git checkout; there is nothing to cross-compile or scp.
Make the project a public repository and commit it (nothing secret is tracked:
the LAN facts live in `/etc/useg/config.yaml` and the database in
`/var/lib/useg`, both created on the box):

```sh
git init && git add . && git commit -m "useg"
git remote add origin <public-repo-url>
git push -u origin main
```

Then on the box:

```sh
sudo apt install -y git
sudo git clone <public-repo-url> /opt/useg
cd /opt/useg
sudo make install
```

`make install` does the whole first-run setup:

- installs `nftables`, `dnsmasq`, `sqlite3`, `conntrack` and `curl` with apt;
- installs Go 1.23.6 if the system has no Go or one older than that (Debian 12
  ships 1.19), and the pinned sing-box 1.14.4 to `/usr/local/bin/sing-box`;
- builds `useg` and installs it, the systemd units, the backup timer and this
  README to `/usr/local/share/useg`;
- asks for the box's LAN IP, the router IP and the admin password, writes
  `/etc/useg/config.yaml` (mode 0600) and derives `lan_cidr` as `x.y.z.0/24`;
- generates `/etc/dnsmasq.d/useg.conf` and `/etc/sysctl.d/90-useg.conf` from
  that config, so edit the config and not those two files;
- enables and starts `useg`, `dnsmasq` and the nightly backup timer.

Rerunning `sudo make install` is safe: it prompts nothing, keeps the config and
the database, and restarts the service.

Then, in order:

1. Turn the ISP router's DHCP **off**. This is the one unavoidable step; two
   DHCP servers on one segment is how this class of setup half-works.
2. First boot creates the schema, but no data. Open `http://<box-ip>/admin`, set
   the Jalali anchor and the two pools (Period), then add people and routes.

If your LAN is not a /24, edit `lan_cidr` in `/etc/useg/config.yaml` and run
`sudo make update`.

Admin is on `:8080` over plain HTTP on purpose (specs/13-security.md): on a family LAN
that is acceptable; reach it through an SSH tunnel if it is not.

### Update

```sh
git push                                   # from the machine you edit on
ssh root@gateway 'cd /opt/useg && make update'
```

`make update` is `git pull --ff-only` plus `make install`: rebuild, reinstall
the binary, units and generated files, restart `useg` and `dnsmasq`. It never
touches `/etc/useg/config.yaml` or the database. Do not edit files inside
`/opt/useg` — the pull will refuse and you will have to stash first. Logs are
`journalctl -u useg -f`.

### Day-to-day

| Task | Command |
|---|---|
| Update to the latest commit | `cd /opt/useg && sudo make update` |
| Maintenance bypass (plain routing, nothing counted) | the admin banner switch; it survives restarts |
| Give everyone unlimited internet (still metered) | the admin Household "Disable all caps" switch |
| Back to metered | the same switch |
| Bypass from a shell (UI unreachable) | `nft delete table inet useg` — NAT stays, traffic flows |
| Undo the shell bypass | `systemctl restart useg` (rebuilds the table and the block set) |
| Restore a backup | `systemctl stop useg; cp backups/useg-YYYYMMDD-HHMM.db /var/lib/useg/useg.db; systemctl start useg` |
| Escape hatch, whole system | turn the router's DHCP back on |
| Upcoming Jalali boundaries | `useg period -c /etc/useg/config.yaml` |

## P0 result — sing-box 1.14.4

The design flagged `metadata.inboundName` as unverified. Measured with
`useg probe` against live TCP and VLESS connections on this build:

| field | result |
|---|---|
| `metadata.host` | present (sniffed SNI) |
| `metadata.sourceIP`, `destinationIP/Port` | present |
| `upload`, `download` | present, cumulative per connection |
| `metadata.inboundName` | **absent** |
| `metadata.type` | `vless/p1:primary` — inbound type and tag, gives lane B identity |
| `chains` | outbound tags (`["direct"]`, `["r1"]`) |
| `rule` | `inbound=p1:primary => route(r1)` |

`useg` reads `metadata.type` (falling back to `inboundName` if it ever appears),
so no per-credential outbound is needed. Verified: a VLESS flow shows
`type: vless/p1:primary`, `chains: ["r1"]`.

## Deliberate differences from the design (specs v0.2)

Each is smaller than the document's version and keeps the same behaviour:

1. **Usage is attributed to the device, not to the person, then summed.** A
   person's cap is their own rows plus every bound device's rows. Binding
   therefore carries guest usage over and unbinding can never refund it, without
   a usage-migration step. The inbound tag still chooses the route, and a VLESS
   connection still binds the device.
2. **The period boundary tick lives in the agent** (once a minute, idempotent),
   not in a systemd timer. One less unit, and it works in the dev container.
3. **Anchor and pools live in the database** (`setting` table, editable in the
   admin UI) instead of YAML, so there is one source of truth. The config file
   only holds host and network facts.
4. **nftables is driven by `nft -f -`** (whole atomic ruleset) and `nft add
   element` for the block set, not netlink. Same semantics, no extra dependency.
   The TPROXY policy routing (`ip rule`/`ip route`) is installed by the agent at
   startup instead of by a unit.
5. **Weights are normalised by the sum of enabled weights**, so a household that
   does not sum to exactly 1.0 still cannot over-allocate the pool.
6. **`guest_device_gb` is a third knob** (per-device guest allowance, default
   1 GB) alongside the shared `guest_pool_gb` ceiling.
7. **Route access is `allow_all` only**; `allow_all=false` hides the route from
   every subscription. Per-route grants need one more table and are deferred.
8. **The psiphon route kind is a label in v1**: the outbound is whatever
   `upstream_json` says. The supervisor has no psiphon child yet.
9. **The `/connections` poll runs at 250 ms, not 1 Hz.** The Clash API only
   lists *open* connections, so a 1 s poll does not lose a short flow's tail —
   it loses the whole flow, which for page-load traffic is most of it. 250 ms
   is still cheap on an i5 and cuts the blind spot by ~4x. The residual loss is
   what the drift line measures.
10. **NAT lives in its own `inet usegnat` table**, so the bypass switch (which
    deletes the metering table) cannot cut the house off. On the box the ISP
    router would NAT anyway; in the dev rig this is what makes bypass behave
    like the real deployment.

Also: the design's worked example for anchor day 31 is arithmetically wrong —
Mehr through Bahman are 30 days, so `1405/07/30 → 1405/08/30`, not `→ 08/31`.
The code clamps per month from the anchor and is unit-tested for that case.

## Tests

`make test` runs everything locally: Jalali boundaries (Esfand leap/common,
anchor 31, anchor 1, a window across Nowruz, the non-drifting clamp), rating
(longest match, unknown ⇒ 1.0, route×class matrix), quota decisions (shares,
guest pool, household ceiling, manual blocks, carry-over across
devices), dnsmasq lease parsing, sing-box config generation (determinism,
listener per credential, disabled routes, loopback-pinned Clash API), the nft
ruleset invariants, and the web surfaces (portal, subscription binding rules,
admin session).

## Known limits

- **Short flows are under-counted.** Anything that opens and closes between two
  polls is invisible to the ledger. The drift line in the admin (nft counters vs
  ledger, both directions) is the honest measure; it is only meaningful over
  hours, not over one download. Fixing it properly needs flow-close events or
  eBPF — out of scope for poll-based v1, as specs/06-accounting.md says.
- **The ledger and the ISP's meter drift** for reasons that have nothing to do
  with bugs: NAT overhead, the router's own traffic, retransmits. Set
  `raw_ceiling` a few percent below the ISP's number.
- **A device that ARP-spoofs the router bypasses the box entirely.** Same
  residual hole the design documents; it needs a VLAN or a second NIC.
- **`allow_all=false` routes are invisible to everyone** in v1; there is no
  per-route grant table yet.
- **Admin is plain HTTP on the LAN.** A LAN attacker can sniff the session
  cookie. Use an SSH tunnel if that matters.

## Privacy

Only metadata is measured: SNI/host, destination, byte counts, timing. No TLS
interception, no content. QUIC is dropped in v1 because its hostname is
invisible. Tell the household the gateway exists — an operator who does not is
lying to the people whose traffic they meter.

## License

MIT — see [LICENSE](LICENSE).
