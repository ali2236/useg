#!/bin/sh
# A "LAN client": static address on the LAN bridge, default gateway is the box.
set -e
ip route replace default via 10.66.0.2
printf 'nameserver 10.66.0.2\noptions ndots:1\n' > /etc/resolv.conf
echo "client ready: $(ip -4 addr show eth0 | grep inet) gw $(ip route | grep default)"
exec sleep infinity
