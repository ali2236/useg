package store

import "testing"

func TestChargedBytesNeverUndercounts(t *testing.T) {
	for _, c := range []struct {
		charged float64
		want    int64
	}{
		{-1, 0},
		{0, 0},
		{1, 1},
		{1.0001, 2},
		{4e9 + 0.5, 4000000001},
	} {
		if got := ChargedBytes(c.charged); got != c.want {
			t.Errorf("ChargedBytes(%v) = %d, want %d", c.charged, got, c.want)
		}
	}
}
