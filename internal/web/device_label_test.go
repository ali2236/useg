package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"useg/internal/store"
)

func TestDeviceLabel(t *testing.T) {
	for _, tc := range []struct {
		label, name, hostname, mac, want string
	}{
		{"Living Room TV", "Ali’s MacBook Pro", "Alis-MacBook-Pro", "aa:bb:cc:00:00:01", "Living Room TV"},
		{"", "Ali’s MacBook Pro", "Alis-MacBook-Pro", "aa:bb:cc:00:00:02", "Ali’s MacBook Pro"},
		{"", "", "Galaxy-S25-FE", "aa:bb:cc:00:00:03", "Galaxy S25 FE"},
		{"", "", "Ali’s-MacBook-Pro.local", "aa:bb:cc:00:00:04", "Ali’s MacBook Pro"},
		{"", "", "raspberry_pi_4", "aa:bb:cc:00:00:05", "raspberry pi 4"},
		{"", "", "", "aa:bb:cc:00:00:06", "aa:bb:cc:00:00:06"},
	} {
		got := deviceLabel(store.Device{Label: tc.label, Name: tc.name, Hostname: tc.hostname, MAC: tc.mac})
		if got != tc.want {
			t.Errorf("deviceLabel(%q,%q,%q) = %q, want %q", tc.label, tc.name, tc.hostname, got, tc.want)
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

func TestAdminSetsDeviceLabel(t *testing.T) {
	srv, db, _, _ := tserver(t)
	h := srv.AdminHandler()
	ok := do(h, "POST", "/admin/login", "10.66.0.3:5000", url.Values{"password": {"pw"}}, nil)
	var cookie *http.Cookie
	for _, c := range ok.Result().Cookies() {
		if c.Name == sessionCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie")
	}

	rec := do(h, "POST", "/admin/device", "10.66.0.3:5000", url.Values{
		"mac":    {"aa:bb:cc:00:00:03"},
		"action": {"label"},
		"label":  {"Living Room TV"},
	}, cookie)
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "devices.renamed_ok") {
		t.Errorf("label redirect = %q", loc)
	}
	dv, err := db.DeviceByMAC("aa:bb:cc:00:00:03")
	if err != nil {
		t.Fatal(err)
	}
	if dv.Label != "Living Room TV" {
		t.Fatalf("label = %q", dv.Label)
	}

	body := do(srv.PortalHandler(), "GET", "/", "10.66.0.3:5000", nil, nil).Body.String()
	if !strings.Contains(body, "Living Room TV") {
		t.Errorf("portal should show the override:\n%s", body)
	}
}
