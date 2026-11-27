package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"useg/internal/config"
	"useg/internal/domestic"
	"useg/internal/enforce"
	"useg/internal/jalali"
	"useg/internal/quota"
	"useg/internal/store"
)

//go:embed templates/*.gohtml
var tmplFS embed.FS

//go:embed assets/*
var assetFS embed.FS

func assetHandler() http.Handler {
	h := http.FileServerFS(assetFS)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Assets ship inside the binary and are served from the same machine;
		// never let the browser pin a stale copy across a deploy.
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

type Server struct {
	db   *store.DB
	cfg  *config.Config
	log  *slog.Logger
	nft  *enforce.Nft
	loc  *time.Location
	tmpl *template.Template

	mu         sync.Mutex
	transferMu sync.Mutex // one transfer at a time: check and move must not race
	sessions   map[string]time.Time
	bypass     bool
	pending    func() map[string]store.Totals

	fetchDomestic func(context.Context) ([]store.DomesticSite, error)
	refreshing    bool
}

func New(db *store.DB, cfg *config.Config, nft *enforce.Nft, log *slog.Logger) (*Server, error) {
	tmpl, err := template.New("").Funcs(funcs(cfg.Location())).ParseFS(tmplFS, "templates/*.gohtml")
	if err != nil {
		return nil, err
	}
	return &Server{
		db: db, cfg: cfg, log: log, nft: nft,
		loc:           cfg.Location(),
		tmpl:          tmpl,
		sessions:      map[string]time.Time{},
		fetchDomestic: domestic.Fetcher{}.Fetch,
	}, nil
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	// Buffer first: a template error must be a clean 500, not a half-written
	// page the browser waits on forever.
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		s.log.Error("render", "tmpl", name, "err", err)
		http.Error(w, "render error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// SetPending lets the UI include the accountant's unflushed buckets, so the
// numbers on screen match what the quota engine is about to enforce.
func (s *Server) SetPending(fn func() map[string]store.Totals) { s.pending = fn }

func (s *Server) withPending(in *quota.Input) {
	if s.pending == nil {
		return
	}
	quota.MergePending(in, s.pending())
}

// ---- portal -----------------------------------------------------------------

type portalDevice struct {
	Name    string
	Used    int64
	Current bool
	Blocked bool
}

type portalPerson struct {
	ID   int64
	Name string
	Used int64
	You  bool
}

type dailyRow struct {
	Label string
	Bytes int64
	Today bool
}

type portalPage struct {
	Lang         string
	Dir          string
	Known        bool
	DeviceName   string
	PersonName   string
	Used         int64
	Cap          int64
	Remaining    int64
	Unlimited    bool
	Pct          int
	Over         bool
	Blocked      bool
	Reason       string
	DaysLeft     int
	Devices      []portalDevice
	People       []portalPerson
	DailyUsed    int64
	DailyCap     int64
	HasDaily     bool
	DailyLimitGB string
	DailyHistory []dailyRow
	PeriodEnd    string
	Transferable bool
	Notice       string
}

func reasonText(l, reason string) string {
	key := map[string]string{
		quota.ReasonQuota:     "reason.quota",
		quota.ReasonDaily:     "reason.daily",
		quota.ReasonGuest:     "reason.guest",
		quota.ReasonGuestPool: "reason.guest_pool",
		quota.ReasonHousehold: "reason.household",
		quota.ReasonManual:    "reason.manual",
	}[reason]
	if key == "" {
		return reason
	}
	return T(l, key)
}

// deviceLabel is what a human sees: the admin's label, then the mDNS-advertised
// name, then the DHCP hostname with its separators opened up, then the MAC.
func deviceLabel(d store.Device) string {
	if n := strings.TrimSpace(d.Label); n != "" {
		return n
	}
	if n := strings.TrimSpace(d.Name); n != "" {
		return n
	}
	if h := hostLabel(d.Hostname); h != "" {
		return h
	}
	return d.MAC
}

func hostLabel(host string) string {
	h := strings.TrimSuffix(strings.TrimSpace(host), ".local")
	h = strings.NewReplacer("-", " ", "_", " ").Replace(h)
	return strings.Join(strings.Fields(h), " ")
}

// The connectivity checks the common OSes fire on join. A 302 to the portal is
// what makes them show the "sign in to network" sheet instead of "online".
var captiveProbe = map[string]bool{
	"/hotspot-detect.html": true, // Apple
	"/generate_204":        true, // Android
	"/gen_204":             true, // Android, older
	"/connecttest.txt":     true, // Windows 10+
	"/ncsi.txt":            true, // Windows, older
	"/success.txt":         true, // Firefox
}

func (s *Server) portal(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		if captiveProbe[r.URL.Path] {
			http.Redirect(w, r, "http://"+s.cfg.BoxIP+"/", http.StatusFound)
			return
		}
		http.NotFound(w, r)
		return
	}
	l := lang{"fa", "rtl"}
	now := time.Now()
	period, err := s.db.EnsurePeriod(now, s.loc)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	page := portalPage{
		Lang: l.code, Dir: l.dir,
		PeriodEnd: jalali.Format(time.Unix(period.EndUTC, 0), "long", s.loc),
	}
	ip := clientIP(r)
	dev, err := s.db.DeviceByIP(ip)
	if err == nil {
		in, gerr := quota.Gather(s.db, period, s.loc)
		if gerr != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		s.withPending(&in)
		dec := quota.Compute(in)
		page.Known = true
		page.DeviceName = deviceLabel(dev)
		subj := quota.DeviceSubject(dev.MAC)
		if dev.Owner != nil {
			subj = quota.PersonSubject(*dev.Owner)
			if p, perr := s.db.Person(*dev.Owner); perr == nil {
				page.PersonName = p.Name
			}
		}
		if st, ok := dec.Subjects[subj]; ok {
			page.Used = st.Used
			page.Cap = st.Cap
			page.DailyUsed = st.DailyUsed
			page.DailyCap = st.DailyCap
			page.HasDaily = st.DailyCap > 0
			if page.HasDaily {
				page.DailyLimitGB = strconv.FormatFloat(float64(st.DailyCap)/1e9, 'f', -1, 64)
			}
			page.Unlimited = st.Cap < 0
			if !page.Unlimited {
				page.Remaining = st.Cap - st.Used
				if page.Remaining < 0 {
					page.Remaining = 0
				}
				page.Pct = percent(st.Used, st.Cap)
				page.Over = st.Used > st.Cap
			}
		}
		if reason := dec.Blocked[dev.MAC]; reason != "" {
			page.Blocked = true
			page.Reason = reasonText(l.code, reason)
		}
		// A person sees every device on their share; a guest sees only the one
		// they are on. Usage comes from the same gathered totals as the quota.
		for _, d := range in.Devices {
			if dev.Owner != nil {
				if d.Owner == nil || *d.Owner != *dev.Owner {
					continue
				}
			} else if d.MAC != dev.MAC {
				continue
			}
			page.Devices = append(page.Devices, portalDevice{
				Name:    deviceLabel(d),
				Used:    store.ChargedBytes(in.Totals[quota.DeviceSubject(d.MAC)].Charged),
				Current: d.MAC == dev.MAC,
				Blocked: dec.Blocked[d.MAC] != "",
			})
		}
		sort.SliceStable(page.Devices, func(i, j int) bool {
			return page.Devices[i].Used > page.Devices[j].Used
		})
		// Household members only see the group's usage once their own device is
		// bound to someone; a guest stays on their own numbers.
		if dev.Owner != nil {
			for _, p := range in.Persons {
				st, ok := dec.Subjects[quota.PersonSubject(p.ID)]
				if !ok {
					continue
				}
				page.People = append(page.People, portalPerson{
					ID:   p.ID,
					Name: p.Name,
					Used: st.Used,
					You:  p.ID == *dev.Owner,
				})
			}
			sort.SliceStable(page.People, func(i, j int) bool {
				return page.People[i].Used > page.People[j].Used
			})
			// Only a metered person with something left has data to give away.
			page.Transferable = page.PersonName != "" && !page.Unlimited && page.Remaining > 0
			// A daily cap is personal, so only a bound person sees or sets it.
			page.DailyHistory = s.dailyHistory(in, *dev.Owner, page.DailyUsed, l.code)
		}
	}
	if r.URL.Query().Get("ok") == "transfer" {
		page.Notice = T(l.code, "transfer.ok")
	}
	if r.URL.Query().Get("ok") == "daily" {
		page.Notice = T(l.code, "portal.daily_saved")
	}
	// Whole days left, rounded up: the last partial day still counts as today.
	if left := period.EndUTC - now.Unix(); left > 0 {
		page.DaysLeft = int((left + 86399) / 86400)
	}
	s.render(w, "portal.gohtml", page)
}

// dailyHistory is the person's last seven local days, oldest first. Today is
// overridden with the live counter so the list agrees with the engine.
func (s *Server) dailyHistory(in quota.Input, personID, today int64, lang string) []dailyRow {
	subs := map[string]bool{quota.PersonSubject(personID): true}
	for _, d := range in.Devices {
		if d.Owner != nil && *d.Owner == personID {
			subs[quota.DeviceSubject(d.MAC)] = true
		}
	}
	now := time.Now().In(s.loc)
	days := make([]time.Time, 0, 7)
	for i := 6; i >= 0; i-- {
		days = append(days, now.AddDate(0, 0, -i))
	}
	byDay := map[string]float64{}
	if rows, err := s.db.DailyUsageSince(days[0].Format("2006-01-02")); err == nil {
		for _, r := range rows {
			if subs[r.Subject] {
				byDay[r.Day] += r.Charged
			}
		}
	}
	out := make([]dailyRow, 0, len(days))
	for i, d := range days {
		label := jalali.Format(d, "month-day", s.loc)
		if lang == "fa" {
			label = persianDigits(label)
		}
		used := store.ChargedBytes(byDay[d.Format("2006-01-02")])
		if i == len(days)-1 {
			used = today
		}
		out = append(out, dailyRow{Label: label, Bytes: used, Today: i == len(days)-1})
	}
	return out
}

// dailyLimit lets a bound person set or clear their own daily cap. Guests have
// no person, so they have no daily limit to set.
func (s *Server) dailyLimit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if !sameOrigin(r) {
		http.Error(w, "cross-origin daily limit", http.StatusForbidden)
		return
	}
	dev, err := s.db.DeviceByIP(clientIP(r))
	if err != nil || dev.Owner == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	person, err := s.db.Person(*dev.Owner)
	if err != nil || !person.Enabled {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	b, err := formGB(r, "gb")
	switch {
	case err != nil:
		// A malformed field changes nothing and reports no false success.
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	case b == nil:
		// An empty field clears the cap.
		err = s.db.SetDailyLimit(person.ID, nil)
		if err == nil {
			_ = s.db.AddEvent("daily_limit", quota.PersonSubject(person.ID), "cleared")
		}
	case *b <= 0:
		// Zero or negative is not a cap; keep the old one.
	default:
		err = s.db.SetDailyLimit(person.ID, b)
		if err == nil {
			_ = s.db.AddEvent("daily_limit", quota.PersonSubject(person.ID),
				fmt.Sprintf("%.2f GB", float64(*b)/1e9))
		}
	}
	if err != nil {
		s.log.Error("set daily limit", "person", person.ID, "err", err)
	}
	http.Redirect(w, r, "/?ok=daily", http.StatusSeeOther)
}

type transferPage struct {
	Lang      string
	Dir       string
	FromName  string
	ToName    string
	ToID      int64
	Remaining int64
	Unlimited bool
	Error     string
}

func remainingOf(st quota.Status) int64 {
	if st.Cap < 0 {
		return 0
	}
	if left := st.Cap - st.Used; left > 0 {
		return left
	}
	return 0
}

// personStatus is the same number the portal and the quota engine see, with the
// accountant's unflushed buckets folded in.
func (s *Server) personStatus(period *store.Period, personID int64) (quota.Status, error) {
	in, err := quota.Gather(s.db, period, s.loc)
	if err != nil {
		return quota.Status{}, err
	}
	s.withPending(&in)
	return quota.Compute(in).Subjects[quota.PersonSubject(personID)], nil
}

// sameOrigin blocks a form posted by another site. The portal has no session
// cookie, so the browser's Origin header is the only cross-site signal.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	return err == nil && u.Host == r.Host
}

// transfer moves data from the calling device's person to another person for
// the current period.
func (s *Server) transfer(w http.ResponseWriter, r *http.Request) {
	l := lang{"fa", "rtl"}
	now := time.Now()
	period, err := s.db.EnsurePeriod(now, s.loc)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	dev, err := s.db.DeviceByIP(clientIP(r))
	if err != nil || dev.Owner == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	from, err := s.db.Person(*dev.Owner)
	if err != nil || !from.Enabled {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	toID, _ := strconv.ParseInt(r.FormValue("to"), 10, 64)
	to, err := s.db.Person(toID)
	if err != nil || !to.Enabled || to.ID == from.ID {
		http.Error(w, "unknown transfer target", http.StatusBadRequest)
		return
	}

	page := transferPage{Lang: l.code, Dir: l.dir, FromName: from.Name, ToName: to.Name, ToID: to.ID}

	if r.Method == http.MethodPost {
		if !sameOrigin(r) {
			http.Error(w, "cross-origin transfer", http.StatusForbidden)
			return
		}
		s.transferMu.Lock()
		defer s.transferMu.Unlock()
		st, err := s.personStatus(period, from.ID)
		if err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		page.Remaining = remainingOf(st)
		amount, ferr := formGB(r, "gb")
		switch {
		case ferr != nil || amount == nil || *amount <= 0:
			page.Error = T(l.code, "transfer.err_amount")
		case st.Cap < 0 || *amount > page.Remaining:
			page.Error = T(l.code, "transfer.err_insufficient")
		default:
			if err := s.db.Transfer(period.ID, from.ID, to.ID, *amount, period.AllocMode == "manual"); err != nil {
				s.log.Error("transfer", "from", from.ID, "to", to.ID, "err", err)
				page.Error = T(l.code, "transfer.err_failed")
				break
			}
			_ = s.db.AddEvent("transfer", quota.PersonSubject(from.ID),
				fmt.Sprintf("to person:%d %.2f GB", to.ID, float64(*amount)/1e9))
			http.Redirect(w, r, "/?ok=transfer", http.StatusSeeOther)
			return
		}
		s.render(w, "transfer.gohtml", page)
		return
	}

	st, err := s.personStatus(period, from.ID)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	page.Remaining = remainingOf(st)
	page.Unlimited = st.Cap < 0
	s.render(w, "transfer.gohtml", page)
}

func (s *Server) subURL(r *http.Request, p store.Person) string {
	host := s.cfg.BoxIP
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		host = h
	} else if r.Host != "" {
		host = r.Host
	}
	// When the portal is published to a dev machine, pointing the phone at
	// localhost would be useless; the LAN address of the box is the truth.
	if !s.cfg.Dev {
		if host == "" || host == "127.0.0.1" || host == "localhost" {
			host = s.cfg.BoxIP
		}
	}
	return s.cfg.SubURL(host, p.SubToken)
}

// ---- subscription -----------------------------------------------------------

func (s *Server) sub(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.URL.Path, "/sub/")
	if token == "" || strings.Contains(token, "/") {
		http.NotFound(w, r)
		return
	}
	person, err := s.db.PersonByToken(token)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !person.Enabled {
		http.Error(w, "subscription disabled", http.StatusForbidden)
		return
	}

	now := time.Now().Unix()
	if dev, derr := s.db.DeviceByIP(clientIP(r)); derr == nil {
		switch {
		case dev.Owner == nil || *dev.Owner == person.ID:
			if dev.BindSource != "sub" || dev.Owner == nil {
				_ = s.db.Bind(dev.MAC, person.ID, "sub", now)
				_ = s.db.AddEvent("bind", "dev:"+dev.MAC,
					fmt.Sprintf("subscription fetch bound it to person:%d", person.ID))
			} else {
				_ = s.db.TouchBinding(dev.MAC, now)
			}
		default:
			// A VLESS binding is the stronger signal; a stale HTTP one is not
			// allowed to steal an active device from another person.
			_ = s.db.AddEvent("bind_rejected", "dev:"+dev.MAC,
				fmt.Sprintf("sub fetch by person:%d ignored, bound by %s", person.ID, dev.BindSource))
		}
	}

	period, err := s.db.EnsurePeriod(time.Now(), s.loc)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	in, err := quota.Gather(s.db, period, s.loc)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	s.withPending(&in)
	dec := quota.Compute(in)
	st := dec.Subjects[quota.PersonSubject(person.ID)]

	// One unconnectable row per owned device, titled with its name and what it
	// used this period. Clients only understand configs, so a row is the only
	// way to show per-device usage in them.
	devices, err := s.db.Devices()
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	locale := person.Locale
	if locale == "" {
		locale = "fa"
	}
	const noConnect = "vless://00000000-0000-0000-0000-000000000000@127.0.0.1:1"
	q := url.Values{}
	q.Set("type", "tcp")
	q.Set("security", "none")
	q.Set("encryption", "none")
	var lines []string

	// One connectable row per enabled (person, route) listener. These are what
	// make the subscription installable; the rows below are only labels.
	routes, err := s.db.Routes()
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	creds, err := s.db.Credentials()
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	routeByID := map[int64]store.Route{}
	for _, rt := range routes {
		routeByID[rt.ID] = rt
	}
	for _, c := range creds {
		rt, ok := routeByID[c.RouteID]
		if c.PersonID != person.ID || !c.Enabled || !ok || !rt.Enabled || !rt.AllowAll {
			continue
		}
		lines = append(lines, fmt.Sprintf("vless://%s@%s:%d?%s#%s",
			c.UUID, s.cfg.BoxIP, c.Port, q.Encode(), url.PathEscape(rt.Name)))
	}

	// The remaining share as a row, x-ui style: clients that only show server
	// names would otherwise hide the Subscription-Userinfo header.
	remaining := st.Cap - st.Used
	if remaining < 0 {
		remaining = 0
	}
	title := T(locale, "sub.unlimited")
	if st.Cap >= 0 {
		title = fmt.Sprintf(T(locale, "sub.remaining"), Bytes(remaining, locale), Bytes(st.Cap, locale))
	}
	lines = append(lines, fmt.Sprintf("%s?%s#%s", noConnect, q.Encode(), url.PathEscape(title)))

	for _, d := range devices {
		if d.Owner == nil || *d.Owner != person.ID {
			continue
		}
		used := store.ChargedBytes(in.Totals[quota.DeviceSubject(d.MAC)].Charged)
		title := deviceLabel(d) + " · " + Bytes(used, locale)
		lines = append(lines, fmt.Sprintf("%s?%s#%s", noConnect, q.Encode(), url.PathEscape(title)))
	}

	total := st.Cap
	if total < 0 {
		total = 0 // clients read 0 as unlimited
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Profile-Update-Interval", "12")
	w.Header().Set("Subscription-Userinfo",
		fmt.Sprintf("upload=0; download=%d; total=%d; expire=%d", st.Used, total, period.EndUTC))
	body := strings.Join(lines, "\n")
	if body != "" {
		body += "\n"
	}
	// Happ and the other common clients take the open URI list; the base64
	// wrapper is the x-ui convention and is opt-in.
	if r.URL.Query().Get("b64") != "" {
		fmt.Fprint(w, base64.StdEncoding.EncodeToString([]byte(body)))
		return
	}
	fmt.Fprint(w, body)
}

// ---- sessions ---------------------------------------------------------------

const sessionCookie = "useg_admin"

func (s *Server) newSession() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b)
	s.mu.Lock()
	s.sessions[tok] = time.Now().Add(12 * time.Hour)
	s.mu.Unlock()
	return tok, nil
}

func (s *Server) authed(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[c.Value]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.sessions, c.Value)
		return false
	}
	return true
}

func (s *Server) auth(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authed(r) {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		fn(w, r)
	}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	l := adminLang(r)
	setLangCookie(w, r)
	page := func(errKey string) map[string]any {
		m := map[string]any{"Lang": l.code, "Dir": l.dir}
		if errKey != "" {
			m["Error"] = T(l.code, errKey)
		}
		return m
	}
	if r.Method == http.MethodPost {
		_ = r.ParseForm()
		got := r.FormValue("password")
		want := s.cfg.AdminPassword
		if want == "" {
			s.render(w, "login.gohtml", page("login.nopass"))
			return
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			time.Sleep(300 * time.Millisecond)
			s.render(w, "login.gohtml", page("login.wrong"))
			return
		}
		tok, err := s.newSession()
		if err != nil {
			http.Error(w, "session error", http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name: sessionCookie, Value: tok, Path: "/",
			HttpOnly: true, SameSite: http.SameSiteStrictMode,
			MaxAge: 12 * 3600,
		})
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if s.authed(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	s.render(w, "login.gohtml", page(""))
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

func atoiDefault(s string, def int64) int64 {
	if s == "" {
		return def
	}
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		return v
	}
	return def
}

func floatDefault(s string, def float64) float64 {
	if s == "" {
		return def
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return v
	}
	return def
}
