// Package enforce is the only place that touches nftables. Blocking lives here
// rather than in the proxy so it survives proxy restarts and needs no reload.
package enforce

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"sort"
	"strings"
)

type Options struct {
	LanCIDR    string
	BoxIP      string
	RouterIP   string
	TProxyPort int
	PortalPort int
	Iface      string
}

type Nft struct {
	opt     Options
	log     *slog.Logger
	enabled bool
	blocked map[string]bool
	applied bool
	dryErr  bool
}

func New(opt Options, enabled bool, log *slog.Logger) *Nft {
	return &Nft{opt: opt, enabled: enabled, log: log, blocked: map[string]bool{}}
}

// Ruleset renders the whole table. Golden-tested; keep it readable.
func Ruleset(o Options) string {
	return fmt.Sprintf(`table inet useg {
  set lan4     { type ipv4_addr; flags interval; elements = { %s } }
  set box4     { type ipv4_addr; elements = { %s } }
  set router4  { type ipv4_addr; elements = { %s } }
  set blocked4 { type ipv4_addr; flags interval; }
  set devbytes { type ipv4_addr; counter; }

  chain prerouting {
    type filter hook prerouting priority mangle; policy accept;

    # never intercept traffic addressed to the box itself: the portal, DNS,
    # the admin UI and every VLESS listener live there
    ip daddr @box4 accept

    # raw per-device counters, both directions: drift report only, never used
    # to charge or block
    ip saddr @lan4 update @devbytes { ip saddr }
    ip daddr @lan4 update @devbytes { ip daddr }

    # a blocked browser asking for any site gets the portal instead
    ip saddr @blocked4 tcp dport 80 accept
    ip saddr @blocked4 counter drop

    # no direct path to the ISP router
    ip saddr @lan4 ip daddr @router4 counter drop

    # QUIC off in v1: the hostname is invisible, so it cannot be rated
    ip saddr @lan4 udp dport 443 counter reject

    ip saddr @lan4 meta l4proto { tcp, udp } meta mark set 0x1 tproxy ip to :%d counter accept
  }

  chain dstnat {
    type nat hook prerouting priority dstnat; policy accept;
    ip saddr @blocked4 tcp dport 80 redirect to :%d
  }

  chain forward {
    type filter hook forward priority filter; policy accept;
    ip saddr @lan4 accept
    ip daddr @lan4 accept
  }
}`, o.LanCIDR, o.BoxIP, o.RouterIP, o.TProxyPort, o.PortalPort)
}

// NatRuleset is kept in its own table so bypass (which deletes the metering
// table) cannot leave the house without NAT.
func NatRuleset(o Options) string {
	return fmt.Sprintf(`table inet usegnat {
  chain postrouting {
    type nat hook postrouting priority srcnat; policy accept;
    ip saddr %s oifname "%s" masquerade
  }
}`, o.LanCIDR, o.Iface)
}

func (n *Nft) run(stdin string, args ...string) error {
	cmd := exec.Command("nft", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("nft %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// setupRouting installs the TPROXY policy routing. Idempotent; doing it here
// instead of a second systemd unit means it cannot drift from the ruleset.
func (n *Nft) setupRouting() {
	if _, err := exec.LookPath("ip"); err != nil {
		return
	}
	_ = exec.Command("ip", "rule", "add", "fwmark", "0x1", "lookup", "100").Run()
	_ = exec.Command("ip", "route", "add", "local", "0.0.0.0/0", "dev", "lo", "table", "100").Run()
}

func (n *Nft) Apply() error {
	if !n.enabled {
		n.log.Warn("enforcement disabled (config enforce=false): blocking is recorded but not applied")
		return nil
	}
	if _, err := exec.LookPath("nft"); err != nil {
		n.log.Error("nft not found; blocking will not be enforced")
		return err
	}
	n.setupRouting()
	if err := n.run(NatRuleset(n.opt), "-f", "-"); err != nil {
		return fmt.Errorf("nat table: %w", err)
	}
	if err := n.run(Ruleset(n.opt), "-f", "-"); err != nil {
		return err
	}
	n.applied = true
	n.log.Info("nftables ruleset applied", "lan", n.opt.LanCIDR)
	return nil
}

// Bypass removes the table: plain forwarding keeps working, nothing is metered
// or blocked. It is the maintenance switch, and it must stay trivial.
func (n *Nft) Bypass(on bool) error {
	if !n.enabled {
		return nil
	}
	if on {
		if err := n.run("", "delete", "table", "inet", "useg"); err != nil {
			n.log.Warn("bypass: delete table", "err", err)
		}
		n.applied = false
		n.blocked = map[string]bool{}
		n.log.Warn("bypass ON: TPROXY stopped, plain forwarding in effect")
		return nil
	}
	n.log.Warn("bypass OFF: re-applying the metered path")
	return n.Apply()
}

// SetBlocked applies the diff and flushes conntrack for newly blocked IPs so
// open flows cannot overshoot by more than one enforcement tick.
func (n *Nft) SetBlocked(ips []string) error {
	want := map[string]bool{}
	for _, ip := range ips {
		if ip != "" {
			want[ip] = true
		}
	}
	var add, del []string
	for ip := range want {
		if !n.blocked[ip] {
			add = append(add, ip)
		}
	}
	for ip := range n.blocked {
		if !want[ip] {
			del = append(del, ip)
		}
	}
	sort.Strings(add)
	sort.Strings(del)
	n.blocked = want
	if !n.enabled || !n.applied || (len(add) == 0 && len(del) == 0) {
		return nil
	}
	for _, ip := range add {
		if err := n.run("", "add", "element", "inet", "useg", "blocked4", "{", ip, "}"); err != nil {
			n.log.Error("block", "ip", ip, "err", err)
			continue
		}
		if err := exec.Command("conntrack", "-D", "-s", ip).Run(); err != nil {
			n.log.Debug("conntrack flush skipped", "ip", ip, "err", err)
		}
		n.log.Info("blocked device", "ip", ip)
	}
	for _, ip := range del {
		if err := n.run("", "delete", "element", "inet", "useg", "blocked4", "{", ip, "}"); err != nil {
			n.log.Error("unblock", "ip", ip, "err", err)
			continue
		}
		n.log.Info("unblocked device", "ip", ip)
	}
	return nil
}

// DevBytes reads the per-device nft counters. Drift check only: these cannot be
// weighted, so they can never be the truth.
func (n *Nft) DevBytes() (map[string]int64, error) {
	if !n.enabled {
		return nil, fmt.Errorf("enforcement disabled")
	}
	out, err := exec.Command("nft", "-j", "list", "set", "inet", "useg", "devbytes").Output()
	if err != nil {
		return nil, err
	}
	var doc struct {
		Nftables []struct {
			Set *struct {
				Elem []struct {
					Elem struct {
						Val     string `json:"val"`
						Counter struct {
							Bytes int64 `json:"bytes"`
						} `json:"counter"`
					} `json:"elem"`
				} `json:"elem"`
			} `json:"set"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, err
	}
	res := map[string]int64{}
	for _, item := range doc.Nftables {
		if item.Set == nil {
			continue
		}
		for _, el := range item.Set.Elem {
			res[el.Elem.Val] = el.Elem.Counter.Bytes
		}
	}
	return res, nil
}
