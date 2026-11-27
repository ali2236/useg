package web

import "testing"

func TestBytes(t *testing.T) {
	for _, c := range []struct {
		n    int64
		l    string
		want string
	}{
		{-1, "fa", "—"},
		{0, "fa", "۰ بایت"},
		{449_000_000, "fa", "۴۴۹ مگابایت"},
		{1_200_000_000, "fa", "۱٫۲ گیگابایت"},
		{2_000_000_000, "fa", "۲ گیگابایت"},
		{1_200_000_000, "en", "1.2 GB"},
		{2_000_000_000, "en", "2 GB"},
	} {
		if got := Bytes(c.n, c.l); got != c.want {
			t.Errorf("Bytes(%d, %q) = %q, want %q", c.n, c.l, got, c.want)
		}
	}
}
