package names

import (
	"strings"
	"testing"
)

func TestInstanceName(t *testing.T) {
	// Captured from a real Mac: UTF-8 arrives as decimal escapes.
	raw := `Ali\226\128\153s\ MacBook\ Pro\ (2)._companion-link._tcp.local.`
	if got, want := instance(raw, "_companion-link._tcp"), "Ali’s MacBook Pro (2)"; got != want {
		t.Errorf("instance = %q, want %q", got, want)
	}

	for _, tc := range []struct{ raw, service, want string }{
		{"Living Room TV._googlecast._tcp.local.", "_googlecast._tcp", "Living Room TV"},
		{"Galaxy-S25-FE._samsungmsf._tcp.local.", "_samsungmsf._tcp", "Galaxy-S25-FE"},
		{"Ali._AIRPLAY._tcp.local.", "_airplay._tcp", "Ali"},
		{"  spaced  ._ipp._tcp.local.", "_ipp._tcp", "spaced"},
		{`bad\255utf8._ipp._tcp.local.`, "_ipp._tcp", ""},
		{"", "_ipp._tcp", ""},
		{"._ipp._tcp.local.", "_ipp._tcp", ""},
	} {
		if got := instance(tc.raw, tc.service); got != tc.want {
			t.Errorf("instance(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}

	long := strings.Repeat("a", 80) + "._ipp._tcp.local."
	if got := instance(long, "_ipp._tcp"); len([]rune(got)) != 63 {
		t.Errorf("long name = %d runes, want 63", len([]rune(got)))
	}
}

func TestUnescape(t *testing.T) {
	for in, want := range map[string]string{
		`a\032b`:       "a b",
		`a\.b`:         "a.b",
		`a\\b`:         `a\b`,
		`plain`:        "plain",
		`trailing\`:    `trailing\`,
		`\226\128\153`: "’",
	} {
		if got := unescape(in); got != want {
			t.Errorf("unescape(%q) = %q, want %q", in, got, want)
		}
	}
}
