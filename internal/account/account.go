// Package account folds sing-box's per-connection counters into the usage
// ledger. One connection object per flow, so a flow is never counted twice.
package account

import (
	"fmt"
	"log/slog"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"useg/internal/rating"
	"useg/internal/singbox"
	"useg/internal/store"
)

type credRef struct {
	personID int64
	routeID  int64
}

type flow struct {
	subject    string
	routeID    int64
	classID    int64
	weight     float64
	up, down   int64
	classified bool
}

type bucketKey struct {
	subject string
	routeID int64
	classID int64
}

type bucket struct {
	up, down int64
	charged  float64
}

type state struct {
	routes   map[int64]store.Route
	directID int64
	creds    map[string]credRef
	matcher  *rating.Matcher
	rates    map[int64]map[int64]float64
	devices  map[string]store.Device // by ip
}

type Accountant struct {
	db    *store.DB
	clash *singbox.Client
	log   *slog.Logger
	lan   netip.Prefix
	box   netip.Addr

	mu        sync.Mutex
	periodID  int64
	flows     map[string]*flow
	buckets   map[bucketKey]*bucket
	lastFlush time.Time
	refreshed time.Time
	st        state
	bindSeen  map[string]time.Time
	lastErr   time.Time
}

func New(db *store.DB, clash *singbox.Client, log *slog.Logger, lanCIDR, boxIP string) *Accountant {
	a := &Accountant{
		db:       db,
		clash:    clash,
		log:      log,
		flows:    map[string]*flow{},
		buckets:  map[bucketKey]*bucket{},
		bindSeen: map[string]time.Time{},
	}
	if p, err := netip.ParsePrefix(lanCIDR); err == nil {
		a.lan = p.Masked()
	}
	if ip, err := netip.ParseAddr(boxIP); err == nil {
		a.box = ip
	}
	a.reloadLocked()
	a.lastFlush = time.Now()
	return a
}

// localDest is the accounting-side guarantee that only traffic leaving the LAN
// is metered. The nft rules already keep local destinations out of the proxy;
// this catches a stale ruleset, a moved box IP, or a proxy that accepted a
// multicast packet anyway.
func (a *Accountant) localDest(dst string) bool {
	addr, err := netip.ParseAddr(dst)
	if err != nil {
		return false // unknown/domain destination: not local
	}
	addr = addr.Unmap()
	if a.box.IsValid() && addr == a.box {
		return true
	}
	if a.lan.IsValid() && a.lan.Contains(addr) {
		return true
	}
	if addr.IsPrivate() || addr.IsLoopback() || addr.IsMulticast() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsUnspecified() {
		return true
	}
	if addr.Is4() {
		b := addr.As4()
		// limited broadcast and the reserved/experimental ranges
		if b == [4]byte{255, 255, 255, 255} || b[0] == 0 || b[0] >= 240 {
			return true
		}
	}
	return false
}

// RollPeriod flushes the old period's buckets before switching, so a boundary
// can never move bytes into the next month.
func (a *Accountant) RollPeriod(id int64, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.periodID != 0 && a.periodID != id {
		a.flushLocked(now)
	}
	a.periodID = id
}

func (a *Accountant) reloadLocked() {
	persons, err := a.db.Persons()
	if err != nil {
		a.log.Error("load persons", "err", err)
		return
	}
	routes, err := a.db.Routes()
	if err != nil {
		a.log.Error("load routes", "err", err)
		return
	}
	creds, err := a.db.Credentials()
	if err != nil {
		a.log.Error("load credentials", "err", err)
		return
	}
	domains, err := a.db.DomesticDomains()
	if err != nil {
		a.log.Error("load domestic domains", "err", err)
		return
	}
	rates, err := a.db.RateMap()
	if err != nil {
		a.log.Error("load rates", "err", err)
		return
	}
	devices, err := a.db.Devices()
	if err != nil {
		a.log.Error("load devices", "err", err)
		return
	}

	st := state{
		routes:  map[int64]store.Route{},
		creds:   map[string]credRef{},
		matcher: rating.New(domains),
		rates:   rates,
		devices: map[string]store.Device{},
	}
	direct := int64(0)
	byID := map[int64]store.Person{}
	for _, p := range persons {
		byID[p.ID] = p
	}
	for _, r := range routes {
		st.routes[r.ID] = r
		if direct == 0 && r.Enabled && r.Kind == "direct" {
			direct = r.ID
		}
	}
	st.directID = direct
	for _, c := range creds {
		r, ok := st.routes[c.RouteID]
		if !ok || !c.Enabled || !r.Enabled {
			continue
		}
		if p, ok := byID[c.PersonID]; !ok || !p.Enabled {
			continue
		}
		st.creds[singbox.InboundTag(c.PersonID, r.Name)] = credRef{c.PersonID, c.RouteID}
	}
	for _, d := range devices {
		if d.IP != "" {
			st.devices[d.IP] = d
		}
	}
	a.st = st
	a.refreshed = time.Now()
}

// inboundTag extracts the inbound tag. Pinned sing-box 1.14.4 has no
// metadata.inboundName; it puts "<inbound type>/<tag>" in metadata.type
// (verified with `useg probe`). Both shapes are accepted.
func inboundTag(m singbox.Metadata) string {
	if m.InboundName != "" {
		return m.InboundName
	}
	if i := strings.IndexByte(m.Type, '/'); i >= 0 {
		return m.Type[i+1:]
	}
	return ""
}

func hostOf(c singbox.Conn) string {
	if c.Metadata.Host != "" {
		return c.Metadata.Host
	}
	return c.Metadata.SniffHost
}

func (a *Accountant) newFlow(c singbox.Conn, now time.Time) *flow {
	if a.localDest(c.Metadata.DestinationIP) {
		return nil
	}
	f := &flow{up: c.Upload, down: c.Download}
	ref, laneB := a.st.creds[inboundTag(c.Metadata)]
	if laneB {
		f.routeID = ref.routeID
		a.bindFromVLESS(c.Metadata.SourceIP, ref.personID, now)
	} else {
		f.routeID = a.st.directID
	}
	// Attribution is by device, not by person: a person's cap is then the sum
	// of their devices, so binding carries guest usage over and unbinding can
	// never refund it. The inbound tag only chooses the route.
	if dev, ok := a.st.devices[c.Metadata.SourceIP]; ok && c.Metadata.SourceIP != "" {
		f.subject = "dev:" + dev.MAC
		a.refreshBinding(dev, now)
	} else if laneB {
		f.subject = fmt.Sprintf("person:%d", ref.personID)
	} else if c.Metadata.SourceIP != "" {
		f.subject = "dev:ip:" + c.Metadata.SourceIP
	} else {
		return nil // not attributable: don't invent a subject
	}
	f.classID = rating.DefaultClassID
	if h := hostOf(c); h != "" {
		f.classID = a.st.matcher.Class(h)
		f.classified = true
	}
	// Rate is fixed at first classification and reused for the flow's life.
	f.weight = rating.Rate(a.st.rates, f.routeID, f.classID)
	return f
}

// bindFromVLESS is the strong binding signal: an authenticated VLESS flow from
// this IP proves the device belongs to that person. Throttled to one write/hour.
func (a *Accountant) bindFromVLESS(ip string, personID int64, now time.Time) {
	dev, ok := a.st.devices[ip]
	if !ok || ip == "" {
		return
	}
	if dev.Owner != nil && *dev.Owner == personID &&
		dev.BoundAt != nil && now.Unix()-*dev.BoundAt < 3600 {
		return
	}
	if a.bindSeen[ip].Add(30 * time.Second).After(now) {
		return
	}
	a.bindSeen[ip] = now
	if err := a.db.Bind(dev.MAC, personID, "vless", now.Unix()); err != nil {
		a.log.Error("bind device", "mac", dev.MAC, "err", err)
		return
	}
	dev.Owner = &personID
	ts := now.Unix()
	dev.BoundAt = &ts
	a.st.devices[ip] = dev
	a.log.Info("device bound by VLESS", "mac", dev.MAC, "ip", ip, "person", personID)
}

// refreshBinding keeps the 30-day TTL alive for a device that only ever uses
// lane A. One write per device per hour at most.
func (a *Accountant) refreshBinding(dev store.Device, now time.Time) {
	if dev.Owner == nil || dev.BoundAt == nil || now.Unix()-*dev.BoundAt < 3600 {
		return
	}
	key := "touch:" + dev.MAC
	if a.bindSeen[key].Add(time.Hour).After(now) {
		return
	}
	a.bindSeen[key] = now
	if err := a.db.TouchBinding(dev.MAC, now.Unix()); err != nil {
		a.log.Error("refresh binding", "mac", dev.MAC, "err", err)
		return
	}
	ts := now.Unix()
	dev.BoundAt = &ts
	a.st.devices[dev.IP] = dev
}

func (a *Accountant) Tick(now time.Time) {
	// Fetch before taking the lock: a slow or wedged Clash API must not hold up
	// Pending(), and with it every admin request, behind a network timeout.
	conns, err := a.clash.Connections()
	if err != nil {
		if now.Sub(a.lastErr) >= time.Minute {
			a.lastErr = now
			a.log.Warn("clash api unavailable", "err", err)
		}
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if now.Sub(a.refreshed) > 20*time.Second {
		a.reloadLocked()
	}
	seen := make(map[string]bool, len(conns))
	for _, c := range conns {
		seen[c.ID] = true
		f := a.flows[c.ID]
		if f == nil {
			f = a.newFlow(c, now)
			if f == nil {
				continue
			}
			a.flows[c.ID] = f
		} else if !f.classified {
			// The host may only be sniffed after the connection appears; lock
			// the class the first time it becomes visible, never before.
			if h := hostOf(c); h != "" {
				f.classID = a.st.matcher.Class(h)
				f.weight = rating.Rate(a.st.rates, f.routeID, f.classID)
				f.classified = true
			}
		}

		du, dd := c.Upload-f.up, c.Download-f.down
		if du < 0 {
			du = 0 // counters are cumulative; never subtract a reset
		}
		if dd < 0 {
			dd = 0
		}
		f.up, f.down = c.Upload, c.Download
		if du == 0 && dd == 0 {
			continue
		}
		k := bucketKey{f.subject, f.routeID, f.classID}
		b := a.buckets[k]
		if b == nil {
			b = &bucket{}
			a.buckets[k] = b
		}
		b.up += du
		b.down += dd
		b.charged += float64(du+dd) * f.weight
	}
	for id := range a.flows {
		if !seen[id] {
			delete(a.flows, id)
		}
	}
	if now.Sub(a.lastFlush) >= 5*time.Minute {
		a.flushLocked(now)
	}
}

func (a *Accountant) flushLocked(now time.Time) {
	if len(a.buckets) == 0 {
		a.lastFlush = now
		return
	}
	rows := make([]store.UsageRow, 0, len(a.buckets))
	for k, b := range a.buckets {
		rows = append(rows, store.UsageRow{
			Subject: k.subject, RouteID: k.routeID, ClassID: k.classID,
			Up: b.up, Down: b.down, Charged: b.charged,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Subject != rows[j].Subject {
			return rows[i].Subject < rows[j].Subject
		}
		if rows[i].RouteID != rows[j].RouteID {
			return rows[i].RouteID < rows[j].RouteID
		}
		return rows[i].ClassID < rows[j].ClassID
	})
	if a.periodID == 0 {
		a.log.Warn("no period open; usage not flushed")
		return
	}
	if err := a.db.UpsertUsage(a.periodID, rows); err != nil {
		a.log.Error("flush usage", "err", err)
		return
	}
	a.log.Debug("usage flushed", "rows", len(rows))
	a.buckets = map[bucketKey]*bucket{}
	a.lastFlush = now
}

// Pending exposes the unflushed buckets. The quota tick and the UI must see
// the last few minutes, not the last 5-minute flush, or blocking would be late
// by a whole flush window.
func (a *Accountant) Pending() map[string]store.Totals {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[string]store.Totals, len(a.buckets))
	for k, b := range a.buckets {
		t := out[k.subject]
		t.Charged += b.charged
		t.Raw += b.up + b.down
		out[k.subject] = t
	}
	return out
}

// Close flushes everything still in memory. Called on shutdown.
func (a *Accountant) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.flushLocked(time.Now())
}
