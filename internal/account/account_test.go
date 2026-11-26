package account

import (
	"net/netip"
	"testing"
)

func TestLocalDest(t *testing.T) {
	a := &Accountant{}
	a.lan, _ = netip.ParsePrefix("192.168.100.0/24")
	a.box, _ = netip.ParseAddr("192.168.100.3")

	local := []string{
		"192.168.100.1",   // router, inside lan_cidr
		"192.168.100.3",   // the box
		"192.168.100.255", // subnet broadcast
		"10.1.2.3",        // another private subnet
		"172.16.0.9",      // private
		"224.0.0.251",     // mDNS
		"239.255.255.250", // SSDP
		"255.255.255.255", // limited broadcast
		"169.254.1.1",     // link-local
		"127.0.0.1",       // loopback
		"0.0.0.0",         // unspecified
		"240.0.0.1",       // reserved
	}
	for _, ip := range local {
		if !a.localDest(ip) {
			t.Errorf("%s should be local", ip)
		}
	}

	wan := []string{"8.8.8.8", "1.1.1.1", "185.55.226.26", "2a00:1450::1", "", "example.com"}
	for _, ip := range wan {
		if a.localDest(ip) {
			t.Errorf("%s should not be local", ip)
		}
	}
}
