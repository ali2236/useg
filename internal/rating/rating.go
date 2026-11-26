// Package rating maps a sniffed hostname to a destination class and combines
// route and class weights into the multiplier used to charge a flow.
package rating

import (
	"strconv"
	"strings"
)

type Matcher struct {
	domestic map[string]struct{} // the ITO domestic-tariff domains
}

const (
	DefaultClassID int64 = 1
	NimbahaClassID int64 = 2
)

func New(domestic []string) *Matcher {
	m := &Matcher{domestic: make(map[string]struct{}, len(domestic))}
	for _, d := range domestic {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
			m.domestic[d] = struct{}{}
		}
	}
	return m
}

// Class returns `nimbaha` for a host on the domestic list and `default` for
// everything else. Unidentified or un-sniffable destinations are `default`
// (weight 1.0, never a discount).
func (m *Matcher) Class(host string) int64 {
	h := NormalizeHost(host)
	if h == "" || isIP(h) {
		return DefaultClassID
	}
	for {
		if _, ok := m.domestic[h]; ok {
			return NimbahaClassID
		}
		i := strings.IndexByte(h, '.')
		if i < 0 {
			return DefaultClassID
		}
		h = h[i+1:]
	}
}

func NormalizeHost(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	h = strings.TrimSuffix(h, ".")
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.Contains(h[i+1:], "]") {
		if _, err := strconv.Atoi(h[i+1:]); err == nil {
			h = h[:i]
		}
	}
	return h
}

func isIP(h string) bool {
	// Cheap and dependency-free: a hostname never has only digits and dots.
	if strings.Contains(h, ":") {
		return true
	}
	for _, c := range h {
		if (c < '0' || c > '9') && c != '.' {
			return false
		}
	}
	return strings.Count(h, ".") == 3
}

// Rate returns the charge multiplier for a route × class cell; a missing cell
// charges 1.0.
func Rate(rates map[int64]map[int64]float64, routeID, classID int64) float64 {
	if w, ok := rates[routeID][classID]; ok {
		return w
	}
	return 1
}
