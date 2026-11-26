package leases

import (
	"strings"
	"testing"
)

func TestParseDnsmasqLeases(t *testing.T) {
	raw := `1760000000 AA:BB:CC:00:11:22 192.168.1.50 pixel-6 01:aa:bb:cc:00:11:22
1760000100 aa-bb-cc-00-11-33 192.168.1.51 * *
1760000200 11:22:33:44:55:66 192.168.1.52 ipad *
garbage line
1760000300 22:33:44:55:66:77 192.168.1.53 tv
`
	ls, err := Parse(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 4 {
		t.Fatalf("got %d leases, want 4: %+v", len(ls), ls)
	}
	if ls[0].MAC != "aa:bb:cc:00:11:22" {
		t.Errorf("mac not normalized: %q", ls[0].MAC)
	}
	if ls[0].Hostname != "pixel-6" || ls[0].IP != "192.168.1.50" {
		t.Errorf("lease 0: %+v", ls[0])
	}
	if ls[1].Hostname != "" {
		t.Errorf("* hostname should be empty, got %q", ls[1].Hostname)
	}
	if ls[1].MAC != "aa:bb:cc:00:11:33" {
		t.Errorf("dash mac not normalized: %q", ls[1].MAC)
	}
	if ls[3].Hostname != "tv" {
		t.Errorf("4-field line: %+v", ls[3])
	}
}
