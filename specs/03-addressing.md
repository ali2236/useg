# 3. Addressing, DHCP, DNS

`dnsmasq` is authoritative for the LAN:

```
interface=eth0
dhcp-range=192.168.1.50,192.168.1.240,12h
dhcp-option=3,192.168.1.2            # gateway = box
dhcp-option=6,192.168.1.2            # DNS = box
dhcp-leasefile=/var/lib/misc/dnsmasq.leases
address=/portal.gate/192.168.1.2     # friendly portal name
server=192.168.1.1                   # upstream resolver
domain-needed
bogus-priv
```

The lease file is the device inventory's raw feed: MAC, IP, hostname, expiry.
The agent watches it, writes changes into `device`, and re-applies any block
that should follow the device to its new address.

Static reservations for admin machines, the router, and the box are optional
but recommended.
