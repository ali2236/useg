// Package names discovers the names devices advertise over mDNS (Bonjour).
// DHCP hostnames are the fallback: they are often a machine name such as
// "Alis-MacBook-Pro", while the name the owner chose ("Ali’s MacBook Pro")
// is only published over mDNS.
package names

import (
	"context"
	"io"
	"log"
	"net"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hashicorp/mdns"
)

// services are queried in preference order: the first name found for an IP
// wins. The first four carry the user-set name on Apple devices; the rest
// cover the common Android/TV/printer responders.
var services = []string{
	"_companion-link._tcp",
	"_airplay._tcp",
	"_raop._tcp",
	"_device-info._tcp",
	"_googlecast._tcp",
	"_spotify-connect._tcp",
	"_samsungmsf._tcp",
	"_samsungtvrc._tcp",
	"_workstation._tcp",
	"_smb._tcp",
	"_afpovertcp._tcp",
	"_ipp._tcp",
	"_printer._tcp",
}

const queryTimeout = 1200 * time.Millisecond

// Discover returns the advertised name per IPv4 address. It is best effort:
// no multicast, no responders and an empty map are normal outcomes.
func Discover(ctx context.Context, iface *net.Interface) map[string]string {
	out := map[string]string{}
	for _, svc := range services {
		if ctx.Err() != nil {
			break
		}
		for _, e := range query(ctx, iface, svc) {
			if e.AddrV4 == nil {
				continue
			}
			ip := e.AddrV4.String()
			if ip == "" || out[ip] != "" {
				continue
			}
			if n := instance(e.Name, svc); n != "" {
				out[ip] = n
			}
		}
	}
	return out
}

func query(ctx context.Context, iface *net.Interface, svc string) []*mdns.ServiceEntry {
	ch := make(chan *mdns.ServiceEntry, 64)
	_ = mdns.QueryContext(ctx, &mdns.QueryParam{
		Service:     svc,
		Domain:      "local",
		Timeout:     queryTimeout,
		Interface:   iface,
		Entries:     ch,
		DisableIPv6: true, // leases are IPv4; AAAA would add noise
		Logger:      log.New(io.Discard, "", 0),
	})
	close(ch)
	var out []*mdns.ServiceEntry
	for e := range ch {
		out = append(out, e)
	}
	return out
}

// instance extracts the display name from the presented DNS name, e.g.
// `Ali\226\128\153s\ MacBook\ Pro\ (2)._companion-link._tcp.local.` becomes
// `Ali’s MacBook Pro (2)`. The mDNS library returns names still DNS-escaped.
func instance(raw, service string) string {
	s := strings.TrimSuffix(raw, ".")
	if i := strings.Index(strings.ToLower(s), "."+service+"."); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(unescape(s))
	if s == "" || !utf8.ValidString(s) {
		return ""
	}
	if r := []rune(s); len(r) > 63 {
		s = string(r[:63])
	}
	return s
}

func unescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i == len(s)-1 {
			b.WriteByte(s[i])
			continue
		}
		if i+3 < len(s) && isDigit(s[i+1]) && isDigit(s[i+2]) && isDigit(s[i+3]) {
			n := int(s[i+1]-'0')*100 + int(s[i+2]-'0')*10 + int(s[i+3]-'0')
			if n <= 255 {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		i++
		b.WriteByte(s[i])
	}
	return b.String()
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
