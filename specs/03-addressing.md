# 3. Addressing, DHCP, DNS

`dnsmasq` is authoritative for the LAN:

```
interface=eth0
dhcp-range=192.168.100.50,192.168.100.240,12h
dhcp-option=3,192.168.100.2            # gateway = box
dhcp-option=6,192.168.100.2            # DNS = box
dhcp-leasefile=/var/lib/misc/dnsmasq.leases
address=/portal.gate/192.168.100.2     # friendly portal name
address=/wifi.net/192.168.100.2        # the same page under a second name
no-resolv                            # ignore the host's /etc/resolv.conf
strict-order                         # try the servers below in this order
cache-size=10000
server=217.218.127.127               # TCI
server=217.218.155.155               # TCI
server=185.55.226.26                 # Begzar
server=1.1.1.1                       # Cloudflare
server=1.0.0.1                       # Cloudflare
server=8.8.8.8                       # Google
server=8.8.4.4                       # Google
server=9.9.9.9                       # Quad9
server=149.112.112.112               # Quad9
domain-needed
bogus-priv
```

The OS connectivity probes (`captive.apple.com`, `connectivitycheck.gstatic.com`,
`www.msftconnecttest.com`, `detectportal.firefox.com`) are deliberately not
listed, so they resolve upstream and a joining device simply declares the LAN
online. No sign-in sheet opens; the portal is reached by name (`portal.gate` or
`wifi.net`) or by the box address.

`no-resolv` and `strict-order` are deliberate. The host's `/etc/resolv.conf`
may list a public resolver such as `8.8.8.8`, which is commonly blocked or
intercepted on a TCI line; without `no-resolv` dnsmasq would use it alongside
the servers above and answers would vary by query. `strict-order` tries the
servers in the order listed and moves down only when one fails, so **the first
entry is the one that normally answers**: the domestic resolvers (TCI, Begzar)
come first and the public ones are failover only. Public DNS first would mean
every query waits for a blackholed resolver before falling back.

Radar is not in the list: its addresses (`10.202.10.202`, `10.202.10.102`) are
private and only reachable inside an ISP's own network, so they cannot answer
from a home LAN. Shecan and Begzar's second address (`185.55.225.25`) were
dropped after they stopped answering.

Plain UDP/53 to public resolvers is also commonly intercepted on these lines
(a blocked name can return `10.10.34.36` even when queried at `1.1.1.1`). So the
Cloudflare-first order is an availability choice, not a guarantee of unfiltered
answers; the long list is for failover, not for bypassing filtering.
Independent resolution needs DoH/DoT over a route and is out of scope for v1.
The ISP router is deliberately absent from the list: its DHCP server must be
off, and its DNS forwarder stops with it, so `192.168.100.1` is not a resolver.
The box is the only DNS on the LAN.

The box's own resolver is separate: point it at `217.218.127.127` or
`192.168.100.2` (dnsmasq), not the router and not a public resolver. glibc
reads at most three nameservers from `/etc/resolv.conf`; `systemd-resolved` can
hold the whole list.

The lease file is the device inventory's raw feed: MAC, IP, hostname, expiry.
The agent watches it, writes changes into `device`, and re-applies any block
that should follow the device to its new address.

The lease hostname is DHCP option 12. Some clients send the owner's name there
(`Galaxy-S25-FE`, `Alis-MacBook-Pro`); many send nothing. The agent also browses
mDNS on the LAN interface every 10 minutes for the names devices advertise
(`Ali’s MacBook Pro`, `Living Room TV`) and stores them in `device.name`. mDNS
is preferred for display, DHCP is the fallback, and a device that advertises
neither is shown by MAC. Phones usually do not answer mDNS, so their DHCP name
is the only one available.

Static reservations for admin machines, the router, and the box are optional
but recommended.
