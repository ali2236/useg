// Package leases turns dnsmasq's lease file into the device inventory's raw
// feed. MAC is the stable key; IP is a property of the lease.
package leases

import (
	"bufio"
	"io"
	"os"
	"strings"
)

type Lease struct {
	Expiry   int64
	MAC      string
	IP       string
	Hostname string
}

// Parse reads dnsmasq's format: expiry mac ip hostname client-id. The hostname
// is `*` when the client did not send one.
func Parse(r io.Reader) ([]Lease, error) {
	var out []Lease
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 {
			continue
		}
		var l Lease
		l.MAC = NormalizeMAC(f[1])
		l.IP = f[2]
		if len(f) > 3 && f[3] != "*" {
			l.Hostname = f[3]
		}
		// expiry is int64 seconds; parse leniently, a corrupt line is skipped
		var e int64
		ok := true
		for _, c := range f[0] {
			if c < '0' || c > '9' {
				ok = false
				break
			}
			e = e*10 + int64(c-'0')
		}
		if !ok || l.IP == "" || l.MAC == "" {
			continue
		}
		l.Expiry = e
		out = append(out, l)
	}
	return out, sc.Err()
}

func Read(path string) ([]Lease, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Parse(f)
}

func NormalizeMAC(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.ReplaceAll(s, "-", ":")
}
