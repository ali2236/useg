package rating

import "testing"

func TestDomesticMatch(t *testing.T) {
	m := New([]string{"nimbaha.com", "prom.tabanbin.ir", "Example.COM"})
	cases := map[string]int64{
		"nimbaha.com":      NimbahaClassID,
		"api.nimbaha.com":  NimbahaClassID,
		"prom.tabanbin.ir": NimbahaClassID,
		"example.com":      NimbahaClassID,
		"example.com:443":  NimbahaClassID,
		"API.EXAMPLE.COM.": NimbahaClassID,
		"notexample.com":   DefaultClassID,
		"example.com.evil": DefaultClassID,
		"tabanbin.ir":      DefaultClassID,
		"":                 DefaultClassID,
		"1.2.3.4":          DefaultClassID,
	}
	for host, want := range cases {
		if got := m.Class(host); got != want {
			t.Errorf("Class(%q) = %d, want %d", host, got, want)
		}
	}
}

func TestRateMatrix(t *testing.T) {
	rates := map[int64]map[int64]float64{1: {2: 0.3}}
	if w := Rate(rates, 1, 2); w != 0.3 {
		t.Fatalf("nimbaha on primary = %v, want 0.3", w)
	}
	if w := Rate(rates, 1, 1); w != 1.0 {
		t.Fatalf("default = %v, want 1.0", w)
	}
	if w := Rate(nil, 2, 2); w != 1.0 {
		t.Fatalf("unset route = %v, want 1.0", w)
	}
}
