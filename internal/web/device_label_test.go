package web

import (
	"strings"
	"testing"

	"useg/internal/store"
)

func TestDeviceLabel(t *testing.T) {
	for _, tc := range []struct {
		name, hostname, mac, want string
	}{
		{"Ali’s MacBook Pro", "Alis-MacBook-Pro", "aa:bb:cc:00:00:01", "Ali’s MacBook Pro"},
		{"", "Galaxy-S25-FE", "aa:bb:cc:00:00:02", "Galaxy S25 FE"},
		{"", "Ali’s-MacBook-Pro.local", "aa:bb:cc:00:00:03", "Ali’s MacBook Pro"},
		{"", "raspberry_pi_4", "aa:bb:cc:00:00:04", "raspberry pi 4"},
		{"", "", "aa:bb:cc:00:00:05", "aa:bb:cc:00:00:05"},
	} {
		got := deviceLabel(store.Device{Name: tc.name, Hostname: tc.hostname, MAC: tc.mac})
		if got != tc.want {
			t.Errorf("deviceLabel(%q,%q) = %q, want %q", tc.name, tc.hostname, got, tc.want)
		}
	}
}

func TestPortalPrefersAdvertisedName(t *testing.T) {
	srv, db, _, _ := tserver(t)
	if err := db.SetDeviceName("10.66.0.3", "Ali’s iPhone"); err != nil {
		t.Fatal(err)
	}
	rec := do(srv.PortalHandler(), "GET", "/", "10.66.0.3:5000", nil, nil)
	body := rec.Body.String()
	if !strings.Contains(body, "Ali’s iPhone") {
		t.Errorf("portal should show the mDNS name:\n%s", body)
	}
	if strings.Contains(body, ">phone<") {
		t.Error("portal should not fall back to the DHCP name when mDNS has one")
	}
}
