package web

import (
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
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
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

	mu       sync.Mutex
	sessions map[string]time.Time
	bypass   bool
	pending  func() map[string]store.Totals

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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		s.log.Error("render", "tmpl", name, "err", err)
	}
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
	quota.MergePending(in.Totals, s.pending())
}

// ---- portal -----------------------------------------------------------------

type portalPage struct {
	Lang, Dir  string
	Known      bool
	DeviceName string
	MAC        string
	PersonName string
	Guest      bool
	Used       int64
	Cap        int64
	Remaining  int64
	Unlimited  bool
	Pct        int
	Over       bool
	Blocked    bool
	Reason     string
	SubURL     string
	PeriodEnd  string
}

func reasonText(l, reason string) string {
	key := map[string]string{
		quota.ReasonQuota:     "reason.quota",
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

func (s *Server) portal(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
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
		in, gerr := quota.Gather(s.db, period)
		if gerr != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		s.withPending(&in)
		dec := quota.Compute(in)
		page.Known = true
		page.MAC = dev.MAC
		page.DeviceName = dev.Hostname
		if page.DeviceName == "" {
			page.DeviceName = dev.MAC
		}
		page.Guest = dev.Owner == nil
		subj := quota.DeviceSubject(dev.MAC)
		if dev.Owner != nil {
			subj = quota.PersonSubject(*dev.Owner)
			if p, perr := s.db.Person(*dev.Owner); perr == nil {
				page.PersonName = p.Name
				if p.Enabled {
					page.SubURL = s.subURL(r, p)
				}
			}
		}
		if st, ok := dec.Subjects[subj]; ok {
			page.Used = st.Used
			page.Cap = st.Cap
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
	}
	s.render(w, "portal.gohtml", page)
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
	return fmt.Sprintf("http://%s:%d/sub/%s", host, s.cfg.PortalPort, p.SubToken)
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

	creds, err := s.db.Credentials()
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	routes, err := s.db.Routes()
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	routeByID := map[int64]store.Route{}
	for _, rt := range routes {
		routeByID[rt.ID] = rt
	}
	var lines []string
	for _, c := range creds {
		rt, ok := routeByID[c.RouteID]
		if !ok || c.PersonID != person.ID || !c.Enabled || !rt.Enabled {
			continue
		}
		if !rt.AllowAll {
			continue // v1: per-route grants are not implemented yet
		}
		q := url.Values{}
		q.Set("type", "tcp")
		q.Set("security", "none")
		q.Set("encryption", "none")
		lines = append(lines, fmt.Sprintf("vless://%s@%s:%d?%s#%s",
			c.UUID, s.cfg.BoxIP, c.Port, q.Encode(), url.PathEscape(rt.Name)))
	}
	if r.URL.Query().Get("raw") != "" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, strings.Join(lines, "\n"))
		return
	}
	body := strings.Join(lines, "\n")
	if body != "" {
		body += "\n"
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Profile-Update-Interval", "12")
	fmt.Fprint(w, base64.StdEncoding.EncodeToString([]byte(body)))
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
