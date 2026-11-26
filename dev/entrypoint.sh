#!/bin/sh
# Dev gateway: reproduce the box's kernel-side setup inside one container.
set -e

sysctl -w net.ipv4.ip_forward=1 >/dev/null 2>&1 || true
sysctl -w net.ipv4.conf.all.send_redirects=0 >/dev/null 2>&1 || true
sysctl -w net.ipv4.conf.eth0.send_redirects=0 >/dev/null 2>&1 || true
sysctl -w net.ipv4.conf.all.rp_filter=0 >/dev/null 2>&1 || true

# TPROXY sends marked packets to the local socket through table 100.
ip rule add fwmark 0x1 lookup 100 2>/dev/null || true
ip route add local 0.0.0.0/0 dev lo table 100 2>/dev/null || true

mkdir -p /var/lib/misc /var/lib/useg
# Static compose addresses, so the lease fixture is deterministic. On the box
# dnsmasq writes this file for real.
now=$(date +%s)
cat > /var/lib/misc/dnsmasq.leases <<EOF
$((now + 43200)) aa:bb:cc:00:00:03 10.66.0.3 client-a *
$((now + 43200)) aa:bb:cc:00:00:04 10.66.0.4 client-b *
EOF

useg migrate -c /etc/useg/config.yaml
if [ ! -f /var/lib/useg/.seeded ]; then
  useg seed -dev -c /etc/useg/config.yaml
  touch /var/lib/useg/.seeded
fi

# Keep dnsmasq in the foreground of a child shell so the agent stays PID 1.
dnsmasq --conf-file=/etc/dnsmasq.conf --no-daemon &
exec useg server -c /etc/useg/config.yaml
