package enforce

import (
	"strings"
	"testing"
)

func TestRulesetKeepsTheInvariants(t *testing.T) {
	got := Ruleset(Options{
		LanCIDR: "192.168.1.0/24", BoxIP: "192.168.1.2", RouterIP: "192.168.1.1",
		TProxyPort: 7895, PortalPort: 80, Iface: "eth0",
	})
	must := []string{
		// traffic to the box itself must never be intercepted, or a VLESS
		// listener would capture its own clients in a loop
		"ip daddr @box4 accept",
		// local-only destinations are never proxied or metered: mDNS and
		// other multicast must stay on the LAN
		"ip saddr @lan4 ip daddr @lan4 accept",
		"ip daddr 224.0.0.0/4 accept",
		"ip daddr 255.255.255.255 accept",
		"ip daddr 169.254.0.0/16 accept",
		"tproxy ip to :7895",
		"meta mark set 0x1",
		"ip saddr @lan4 ip daddr @router4 counter drop",
		"ip saddr @lan4 udp dport 443 counter reject",
		"ip saddr @blocked4 counter drop",
		"ip saddr @blocked4 tcp dport 80 accept",
		"redirect to :80",
		"update @devbytes { ip saddr }",
		"update @devbytes { ip daddr }",
	}
	for _, m := range must {
		if !strings.Contains(got, m) {
			t.Errorf("ruleset is missing %q", m)
		}
	}
	nat := NatRuleset(Options{LanCIDR: "192.168.1.0/24", Iface: "eth0"})
	if !strings.Contains(nat, `ip saddr 192.168.1.0/24 oifname "eth0" masquerade`) {
		t.Error("nat table must masquerade the LAN out the single NIC")
	}
	if !strings.Contains(nat, "ip daddr 192.168.1.0/24 return") ||
		!strings.Contains(nat, "ip daddr 224.0.0.0/4 return") {
		t.Error("nat table must leave local destinations unmangled")
	}
	if strings.Contains(got, "masquerade") {
		t.Error("masquerade belongs in the nat table, which bypass keeps")
	}

	// Local traffic must be accepted before the proxy counter and the drift
	// counters, or mDNS is swallowed and local bytes reach the ledger.
	for _, local := range []string{
		"ip saddr @lan4 ip daddr @lan4 accept",
		"ip daddr 224.0.0.0/4 accept",
		"ip daddr 255.255.255.255 accept",
		"ip daddr 169.254.0.0/16 accept",
	} {
		if strings.Index(got, local) > strings.Index(got, "tproxy ip to :7895") {
			t.Errorf("%q must precede the tproxy rule", local)
		}
		if strings.Index(got, local) > strings.Index(got, "update @devbytes { ip saddr }") {
			t.Errorf("%q must precede the devbytes counters", local)
		}
	}

	// The local-service rule must come before the block rule, or a blocked
	// device could not be told why it is blocked.
	if strings.Index(got, "ip daddr @box4 accept") > strings.Index(got, "ip saddr @blocked4 counter drop") {
		t.Error("box4 accept must precede the block drop")
	}
}
