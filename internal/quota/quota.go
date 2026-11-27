// Package quota turns the ledger into a block set. The decision is a pure
// function so the portal, the tests and the enforcement tick all agree.
package quota

import (
	"fmt"
	"log/slog"
	"time"

	"useg/internal/config"
	"useg/internal/enforce"
	"useg/internal/store"
)

const (
	ReasonQuota     = "monthly quota exhausted"
	ReasonGuest     = "guest allowance exhausted"
	ReasonGuestPool = "guest pool exhausted"
	ReasonHousehold = "household raw ceiling reached"
	ReasonManual    = "blocked by admin"
)

type Status struct {
	Subject string `json:"subject"`
	Used    int64  `json:"used"`
	Cap     int64  `json:"cap"` // -1 = unmetered / unlimited
	Blocked bool   `json:"blocked"`
	Reason  string `json:"reason,omitempty"`
	Guest   bool   `json:"guest"`
}

type Input struct {
	Persons          []store.Person
	Devices          []store.Device
	Totals           map[string]store.Totals
	Allocations      map[int64]int64
	Blocks           map[string]store.Block
	Now              int64
	PoolCharged      *int64
	GuestPool        int64
	GuestDeviceBytes int64
	RawUsed          int64
	RawCeiling       *int64
	Manual           bool            // explicit per-person allocations instead of weights
	TransferNet      map[int64]int64 // received minus sent, weight periods only
}

type Decision struct {
	Subjects map[string]Status // person:<id> and dev:<mac>
	Blocked  map[string]string // mac -> human reason
}

func PersonSubject(id int64) string   { return fmt.Sprintf("person:%d", id) }
func DeviceSubject(mac string) string { return "dev:" + mac }

func blockActive(b store.Block, now int64) bool {
	return b.Until == nil || *b.Until > now
}

func Compute(in Input) Decision {
	out := Decision{Subjects: map[string]Status{}, Blocked: map[string]string{}}

	household := in.RawCeiling != nil && in.RawUsed >= *in.RawCeiling

	var totalWeight float64
	for _, p := range in.Persons {
		if p.Enabled {
			totalWeight += p.Weight
		}
	}
	// A person's usage is their own rows plus every device bound to them, so
	// guest bytes spent before binding stay on the person's ledger and
	// unbinding never refunds anything.
	owned := map[int64][]string{}
	for _, d := range in.Devices {
		if d.Owner != nil {
			owned[*d.Owner] = append(owned[*d.Owner], d.MAC)
		}
	}
	for _, p := range in.Persons {
		if !p.Enabled {
			continue
		}
		subj := PersonSubject(p.ID)
		used := store.ChargedBytes(in.Totals[subj].Charged)
		for _, mac := range owned[p.ID] {
			used += store.ChargedBytes(in.Totals[DeviceSubject(mac)].Charged)
		}
		st := Status{Subject: subj, Used: used, Cap: -1}
		if in.Manual {
			st.Cap = in.Allocations[p.ID]
		} else if in.PoolCharged != nil && totalWeight > 0 {
			base := float64(*in.PoolCharged - in.GuestPool)
			if base < 0 {
				base = 0
			}
			if p.CapOverride != nil {
				st.Cap = *p.CapOverride
			} else {
				st.Cap = int64(p.Weight / totalWeight * base)
			}
		}
		// A transfer moves share between two people; it never creates data, so
		// it is ignored while caps are off or the pool is unmetered.
		if st.Cap >= 0 {
			st.Cap += in.TransferNet[p.ID]
			if st.Used >= st.Cap {
				st.Blocked, st.Reason = true, ReasonQuota
			}
		}
		if b, ok := in.Blocks[subj]; ok && blockActive(b, in.Now) {
			st.Blocked, st.Reason = true, ReasonManual
		}
		out.Subjects[subj] = st
	}

	// Guests: first-come, one allowance each, bounded by the shared pool.
	var guestUsed int64
	for _, d := range in.Devices {
		if d.Owner == nil {
			guestUsed += store.ChargedBytes(in.Totals[DeviceSubject(d.MAC)].Charged)
		}
	}
	// What the pool still holds for guests; -1 means no shared bound. The
	// displayed cap folds this in so "remaining" is what the device can use now.
	poolLeft := in.GuestPool - guestUsed
	if !in.Manual && in.GuestPool <= 0 {
		poolLeft = -1
	} else if poolLeft < 0 {
		poolLeft = 0
	}
	for _, d := range in.Devices {
		if d.Owner != nil {
			continue
		}
		subj := DeviceSubject(d.MAC)
		st := Status{Subject: subj, Used: store.ChargedBytes(in.Totals[subj].Charged), Cap: in.GuestDeviceBytes, Guest: true}
		if poolLeft >= 0 && (st.Cap < 0 || st.Used+poolLeft < st.Cap) {
			st.Cap = st.Used + poolLeft
		}
		if st.Cap >= 0 && st.Used >= st.Cap {
			st.Blocked, st.Reason = true, ReasonGuest
		}
		if in.Manual && (in.GuestPool <= 0 || guestUsed >= in.GuestPool) {
			st.Blocked, st.Reason = true, ReasonGuestPool
		} else if !in.Manual && in.GuestPool > 0 && guestUsed >= in.GuestPool {
			st.Blocked, st.Reason = true, ReasonGuestPool
		}
		if b, ok := in.Blocks[subj]; ok && blockActive(b, in.Now) {
			st.Blocked, st.Reason = true, ReasonManual
		}
		out.Subjects[subj] = st
	}

	for _, d := range in.Devices {
		if d.IP == "" {
			continue
		}
		subj := DeviceSubject(d.MAC)
		if d.Owner != nil {
			subj = PersonSubject(*d.Owner)
		}
		if b, ok := in.Blocks[DeviceSubject(d.MAC)]; ok && blockActive(b, in.Now) {
			out.Blocked[d.MAC] = ReasonManual
			continue
		}
		if st, ok := out.Subjects[subj]; ok && st.Blocked {
			out.Blocked[d.MAC] = st.Reason
		}
	}

	if household {
		for subj, st := range out.Subjects {
			st.Blocked, st.Reason = true, ReasonHousehold
			out.Subjects[subj] = st
		}
		for _, d := range in.Devices {
			if d.IP != "" {
				out.Blocked[d.MAC] = ReasonHousehold
			}
		}
	}
	return out
}

func BlockedIPs(devices []store.Device, dec Decision) []string {
	var ips []string
	for _, d := range devices {
		if d.IP != "" && dec.Blocked[d.MAC] != "" {
			ips = append(ips, d.IP)
		}
	}
	return ips
}

func GuestDeviceBytes(s map[string]string) int64 {
	var gb float64 = 1
	if v := s["guest_device_gb"]; v != "" {
		fmt.Sscanf(v, "%g", &gb)
	}
	return int64(gb * 1e9)
}

func GuestPoolBytes(s map[string]string) int64 {
	var gb float64
	if v := s["guest_pool_gb"]; v != "" {
		fmt.Sscanf(v, "%g", &gb)
	}
	return int64(gb * 1e9)
}

// Gather loads everything Compute needs for one period.
func Gather(db *store.DB, period *store.Period) (Input, error) {
	in := Input{
		PoolCharged:      period.PoolCharged,
		GuestPool:        deref(period.GuestPool),
		RawCeiling:       period.RawCeiling,
		Now:              time.Now().Unix(),
		Manual:           period.AllocMode == "manual",
		Blocks:           map[string]store.Block{},
		GuestDeviceBytes: 0,
	}
	var err error
	if in.Persons, err = db.Persons(); err != nil {
		return in, err
	}
	if in.Devices, err = db.Devices(); err != nil {
		return in, err
	}
	if in.Totals, err = db.SubjectTotals(period.ID); err != nil {
		return in, err
	}
	if in.Allocations, err = db.Allocations(period.ID); err != nil {
		return in, err
	}
	// A manual cycle already moved the allocation rows; only weight cycles
	// derive caps from the transfer ledger.
	if !in.Manual {
		if in.TransferNet, err = db.TransferNet(period.ID); err != nil {
			return in, err
		}
	}
	blocks, err := db.Blocks()
	if err != nil {
		return in, err
	}
	for _, b := range blocks {
		in.Blocks[b.Subject] = b
	}
	if in.RawUsed, err = db.PeriodRaw(period.ID); err != nil {
		return in, err
	}
	settings, err := db.Settings()
	if err != nil {
		return in, err
	}
	in.GuestDeviceBytes = GuestDeviceBytes(settings)
	// A manual cycle stores 0 deliberately: guests get data only when the admin
	// allocates some. Only an unset period falls back to the setting.
	if period.GuestPool == nil {
		in.GuestPool = GuestPoolBytes(settings)
	}
	// The admin kill switch keeps metering but disables every cap, including
	// the period snapshot, so turning it back on restores the old limits.
	if settings["caps_off"] == "1" {
		in.PoolCharged = nil
		in.RawCeiling = nil
		in.GuestPool = 0
		in.GuestDeviceBytes = -1
		in.Manual = false
	}
	return in, nil
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// Engine is the 5-second enforcement tick.
type Engine struct {
	db      *store.DB
	nft     *enforce.Nft
	cfg     *config.Config
	log     *slog.Logger
	pending func() map[string]store.Totals
	prev    map[string]string
	ceiled  bool
}

func NewEngine(db *store.DB, nft *enforce.Nft, cfg *config.Config, log *slog.Logger,
	pending func() map[string]store.Totals) *Engine {
	return &Engine{db: db, nft: nft, cfg: cfg, log: log, pending: pending,
		prev: map[string]string{}}
}

// MergePending folds unflushed buckets into the totals, so enforcement sees
// traffic within one tick instead of one flush window.
func MergePending(totals map[string]store.Totals, pending map[string]store.Totals) {
	for subj, p := range pending {
		t := totals[subj]
		t.Charged += p.Charged
		t.Raw += p.Raw
		totals[subj] = t
	}
}

func (e *Engine) Tick(now time.Time) error {
	period, err := e.db.EnsurePeriod(now, e.cfg.Location())
	if err != nil {
		return err
	}
	e.expire(now)

	in, err := Gather(e.db, period)
	if err != nil {
		return err
	}
	if e.pending != nil {
		MergePending(in.Totals, e.pending())
	}
	dec := Compute(in)

	for mac, reason := range dec.Blocked {
		if e.prev[mac] == "" {
			_ = e.db.AddEvent("block", "dev:"+mac, reason)
			e.log.Warn("device blocked", "mac", mac, "reason", reason)
		}
	}
	for mac := range e.prev {
		if dec.Blocked[mac] == "" {
			_ = e.db.AddEvent("unblock", "dev:"+mac, "quota or block cleared")
			e.log.Info("device unblocked", "mac", mac)
		}
	}
	e.prev = dec.Blocked

	if in.RawCeiling != nil && in.RawUsed >= *in.RawCeiling && !e.ceiled {
		e.ceiled = true
		_ = e.db.AddEvent("raw_ceiling", "household", ReasonHousehold)
		e.log.Error("household raw ceiling reached; blocking everyone")
	}
	if in.RawCeiling == nil || in.RawUsed < *in.RawCeiling {
		e.ceiled = false
	}

	return e.nft.SetBlocked(BlockedIPs(in.Devices, dec))
}

// expire clears timed blocks and stale 30-day bindings.
func (e *Engine) expire(now time.Time) {
	blocks, err := e.db.Blocks()
	if err == nil {
		for _, b := range blocks {
			if b.Until != nil && *b.Until <= now.Unix() {
				_ = e.db.ClearBlock(b.Subject)
			}
		}
	}
	stale, err := e.db.ExpireBindings(now.Add(-30 * 24 * time.Hour).Unix())
	if err != nil {
		return
	}
	for _, d := range stale {
		_ = e.db.Unbind(d.MAC)
		_ = e.db.AddEvent("unbind", "dev:"+d.MAC, "binding expired after 30 days")
	}
}
