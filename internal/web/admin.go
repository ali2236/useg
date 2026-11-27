package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"useg/internal/jalali"
	"useg/internal/quota"
	"useg/internal/store"
)

type personRow struct {
	P            store.Person
	Status       quota.Status
	Used         int64
	Cap          int64
	Allocated    int64
	Remaining    int64
	CapGB        string
	SubURL       string
	Pct          int
	AllocPct     int
	UsedPct      int
	DataAllocPct int
	DataUsedPct  int
	Devices      int
	DeviceRows   []deviceRow
}

type deviceRow struct {
	D           store.Device
	Used        int64
	BlockReason string
	LastSeen    string
	LastUsed    string
	OwnerID     int64
	OwnerName   string
	GuestCap    int64
	GuestPct    int
}

type rateCell struct {
	RouteID   int64
	ClassID   int64
	ClassName string
	Weight    float64
}

type rateRow struct {
	Route store.Route
	Cells []rateCell
}

type usageRow struct {
	Subject, Route, Class string
	Up, Down              int64
	Charged               int64
}

type eventRow struct {
	When, Kind, Subject, Detail string
}

const domesticPerPage = 50

type adminData struct {
	Lang  string
	Dir   string
	Flash string
	Error string

	Bypass      bool
	CapsOff     bool
	PeriodStart string
	PeriodEnd   string

	Charged, Pool int64
	PoolSet       bool
	PoolPct       int
	Over          bool
	GuestUsed     int64
	GuestPool     int64
	RawUsed       int64
	RawCeiling    int64
	RawCeilingSet bool

	DriftAvailable bool
	NftTotal       int64
	LedgerRaw      int64
	DriftPct       float64

	DeviceCount  int
	PersonCount  int
	BlockedCount int
	OnlineCount  int

	CycleDaysLeft int
	Allocated     int64
	Unallocated   int64
	Unused        int64
	UnallocPct    int
	UnusedPct     int

	Manual            bool
	CycleLength       int
	CycleUnit         string
	PersonAllocated   int64
	GuestRemaining    int64
	GuestAllocPct     int
	DataPoolGB        string
	DataGuest         int64
	DataAllocated     int64
	DataUnallocated   int64
	DataGuestAllocPct int
	DataGuestUsedPct  int
	DataUnallocPct    int
	DataUnused        int64
	DataUnusedPct     int

	PoolGB, GuestPoolGB, GuestDeviceGB string

	Persons   []personRow
	Devices   []deviceRow
	Connected []deviceRow
	Guests    []deviceRow
	Routes    []store.Route
	Classes   []store.Class
	RateRows  []rateRow
	Usage     []usageRow
	Events    []eventRow

	Domestic          []store.DomesticSite
	DomesticTotal     int
	DomesticPage      int
	DomesticPages     int
	DomesticPrev      int
	DomesticNext      int
	DomesticHasPrev   bool
	DomesticHasNext   bool
	DomesticQuery     string
	DomesticRefreshed string
}

// deviceActivity is the last time a device did something visible: moved bytes,
// or first appeared in a lease if it has never been used.
func deviceActivity(dv store.Device) int64 {
	if dv.LastUsed > dv.FirstSeen {
		return dv.LastUsed
	}
	return dv.FirstSeen
}

func (s *Server) adminData(r *http.Request, flash, errMsg string) (*adminData, error) {
	now := time.Now()
	period, err := s.db.EnsurePeriod(now, s.loc)
	if err != nil {
		return nil, err
	}
	in, err := quota.Gather(s.db, period)
	if err != nil {
		return nil, err
	}
	s.withPending(&in)
	dec := quota.Compute(in)
	settings, err := s.db.Settings()
	if err != nil {
		return nil, err
	}

	l := adminLang(r)
	d := &adminData{
		Lang:          l.code,
		Dir:           l.dir,
		Flash:         flash,
		Error:         errMsg,
		Bypass:        s.Bypass(),
		CapsOff:       settings["caps_off"] == "1",
		PeriodStart:   jalali.Format(time.Unix(period.StartUTC, 0), "numeric", s.loc),
		PeriodEnd:     jalali.Format(time.Unix(period.EndUTC, 0), "numeric", s.loc),
		RawUsed:       in.RawUsed,
		GuestPool:     in.GuestPool,
		PoolGB:        settings["pool_charged_gb"],
		GuestPoolGB:   settings["guest_pool_gb"],
		GuestDeviceGB: settings["guest_device_gb"],
		Manual:        period.AllocMode == "manual",
		CycleLength:   30,
		CycleUnit:     "days",
	}
	if c, ok, err := s.db.Cycle(); err == nil && ok {
		d.CycleLength, d.CycleUnit = c.Length, c.Unit
	}
	if period.PoolCharged != nil {
		d.Pool = *period.PoolCharged
		d.PoolSet = true
		d.DataPoolGB = strconv.FormatFloat(float64(d.Pool)/1e9, 'f', -1, 64)
	} else {
		d.DataPoolGB = settings["pool_charged_gb"]
	}
	if period.GuestPool != nil {
		d.DataGuest = *period.GuestPool
	}
	if period.RawCeiling != nil {
		d.RawCeiling = *period.RawCeiling
		d.RawCeilingSet = true
	}

	persons, err := s.db.Persons()
	if err != nil {
		return nil, err
	}
	personName := map[int64]string{}
	personIdx := map[int64]int{}
	for _, p := range persons {
		subj := quota.PersonSubject(p.ID)
		st := dec.Subjects[subj]
		row := personRow{P: p, Status: st, Used: st.Used, Cap: st.Cap,
			Allocated: in.Allocations[p.ID], SubURL: s.subURL(r, p)}
		if p.CapOverride != nil {
			row.CapGB = fmt.Sprintf("%.2f", float64(*p.CapOverride)/1e9)
		}
		if st.Cap > 0 {
			row.Remaining = st.Cap - st.Used
			if row.Remaining < 0 {
				row.Remaining = 0
			}
			row.Pct = percent(st.Used, st.Cap)
		}
		personName[p.ID] = p.Name
		personIdx[p.ID] = len(d.Persons)
		d.Persons = append(d.Persons, row)
	}
	d.PersonCount = len(persons)

	// Cycle figures: person shares plus the guest share make the allocated
	// total; whatever is left of the pool is unallocated.
	d.CycleDaysLeft = int((period.EndUTC - now.Unix() + 86399) / 86400)
	var useBase, usedTotal int64
	for i := range d.Persons {
		p := &d.Persons[i]
		if d.Manual {
			d.PersonAllocated += p.Allocated
		} else if p.Cap > 0 {
			d.PersonAllocated += p.Cap
		}
		if p.Cap > 0 {
			if p.Used > p.Cap {
				useBase += p.Used
			} else {
				useBase += p.Cap
			}
		}
		usedTotal += p.Used
	}
	d.DataAllocated = d.PersonAllocated + d.DataGuest
	if d.PoolSet {
		d.DataUnallocated = d.Pool - d.DataAllocated
	}
	if d.Manual {
		base := d.DataAllocated
		if d.PoolSet && d.Pool > base {
			base = d.Pool
		}
		if base > 0 {
			sum := 0
			for i := range d.Persons {
				p := &d.Persons[i]
				p.DataAllocPct = percent(p.Allocated, base)
				sum += p.DataAllocPct
			}
			d.DataGuestAllocPct = percent(d.DataGuest, base)
			d.DataUnallocPct = 100 - sum - d.DataGuestAllocPct
			if d.DataUnallocPct < 0 {
				d.DataUnallocPct = 0
			}
		}
	}
	d.Allocated = d.PersonAllocated + d.GuestPool
	allocBase := d.Allocated
	if d.PoolSet && d.Pool > allocBase {
		allocBase = d.Pool
	}
	if allocBase > 0 {
		d.Unallocated = allocBase - d.Allocated
		sum := 0
		for i := range d.Persons {
			p := &d.Persons[i]
			if p.Cap > 0 {
				p.AllocPct = percent(p.Cap, allocBase)
				sum += p.AllocPct
			}
		}
		d.GuestAllocPct = percent(d.GuestPool, allocBase)
		// The rest segment takes the rounding remainder, so the bar always fills.
		d.UnallocPct = 100 - sum - d.GuestAllocPct
		if d.UnallocPct < 0 {
			d.UnallocPct = 0
		}
	}
	if useBase > 0 {
		d.Unused = useBase - usedTotal
		sum := 0
		for i := range d.Persons {
			p := &d.Persons[i]
			if p.Cap > 0 {
				p.UsedPct = percent(p.Used, useBase)
				sum += p.UsedPct
			}
		}
		d.UnusedPct = 100 - sum
	}

	devices, err := s.db.Devices()
	if err != nil {
		return nil, err
	}
	var guestUsed int64
	for _, dv := range devices {
		used := store.ChargedBytes(in.Totals[quota.DeviceSubject(dv.MAC)].Charged)
		if dv.Owner == nil {
			guestUsed += used
		}
		row := deviceRow{
			D:           dv,
			Used:        used,
			BlockReason: dec.Blocked[dv.MAC],
		}
		if dv.LastSeen > 0 {
			row.LastSeen = time.Unix(dv.LastSeen, 0).In(s.loc).Format("01-02 15:04")
		}
		if dv.LastUsed > 0 {
			row.LastUsed = time.Unix(dv.LastUsed, 0).In(s.loc).Format("01-02 15:04")
		}
		if row.BlockReason != "" {
			d.BlockedCount++
		}
		if dv.IP != "" {
			d.OnlineCount++
		}
		if dv.Owner != nil {
			row.OwnerID = *dv.Owner
			row.OwnerName = personName[*dv.Owner]
			if i, ok := personIdx[*dv.Owner]; ok {
				d.Persons[i].Devices++
				d.Persons[i].DeviceRows = append(d.Persons[i].DeviceRows, row)
			}
			if dv.IP != "" {
				d.Connected = append(d.Connected, row)
			}
		} else {
			row.GuestCap = in.GuestDeviceBytes
			if row.GuestCap > 0 {
				row.GuestPct = percent(used, row.GuestCap)
			}
			d.Guests = append(d.Guests, row)
		}
		d.Devices = append(d.Devices, row)
	}
	sort.SliceStable(d.Connected, func(i, j int) bool { return d.Connected[i].Used > d.Connected[j].Used })
	sort.SliceStable(d.Guests, func(i, j int) bool { return d.Guests[i].Used > d.Guests[j].Used })
	// Most recently active first: the device that just joined (first_seen) or
	// just moved bytes (last_used) is the one the admin is looking for.
	sort.SliceStable(d.Devices, func(i, j int) bool {
		return deviceActivity(d.Devices[i].D) > deviceActivity(d.Devices[j].D)
	})
	// Household charged includes guests; person rows already fold in devices.
	for _, t := range in.Totals {
		d.Charged += store.ChargedBytes(t.Charged)
	}

	// nft counts everything that crossed prerouting; the ledger only counts what
	// the proxy saw. A big gap means unaccounted traffic, so show it, never hide it.
	if nftBytes, derr := s.nft.DevBytes(); derr == nil {
		d.DriftAvailable = true
		for _, dv := range devices {
			if dv.IP == "" {
				continue
			}
			d.NftTotal += nftBytes[dv.IP]
			d.LedgerRaw += in.Totals[quota.DeviceSubject(dv.MAC)].Raw
		}
		if d.LedgerRaw > 0 {
			d.DriftPct = float64(d.NftTotal-d.LedgerRaw) / float64(d.LedgerRaw) * 100
		}
	}
	d.GuestUsed = guestUsed
	d.GuestRemaining = d.DataGuest - guestUsed
	if d.GuestRemaining < 0 {
		d.GuestRemaining = 0
	}
	if d.Manual {
		used := guestUsed
		for _, p := range d.Persons {
			used += p.Used
		}
		base := d.Pool
		if !d.PoolSet || used > base {
			base = used
		}
		if base > 0 {
			d.DataUnused = base - used
			sum := 0
			for i := range d.Persons {
				p := &d.Persons[i]
				p.DataUsedPct = percent(p.Used, base)
				sum += p.DataUsedPct
			}
			d.DataGuestUsedPct = percent(guestUsed, base)
			d.DataUnusedPct = 100 - sum - d.DataGuestUsedPct
			if d.DataUnusedPct < 0 {
				d.DataUnusedPct = 0
			}
		}
	}
	d.DeviceCount = len(devices)
	if d.PoolSet && d.Pool > 0 {
		d.PoolPct = percent(d.Charged, d.Pool)
		d.Over = d.Charged > d.Pool
	}

	if d.Routes, err = s.db.Routes(); err != nil {
		return nil, err
	}
	if d.Classes, err = s.db.Classes(); err != nil {
		return nil, err
	}
	rates, err := s.db.RateMap()
	if err != nil {
		return nil, err
	}
	for _, rt := range d.Routes {
		row := rateRow{Route: rt}
		for _, c := range d.Classes {
			w := 1.0
			if cw, ok := rates[rt.ID][c.ID]; ok {
				w = cw
			}
			row.Cells = append(row.Cells, rateCell{RouteID: rt.ID, ClassID: c.ID, ClassName: c.Name, Weight: w})
		}
		d.RateRows = append(d.RateRows, row)
	}

	detail, err := s.db.UsageDetail(period.ID)
	if err != nil {
		return nil, err
	}
	routeName := map[int64]string{}
	for _, rt := range d.Routes {
		routeName[rt.ID] = rt.Name
	}
	className := map[int64]string{}
	for _, c := range d.Classes {
		className[c.ID] = c.Name
	}
	for _, u := range detail {
		d.Usage = append(d.Usage, usageRow{
			Subject: u.Subject, Route: routeName[u.RouteID], Class: className[u.ClassID],
			Up: u.Up, Down: u.Down, Charged: store.ChargedBytes(u.Charged),
		})
	}

	events, err := s.db.Events(50)
	if err != nil {
		return nil, err
	}
	for _, e := range events {
		d.Events = append(d.Events, eventRow{
			When:    time.Unix(e.At, 0).In(s.loc).Format("01-02 15:04:05"),
			Kind:    e.Kind,
			Subject: e.Subject,
			Detail:  e.Detail,
		})
	}

	d.DomesticQuery = strings.TrimSpace(r.URL.Query().Get("dq"))
	dp, err := s.db.DomesticSites(d.DomesticQuery, int(atoiDefault(r.URL.Query().Get("dpage"), 1)), domesticPerPage)
	if err != nil {
		return nil, err
	}
	d.Domestic = dp.Sites
	d.DomesticTotal = dp.Total
	d.DomesticPage = dp.Page
	d.DomesticPages = dp.Pages
	d.DomesticPrev = dp.Page - 1
	d.DomesticNext = dp.Page + 1
	d.DomesticHasPrev = dp.Page > 1
	d.DomesticHasNext = dp.Page < dp.Pages
	if v, err := s.db.GetSetting("domestic_refreshed_at"); err == nil && v != "" {
		if ts, e := strconv.ParseInt(v, 10, 64); e == nil {
			t := time.Unix(ts, 0).In(s.loc)
			d.DomesticRefreshed = jalali.Format(t, "numeric", s.loc) + " " + t.Format("15:04")
		}
	}
	return d, nil
}

const langCookie = "useg_lang"

func adminLang(r *http.Request) lang {
	if v := r.URL.Query().Get("lang"); v != "" {
		return pickLang(v)
	}
	if c, err := r.Cookie(langCookie); err == nil {
		return pickLang(c.Value)
	}
	return lang{"fa", "rtl"}
}

// adminLang follows ?lang=, then the cookie, then Persian.
func setLangCookie(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("lang") == "" {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: langCookie, Value: adminLang(r).code, Path: "/",
		MaxAge: 365 * 24 * 3600, SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	setLangCookie(w, r)
	d, err := s.adminData(r, r.URL.Query().Get("ok"), r.URL.Query().Get("err"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "admin.gohtml", d)
}

func (s *Server) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/assets/", assetHandler())
	mux.HandleFunc("/admin/login", s.login)
	mux.HandleFunc("/admin/logout", s.logout)
	mux.HandleFunc("/admin", s.auth(s.admin))
	mux.HandleFunc("/admin/", s.auth(s.admin))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	})
	return mux
}

func (s *Server) PortalHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/assets/", assetHandler())
	mux.HandleFunc("/sub/", s.sub)
	mux.HandleFunc("/transfer", s.transfer)
	mux.HandleFunc("/", s.portal)
	return mux
}

func (s *Server) SetBypass(v bool) {
	s.mu.Lock()
	s.bypass = v
	s.mu.Unlock()
}

func (s *Server) Bypass() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bypass
}

func (s *Server) admin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if r.URL.Path == "/admin/api/summary" {
			s.apiSummary(w, r)
			return
		}
		s.dashboard(w, r)
		return
	}
	_ = r.ParseForm()
	var err error
	switch r.URL.Path {
	case "/admin/person/new":
		err = s.postNewPerson(r)
	case "/admin/device":
		err = s.postDevice(r)
	case "/admin/route":
		err = s.postRoute(r)
	case "/admin/route/delete":
		err = s.postRouteDelete(r)
	case "/admin/data/settings":
		err = s.postDataSettings(r)
	case "/admin/data/reset":
		err = s.postDataReset(r)
	case "/admin/data/add":
		err = s.postDataAdd(r)
	case "/admin/data/sync":
		err = s.postDataSync(r)
	case "/admin/data/remove":
		err = s.postDataRemove(r)
	case "/admin/data/allocate-equal":
		err = s.postDataAllocate(r, false)
	case "/admin/data/allocate-weight":
		err = s.postDataAllocate(r, true)
	case "/admin/data/allocate":
		err = s.postDataAllocateRow(r)
	case "/admin/data/unallocate":
		err = s.postDataUnallocateRow(r)
	case "/admin/bypass":
		err = s.postBypass(r)
	case "/admin/caps":
		err = s.postCaps(r)
	case "/admin/domestic/refresh":
		err = s.postDomesticRefresh(r)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.log.Error("admin action", "path", r.URL.Path, "err", err)
		http.Redirect(w, r, "/admin?err="+urlQuery(err.Error()), http.StatusSeeOther)
		return
	}
	flash := "saved"
	switch r.URL.Path {
	case "/admin/domestic/refresh":
		flash = "domestic.refreshed"
	case "/admin/data/reset":
		flash = "data.reset_ok"
	case "/admin/data/add":
		flash = "data.added_ok"
	case "/admin/data/sync":
		flash = "data.synced_ok"
	case "/admin/data/remove":
		flash = "data.removed_ok"
	case "/admin/data/allocate-equal", "/admin/data/allocate-weight":
		flash = "data.allocated_ok"
	case "/admin/data/allocate":
		flash = "data.allocated_ok"
	case "/admin/data/unallocate":
		flash = "data.unallocated_ok"
	case "/admin/device":
		switch r.FormValue("action") {
		case "delete":
			flash = "devices.deleted_ok"
		case "label":
			flash = "devices.renamed_ok"
		}
	}
	http.Redirect(w, r, "/admin?ok="+urlQuery(flash), http.StatusSeeOther)
}

func urlQuery(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), " ", "+")
}

func (s *Server) postNewPerson(r *http.Request) error {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		return fmt.Errorf("name required")
	}
	weight := floatDefault(r.FormValue("weight"), 0.2)
	id, err := s.db.CreatePerson(name, weight, "fa")
	if err != nil {
		return err
	}
	if err := s.ensureCredentials(id); err != nil {
		return err
	}
	return s.db.AddEvent("person_new", "person:"+strconv.FormatInt(id, 10), name)
}

// ensureCredentials gives a person one listener per enabled route, which is
// what makes the subscription work without any further setup.
func (s *Server) ensureCredentials(personID int64) error {
	routes, err := s.db.Routes()
	if err != nil {
		return err
	}
	for _, rt := range routes {
		if !rt.Enabled || !rt.AllowAll {
			continue
		}
		if _, err := s.db.EnsureCredential(personID, rt.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) postDataReset(r *http.Request) error {
	gb := floatDefault(r.FormValue("gb"), 0)
	if gb <= 0 {
		return fmt.Errorf("data GB must be positive")
	}
	length := int(atoiDefault(r.FormValue("length"), 30))
	if length <= 0 {
		return fmt.Errorf("cycle length must be positive")
	}
	period, err := s.db.StartCycle(time.Now(), length, r.FormValue("unit"), int64(gb*1e9), s.loc)
	if err != nil {
		return err
	}
	return s.db.AddEvent("data_reset", "period:"+strconv.FormatInt(period.ID, 10),
		fmt.Sprintf("%d %s, %.2f GB", length, r.FormValue("unit"), gb))
}

func (s *Server) postDataAdd(r *http.Request) error {
	gb := floatDefault(r.FormValue("gb"), 0)
	if gb <= 0 {
		return fmt.Errorf("data GB must be positive")
	}
	period, err := s.db.EnsurePeriod(time.Now(), s.loc)
	if err != nil {
		return err
	}
	if period.AllocMode != "manual" {
		return fmt.Errorf("start a data cycle first")
	}
	total, err := s.db.AddPool(period.ID, int64(gb*1e9))
	if err != nil {
		return err
	}
	return s.db.AddEvent("data_add", "period:"+strconv.FormatInt(period.ID, 10),
		fmt.Sprintf("+%.2f GB, total %.2f GB", gb, float64(total)/1e9))
}

// postDataSync reconciles the ledger with the ISP's page. The difference
// between the ISP's remaining data and our remaining pool is added to the
// unallocated pool; a lower ISP figure never takes data or usage away.
func (s *Server) postDataSync(r *http.Request) error {
	remaining, err := formGB(r, "remaining_gb")
	if err != nil {
		return err
	}
	if remaining == nil || *remaining < 0 {
		return fmt.Errorf("ISP remaining GB is required")
	}
	period, err := s.db.EnsurePeriod(time.Now(), s.loc)
	if err != nil {
		return err
	}
	if period.AllocMode != "manual" {
		return fmt.Errorf("start a data cycle first")
	}
	in, err := quota.Gather(s.db, period)
	if err != nil {
		return err
	}
	s.withPending(&in)
	var used int64
	for _, t := range in.Totals {
		used += store.ChargedBytes(t.Charged)
	}
	var pool int64
	if period.PoolCharged != nil {
		pool = *period.PoolCharged
	}
	left := pool - used
	if left < 0 {
		left = 0
	}
	diff := *remaining - left
	if diff <= 0 {
		return fmt.Errorf("ISP remaining does not exceed the ledger; nothing to add")
	}
	total, err := s.db.AddPool(period.ID, diff)
	if err != nil {
		return err
	}
	return s.db.AddEvent("data_sync", "period:"+strconv.FormatInt(period.ID, 10),
		fmt.Sprintf("ISP %.2f GB, +%.2f GB, total %.2f GB",
			float64(*remaining)/1e9, float64(diff)/1e9, float64(total)/1e9))
}

// postDataRemove takes data back out of the unallocated pool. Allocated shares
// are untouchable, so it clamps to what is still unallocated.
func (s *Server) postDataRemove(r *http.Request) error {
	gb := floatDefault(r.FormValue("gb"), 0)
	if gb <= 0 {
		return fmt.Errorf("data GB must be positive")
	}
	period, err := s.db.EnsurePeriod(time.Now(), s.loc)
	if err != nil {
		return err
	}
	if period.AllocMode != "manual" {
		return fmt.Errorf("start a data cycle first")
	}
	var pool int64
	if period.PoolCharged != nil {
		pool = *period.PoolCharged
	}
	var allocated int64
	if period.GuestPool != nil {
		allocated = *period.GuestPool
	}
	allocs, err := s.db.Allocations(period.ID)
	if err != nil {
		return err
	}
	for _, b := range allocs {
		allocated += b
	}
	unallocated := pool - allocated
	if unallocated <= 0 {
		return fmt.Errorf("nothing unallocated to remove")
	}
	b := int64(gb * 1e9)
	if b > unallocated {
		b = unallocated
	}
	total, err := s.db.ReducePool(period.ID, b)
	if err != nil {
		return err
	}
	return s.db.AddEvent("data_remove", "period:"+strconv.FormatInt(period.ID, 10),
		fmt.Sprintf("-%.2f GB, total %.2f GB", float64(b)/1e9, float64(total)/1e9))
}

// postDataAllocate tops up the unallocated remainder. Equal is the same split
// with every target weighing 1; the guest pool is one of the targets.
func (s *Server) postDataAllocate(r *http.Request, byWeight bool) error {
	period, err := s.db.EnsurePeriod(time.Now(), s.loc)
	if err != nil {
		return err
	}
	if period.AllocMode != "manual" {
		return fmt.Errorf("start a data cycle first")
	}
	var pool, guest int64
	if period.PoolCharged != nil {
		pool = *period.PoolCharged
	}
	if period.GuestPool != nil {
		guest = *period.GuestPool
	}
	allocs, err := s.db.Allocations(period.ID)
	if err != nil {
		return err
	}
	allocated := guest
	for _, b := range allocs {
		allocated += b
	}
	unallocated := pool - allocated
	if unallocated <= 0 {
		return fmt.Errorf("nothing unallocated to allocate")
	}

	persons, err := s.db.Persons()
	if err != nil {
		return err
	}
	targets := make([]int64, 0, len(persons)+1) // 0 is the guest pool
	weights := make([]float64, 0, len(persons)+1)
	for _, p := range persons {
		if !p.Enabled {
			continue
		}
		w := 1.0
		if byWeight {
			w = floatDefault(r.FormValue("weight_"+strconv.FormatInt(p.ID, 10)), 0)
			if w < 0 {
				return fmt.Errorf("weights cannot be negative")
			}
		}
		targets = append(targets, p.ID)
		weights = append(weights, w)
	}
	w := 1.0
	if byWeight {
		w = floatDefault(r.FormValue("weight_guest"), 0)
		if w < 0 {
			return fmt.Errorf("weights cannot be negative")
		}
	}
	targets = append(targets, 0)
	weights = append(weights, w)
	var sum float64
	for _, w := range weights {
		sum += w
	}
	if sum <= 0 {
		return fmt.Errorf("set a weight for at least one person")
	}

	shares := quota.Split(unallocated, weights)
	personShares := map[int64]int64{}
	var guestShare int64
	for i, id := range targets {
		if id == 0 {
			guestShare = shares[i]
		} else if shares[i] > 0 {
			personShares[id] = shares[i]
		}
	}
	if err := s.db.ApplyAllocation(period.ID, personShares, guestShare); err != nil {
		return err
	}
	mode := "equal"
	if byWeight {
		mode = "weighted"
	}
	return s.db.AddEvent("data_allocate", "period:"+strconv.FormatInt(period.ID, 10),
		fmt.Sprintf("%s %.2f GB", mode, float64(unallocated)/1e9))
}

func (s *Server) postDevice(r *http.Request) error {
	mac := r.FormValue("mac")
	action := r.FormValue("action")
	switch action {
	case "label":
		label := strings.TrimSpace(r.FormValue("label"))
		if err := s.db.SetDeviceLabel(mac, label); err != nil {
			return err
		}
		return s.db.AddEvent("device_label", "dev:"+mac, label)
	case "bind":
		pid, _ := strconv.ParseInt(r.FormValue("person"), 10, 64)
		if pid == 0 {
			return s.db.Unbind(mac)
		}
		if err := s.db.Bind(mac, pid, "admin", time.Now().Unix()); err != nil {
			return err
		}
		return s.db.AddEvent("bind", "dev:"+mac, "by admin to person:"+strconv.FormatInt(pid, 10))
	case "unbind":
		if err := s.db.Unbind(mac); err != nil {
			return err
		}
		return s.db.AddEvent("unbind", "dev:"+mac, "by admin")
	case "block":
		reason := "blocked by admin"
		if err := s.db.SetBlock("dev:"+mac, nil, reason, "admin"); err != nil {
			return err
		}
		return s.db.AddEvent("block", "dev:"+mac, "by admin")
	case "unblock":
		if err := s.db.ClearBlock("dev:" + mac); err != nil {
			return err
		}
		return s.db.AddEvent("unblock", "dev:"+mac, "by admin")
	case "delete":
		if err := s.db.DeleteDevice(mac); err != nil {
			return err
		}
		return s.db.AddEvent("device_delete", "dev:"+mac, "by admin")
	}
	return fmt.Errorf("unknown action %q", action)
}

func (s *Server) postRoute(r *http.Request) error {
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	upstream := strings.TrimSpace(r.FormValue("upstream"))
	if upstream == "" {
		upstream = "{}"
	}
	if !json.Valid([]byte(upstream)) {
		return fmt.Errorf("upstream json is not valid JSON")
	}
	rt := store.Route{
		ID:           id,
		Name:         strings.TrimSpace(r.FormValue("name")),
		Kind:         r.FormValue("kind"),
		UpstreamJSON: upstream,
		AllowAll:     r.FormValue("allow_all") == "1",
		Enabled:      r.FormValue("enabled") == "1",
	}
	if rt.Name == "" {
		return fmt.Errorf("route name required")
	}
	saved, err := s.db.SaveRoute(rt)
	if err != nil {
		return err
	}
	if err := s.saveRates(r, saved); err != nil {
		return err
	}
	if rt.AllowAll && rt.Enabled {
		persons, err := s.db.Persons()
		if err != nil {
			return err
		}
		for _, p := range persons {
			if _, err := s.db.EnsureCredential(p.ID, saved); err != nil {
				return err
			}
		}
	}
	return s.db.AddEvent("route_save", rt.Name, "kind="+rt.Kind)
}

// saveRates writes the w_<route>_<class> cells of a route form. A brand-new
// route posts w_0_<class> and the values are applied to the saved id.
func (s *Server) saveRates(r *http.Request, routeID int64) error {
	for k, vs := range r.Form {
		if !strings.HasPrefix(k, "w_") || len(vs) == 0 {
			continue
		}
		parts := strings.Split(k[2:], "_")
		if len(parts) != 2 {
			continue
		}
		rid, err1 := strconv.ParseInt(parts[0], 10, 64)
		cid, err2 := strconv.ParseInt(parts[1], 10, 64)
		if err1 != nil || err2 != nil || (rid != 0 && rid != routeID) {
			continue
		}
		if err := s.db.SetRate(routeID, cid, floatDefault(vs[0], 1)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) postRouteDelete(r *http.Request) error {
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err := s.db.DeleteRoute(id); err != nil {
		return err
	}
	return s.db.AddEvent("route_delete", strconv.FormatInt(id, 10), "")
}

// postDataSettings stores the flat data settings: the guest device cap in any
// mode, and in monthly mode the pool and guest share. The raw ceiling always
// mirrors the pool, so it is written with it.
func (s *Server) postDataSettings(r *http.Request) error {
	pool, err := formGB(r, "pool_charged_gb")
	if err != nil {
		return err
	}
	guest, err := formGB(r, "guest_pool_gb")
	if err != nil {
		return err
	}
	if _, err := formGB(r, "guest_device_gb"); err != nil {
		return err
	}
	for _, k := range []string{"pool_charged_gb", "guest_pool_gb", "guest_device_gb"} {
		if !r.Form.Has(k) {
			continue
		}
		if err := s.db.SetSetting(k, strings.TrimSpace(r.FormValue(k))); err != nil {
			return err
		}
	}
	period, err := s.db.EnsurePeriod(time.Now(), s.loc)
	if err != nil {
		return err
	}
	subj := "period:" + strconv.FormatInt(period.ID, 10)
	if period.AllocMode == "manual" {
		// The cycle's pool and guest share belong to the cycle forms.
		return s.db.AddEvent("data_settings", subj, "guest device updated")
	}
	if _, err := s.db.Exec(`UPDATE period SET pool_charged = ?, guest_pool = ?, raw_ceiling = ? WHERE id = ?`,
		pool, guest, pool, period.ID); err != nil {
		return err
	}
	return s.db.AddEvent("data_settings", subj, "pool updated")
}

// postDataAllocateRow gives one person (or the guest share) GB from the
// unallocated pool, without touching the other allocations.
func (s *Server) postDataAllocateRow(r *http.Request) error {
	period, err := s.db.EnsurePeriod(time.Now(), s.loc)
	if err != nil {
		return err
	}
	if period.AllocMode != "manual" {
		return fmt.Errorf("start a data cycle first")
	}
	pid, guest, b, err := dataAdjust(r)
	if err != nil {
		return err
	}
	if !guest {
		persons, err := s.db.Persons()
		if err != nil {
			return err
		}
		known := false
		for _, p := range persons {
			if p.ID == pid {
				known = true
			}
		}
		if !known {
			return fmt.Errorf("unknown person")
		}
	}
	allocated := int64(0)
	if period.GuestPool != nil {
		allocated = *period.GuestPool
	}
	allocs, err := s.db.Allocations(period.ID)
	if err != nil {
		return err
	}
	for _, x := range allocs {
		allocated += x
	}
	if period.PoolCharged != nil {
		headroom := *period.PoolCharged - allocated
		if headroom <= 0 {
			return fmt.Errorf("nothing unallocated to allocate")
		}
		if b > headroom {
			b = headroom
		}
	}
	if guest {
		err = s.db.ApplyAllocation(period.ID, nil, b)
	} else {
		err = s.db.ApplyAllocation(period.ID, map[int64]int64{pid: b}, 0)
	}
	if err != nil {
		return err
	}
	return s.db.AddEvent("data_allocate", dataSubject(pid, guest),
		fmt.Sprintf("+%.2f GB", float64(b)/1e9))
}

// postDataUnallocateRow returns a person's (or the guests') unused data to the
// unallocated pool, never taking back data already consumed.
func (s *Server) postDataUnallocateRow(r *http.Request) error {
	period, err := s.db.EnsurePeriod(time.Now(), s.loc)
	if err != nil {
		return err
	}
	if period.AllocMode != "manual" {
		return fmt.Errorf("start a data cycle first")
	}
	pid, guest, b, err := dataAdjust(r)
	if err != nil {
		return err
	}
	in, err := quota.Gather(s.db, period)
	if err != nil {
		return err
	}
	s.withPending(&in)
	var alloc, used int64
	if guest {
		if period.GuestPool != nil {
			alloc = *period.GuestPool
		}
		for _, d := range in.Devices {
			if d.Owner == nil {
				used += store.ChargedBytes(in.Totals[quota.DeviceSubject(d.MAC)].Charged)
			}
		}
	} else {
		allocs, err := s.db.Allocations(period.ID)
		if err != nil {
			return err
		}
		alloc = allocs[pid]
		used = store.ChargedBytes(in.Totals[quota.PersonSubject(pid)].Charged)
		for _, d := range in.Devices {
			if d.Owner != nil && *d.Owner == pid {
				used += store.ChargedBytes(in.Totals[quota.DeviceSubject(d.MAC)].Charged)
			}
		}
	}
	unused := alloc - used
	if unused <= 0 {
		return fmt.Errorf("nothing unused to unallocate")
	}
	if b > unused {
		b = unused
	}
	if err := s.db.ReduceAllocation(period.ID, pid, b); err != nil {
		return err
	}
	return s.db.AddEvent("data_unallocate", dataSubject(pid, guest),
		fmt.Sprintf("-%.2f GB", float64(b)/1e9))
}

// dataAdjust reads the clicked row's person (or "guest") and its GB field.
func dataAdjust(r *http.Request) (int64, bool, int64, error) {
	v := r.FormValue("person")
	guest := v == "guest"
	key := "gb_guest"
	var pid int64
	if !guest {
		var err error
		pid, err = strconv.ParseInt(v, 10, 64)
		if err != nil || pid <= 0 {
			return 0, false, 0, fmt.Errorf("choose a person")
		}
		key = "gb_" + strconv.FormatInt(pid, 10)
	}
	b, err := formGB(r, key)
	if err != nil {
		return 0, false, 0, err
	}
	if b == nil || *b <= 0 {
		return 0, false, 0, fmt.Errorf("enter a GB amount")
	}
	return pid, guest, *b, nil
}

func dataSubject(pid int64, guest bool) string {
	if guest {
		return "guest"
	}
	return quota.PersonSubject(pid)
}

// formGB reads a GB field; an empty field means "unset".
func formGB(r *http.Request, k string) (*int64, error) {
	v := strings.TrimSpace(r.FormValue(k))
	if v == "" {
		return nil, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", k, err)
	}
	b := int64(f * 1e9)
	return &b, nil
}

func (s *Server) postBypass(r *http.Request) error {
	on := r.FormValue("on") == "1"
	if err := s.nft.Bypass(on); err != nil {
		return err
	}
	s.mu.Lock()
	s.bypass = on
	s.mu.Unlock()
	state, label := "0", "off"
	if on {
		state, label = "1", "on"
	}
	if err := s.db.SetSetting("bypass", state); err != nil {
		return err
	}
	return s.db.AddEvent("bypass", "gateway", "bypass "+label)
}

func (s *Server) postCaps(r *http.Request) error {
	off := r.FormValue("off") == "1"
	state, label := "0", "caps on"
	if off {
		state, label = "1", "caps off"
	}
	if err := s.db.SetSetting("caps_off", state); err != nil {
		return err
	}
	return s.db.AddEvent("caps", "quota", label)
}

func (s *Server) postDomesticRefresh(r *http.Request) error {
	s.mu.Lock()
	if s.refreshing {
		s.mu.Unlock()
		return fmt.Errorf("a refresh is already running")
	}
	s.refreshing = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.refreshing = false
		s.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	sites, err := s.fetchDomestic(ctx)
	if err != nil {
		return err
	}
	n, err := s.db.ReplaceDomestic(sites)
	if err != nil {
		return err
	}
	if err := s.db.SetSetting("domestic_refreshed_at", strconv.FormatInt(time.Now().Unix(), 10)); err != nil {
		return err
	}
	return s.db.AddEvent("domestic_refresh", "ito", fmt.Sprintf("%d rows", n))
}

func (s *Server) apiSummary(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	period, err := s.db.EnsurePeriod(now, s.loc)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	in, err := quota.Gather(s.db, period)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.withPending(&in)
	dec := quota.Compute(in)
	capsOff, _ := s.db.GetSetting("caps_off")
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(map[string]any{
		"period": map[string]any{
			"id":    period.ID,
			"start": time.Unix(period.StartUTC, 0).UTC(),
			"end":   time.Unix(period.EndUTC, 0).UTC(),
			"jalali": map[string]string{
				"start": jalali.Format(time.Unix(period.StartUTC, 0), "long", s.loc),
				"end":   jalali.Format(time.Unix(period.EndUTC, 0), "long", s.loc),
			},
			"pool_charged": period.PoolCharged,
			"guest_pool":   period.GuestPool,
			"raw_ceiling":  period.RawCeiling,
			"raw_used":     in.RawUsed,
		},
		"subjects": dec.Subjects,
		"blocked":  dec.Blocked,
		"bypass":   s.Bypass(),
		"caps_off": capsOff == "1",
	})
}
