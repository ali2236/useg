#!/bin/bash
# Install or update useg on the gateway from a git checkout.
# Run as root from the checkout: sudo make install
# Idempotent: the same command installs, updates and restarts.
set -eu
# Non-login root shells (su, some panels) drop the sbin dirs; useradd, nft,
# dnsmasq, conntrack and sysctl all live there.
export PATH="/usr/local/sbin:/usr/sbin:/sbin:$PATH"

[ "$(id -u)" = 0 ] || { echo "run as root: sudo make install" >&2; exit 1; }
cd "$(dirname "$0")/.."

GO_VERSION=1.23.6
SB_VERSION=1.14.4
CONF=/etc/useg/config.yaml

die() { echo "install: $*" >&2; exit 1; }

# Bounded: a filtered host must fail, not hang the install. -f so a 4xx/5xx
# counts as a failure and the next mirror is tried.
fetch() {
  curl -fL --connect-timeout 10 --max-time 300 --retry 2 --retry-delay 2 -o "$1" "$2"
}

case $(uname -m) in
  x86_64)        ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "unsupported machine $(uname -m)" ;;
esac

# ---- dependencies ------------------------------------------------------------

MISSING=
command -v nft       >/dev/null || MISSING="$MISSING nftables"
command -v dnsmasq   >/dev/null || MISSING="$MISSING dnsmasq"
command -v sqlite3   >/dev/null || MISSING="$MISSING sqlite3"
command -v conntrack >/dev/null || MISSING="$MISSING conntrack"
command -v curl      >/dev/null || MISSING="$MISSING curl"
command -v useradd   >/dev/null || MISSING="$MISSING passwd"
if [ -n "$MISSING" ]; then
  echo "installing:$MISSING"
  apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y $MISSING
fi

# Go >= 1.23.6, because go.mod asks for it and Debian 12 ships 1.19.
have_go() {
  command -v go >/dev/null || return 1
  local v
  v=$(go env GOVERSION 2>/dev/null | sed 's/^go//')
  [ "$(printf '%s\n%s\n' "$GO_VERSION" "$v" | sort -V | head -n1)" = "$GO_VERSION" ]
}
if ! have_go; then
  # Debian 13's own repo already has 1.24, served by whatever (Iranian) apt
  # mirror the box uses; only older releases fall through to the tarball.
  echo "installing Go from apt"
  apt-get update -qq || true
  if ! DEBIAN_FRONTEND=noninteractive apt-get install -y golang-go >/dev/null \
      || ! hash -r || ! have_go; then
    echo "installing Go $GO_VERSION"
    got=
    # go.dev resolves to Google, unreachable from Iran; the mirror goes first.
    for base in ${GO_MIRROR:-https://mirrors.aliyun.com/golang https://go.dev/dl}; do
      if fetch /tmp/useg-go.tgz "$base/go$GO_VERSION.linux-$ARCH.tar.gz"; then
        got=1
        break
      fi
    done
    [ -n "$got" ] || die "could not download Go; install Go >= $GO_VERSION and rerun"
    rm -rf /usr/local/go
    tar -C /usr/local -xzf /tmp/useg-go.tgz
    ln -sf /usr/local/go/bin/go /usr/local/bin/go
    ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
    hash -r
  fi
fi

if [ ! -x /usr/local/bin/sing-box ]; then
  echo "installing sing-box $SB_VERSION"
  sb="https://github.com/SagerNet/sing-box/releases/download/v$SB_VERSION/sing-box-$SB_VERSION-linux-$ARCH.tar.gz"
  got=
  # GitHub stalls on some Iranian routes; the proxies serve the same asset.
  for url in ${SB_URL:-$sb https://ghfast.top/$sb https://gh-proxy.com/$sb}; do
    if fetch /tmp/useg-sb.tgz "$url"; then
      got=1
      break
    fi
  done
  [ -n "$got" ] || die "could not download sing-box; put the $SB_VERSION binary at /usr/local/bin/sing-box and rerun"
  tar -xzf /tmp/useg-sb.tgz -C /tmp
  install -m 0755 "/tmp/sing-box-$SB_VERSION-linux-$ARCH/sing-box" /usr/local/bin/sing-box
fi

echo "building"
# proxy.golang.org is Google; a cold module cache needs a mirror Iran can reach.
export GOPROXY=${GOPROXY:-https://goproxy.cn,https://mirrors.aliyun.com/goproxy/,direct}
mkdir -p bin
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/useg ./cmd/useg

# ---- config ------------------------------------------------------------------

mkdir -p /etc/useg
cfg() { sed -n "s/^[[:space:]]*$1:[[:space:]]*//p" "$CONF" | head -n1 | tr -d "\"'"; }
lan_ifaces() { ip -br link | awk '$1 != "lo" { print $1 }'; }

CONFIGURED=1
if [ ! -f "$CONF" ]; then
  if [ -t 0 ]; then
    echo
    echo "First install: the LAN facts and the admin password."
    ask() { local a; read -rp "$1 [$2]: " a; echo "${a:-$2}"; }
    valid_ip() {
      [ -n "$1" ] || return 1
      printf '%s' "$1" | awk -F. '
        NF != 4 { exit 1 }
        { for (i = 1; i <= 4; i++) if ($i !~ /^[0-9]+$/ || $i > 255) exit 1 }
      '
    }
    BOX=$(ask "LAN IP for this box" 192.168.100.2)
    until valid_ip "$BOX"; do
      echo "not an IPv4 address"
      BOX=$(ask "LAN IP for this box" 192.168.100.2)
    done
    BASE=${BOX%.*}
    ROUTER=$(ask "Router (ISP) IP" "$BASE.1")
    until valid_ip "$ROUTER"; do
      echo "not an IPv4 address"
      ROUTER=$(ask "Router (ISP) IP" "$BASE.1")
    done
    [ "$ROUTER" != "$BOX" ] || die "router and box IP are the same"
    IFACE=$(ask "LAN interface" "$(lan_ifaces | head -n1)")
    until ip link show "$IFACE" >/dev/null 2>&1; do
      echo "no such interface; have: $(lan_ifaces | tr '\n' ' ')"
      IFACE=$(ask "LAN interface" "$(lan_ifaces | head -n1)")
    done
    while :; do
      read -rsp "Admin password: " PW; echo
      [ -n "$PW" ] && [ "$PW" != change-me-before-first-boot ] && break
      echo "password must be set and not the example value"
    done
    echo "LAN CIDR: $BASE.0/24 (change lan_cidr in $CONF for anything else)"

    USEG_BOX=$BOX USEG_ROUTER=$ROUTER USEG_LAN=$BASE.0/24 USEG_PW=$PW USEG_IFACE=$IFACE \
    awk '
      BEGIN { qc = sprintf("%c", 39) }
      function q(s) { gsub(qc, qc qc, s); return qc s qc }
      /^iface:/         { print "iface: " ENVIRON["USEG_IFACE"]; next }
      /^lan_cidr:/      { print "lan_cidr: " ENVIRON["USEG_LAN"]; next }
      /^box_ip:/        { print "box_ip: " ENVIRON["USEG_BOX"]; next }
      /^router_ip:/     { print "router_ip: " ENVIRON["USEG_ROUTER"]; next }
      /^admin_password:/{ print "admin_password: " q(ENVIRON["USEG_PW"]); next }
      { print }
    ' deploy/config.example.yaml > "$CONF"
    chmod 0600 "$CONF"
    echo "wrote $CONF"
  else
    install -m 0600 deploy/config.example.yaml "$CONF"
    CONFIGURED=0
    echo "no terminal: wrote the example config; edit $CONF and rerun 'sudo make install'"
  fi
fi

# These two files are generated from the config so config.yaml stays the only
# source of truth. Assumes a /24 LAN (x.y.z.0/24), same as the example.
IFACE=$(cfg iface)
IFACE=${IFACE:-eth0}
if ! ip link show "$IFACE" >/dev/null 2>&1; then
  CANDIDATES=$(lan_ifaces)
  if [ "$(printf '%s\n' "$CANDIDATES" | grep -c .)" = 1 ]; then
    echo "install: iface '$IFACE' does not exist; using the only NIC, $CANDIDATES"
    IFACE=$CANDIDATES
    sed "s/^iface:.*/iface: $IFACE/" "$CONF" > "$CONF.tmp"
    chmod 0600 "$CONF.tmp"
    mv "$CONF.tmp" "$CONF"
  else
    die "iface '$IFACE' does not exist (have: $(echo $CANDIDATES)); set iface: in $CONF and rerun"
  fi
fi
BOX=$(cfg box_ip)
BASE=$(cfg lan_cidr); BASE=${BASE%.*}
ADMIN_PORT=$(cfg admin_listen); ADMIN_PORT=${ADMIN_PORT##*:}; ADMIN_PORT=${ADMIN_PORT:-8080}
PORTAL_PORT=$(cfg portal_listen); PORTAL_PORT=${PORTAL_PORT##*:}; PORTAL_PORT=${PORTAL_PORT:-80}

stop_service() { systemctl stop useg.service 2>/dev/null || true; }

# ---- install -----------------------------------------------------------------

stop_service
install -m 0755 bin/useg /usr/local/bin/useg
install -d -m 0750 /var/lib/useg
install -d -m 0755 /usr/local/share/useg

id useg >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin useg
chown -R useg:useg /var/lib/useg
# The unit runs as useg: it owns /var/lib/useg, and needs group read on the
# config (still not world-readable).
chown root:useg "$CONF"
chmod 0640 "$CONF"

install -d -m 0755 /etc/sysctl.d /etc/dnsmasq.d

sed "s/eth0/$IFACE/g" deploy/sysctl-useg.conf > /etc/sysctl.d/90-useg.conf
sysctl -q --system

sed \
  -e "s/^interface=.*/interface=$IFACE/" \
  -e "s/^dhcp-range=.*/dhcp-range=$BASE.50,$BASE.240,12h/" \
  -e "s/^dhcp-option=3,.*/dhcp-option=3,$BOX/" \
  -e "s/^dhcp-option=6,.*/dhcp-option=6,$BOX/" \
  -e "s|^address=/portal.gate/.*|address=/portal.gate/$BOX|" \
  deploy/dnsmasq-useg.conf > /etc/dnsmasq.d/useg.conf

# Fail here with dnsmasq's own message, not after a failed restart. Run it the
# way the unit does: Debian's systemd-helper sources /etc/default/dnsmasq and
# passes -7 $CONFIG_DIR plus $DNSMASQ_OPTS, which can change validity.
if [ -r /etc/default/dnsmasq ]; then . /etc/default/dnsmasq; fi
if ! testout=$(dnsmasq --test ${CONFIG_DIR:+-7 $CONFIG_DIR} ${DNSMASQ_OPTS:-} 2>&1); then
  printf '%s\n' "$testout" >&2
  die "dnsmasq rejects the generated /etc/dnsmasq.d/useg.conf"
fi

install -m 0644 deploy/useg.service /etc/systemd/system/useg.service
install -m 0755 deploy/useg-backup.sh /usr/local/bin/useg-backup.sh
install -m 0644 deploy/useg-backup.service /etc/systemd/system/useg-backup.service
install -m 0644 deploy/useg-backup.timer /etc/systemd/system/useg-backup.timer
install -m 0644 README.md /usr/local/share/useg/README.md

systemctl daemon-reload

if [ "$CONFIGURED" = 1 ]; then
  systemctl enable dnsmasq.service
  if ! systemctl restart dnsmasq.service; then
    journalctl -u dnsmasq -n 20 --no-pager || true
    die "dnsmasq failed to start; check interface=$IFACE and the dhcp-range in /etc/dnsmasq.d/useg.conf"
  fi
  systemctl enable useg.service
  systemctl restart useg.service
  sleep 2
  if ! systemctl is-active --quiet useg.service; then
    journalctl -u useg -n 20 --no-pager || true
    die "useg exited on startup; the journal above says why"
  fi
  systemctl enable --now useg-backup.timer
  cat <<EOF

useg installed and running.
  admin    http://$BOX:$ADMIN_PORT/admin
  portal   http://$BOX:$PORTAL_PORT/
  status   systemctl status useg
  logs     journalctl -u useg -f

Before the household notices anything:
  1. Turn the ISP router's DHCP server OFF. Two DHCP servers is the classic
     way this setup half-works.
  2. Check the box's own resolver: /etc/resolv.conf (or resolvectl) must not
     point at the router. Its DNS stops when its DHCP is off. Use
     217.218.127.127 or $BOX instead.
  3. Open the admin UI and set the Jalali anchor and pools (Period), then add
     people and routes.
  4. If your LAN is not a /24, edit lan_cidr in $CONF and run sudo make update.

Escape hatch: turn the router's DHCP back on and the box is out of the path.
Maintenance bypass: the switch on the admin page (survives restarts).
From a shell: nft delete table inet useg   (NAT stays in inet usegnat)
Undo that:   systemctl restart useg
EOF
else
  echo "useg NOT started. Edit $CONF, then: sudo systemctl enable --now useg"
fi
