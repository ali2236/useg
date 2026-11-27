package web

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"useg/internal/config"
	"useg/internal/enforce"
	"useg/internal/store"
)

func tserver(t *testing.T) (*Server, *store.DB, *config.Config, store.Person) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		DataDir: dir, DBPath: filepath.Join(dir, "t.db"),
		LanCIDR: "10.66.0.0/24", BoxIP: "10.66.0.2", RouterIP: "10.66.0.1",
		PortalListen: ":0", AdminListen: ":0", AdminPassword: "pw",
		PortalPort: 8080, TProxyPort: 7895, ClashURL: "http://127.0.0.1:9090",
		Timezone: "Asia/Tehran", Dev: true,
	}
	for k, v := range map[string]string{
		"pool_charged_gb": "20", "guest_pool_gb": "2", "guest_device_gb": "0.01",
		"anchor_start_jalali": "1405-08-12",
	} {
		if err := db.SetSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}
	pid, err := db.CreatePerson("ali", 1.0, "fa")
	if err != nil {
		t.Fatal(err)
	}
	rid, err := db.SaveRoute(store.Route{Name: "primary", Kind: "direct", UpstreamJSON: "{}",
		AllowAll: true, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.EnsureCredential(pid, rid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertLease("aa:bb:cc:00:00:03", "10.66.0.3", "phone", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	nft := enforce.New(enforce.Options{LanCIDR: "10.66.0.0/24", BoxIP: "10.66.0.2",
		RouterIP: "10.66.0.1", TProxyPort: 7895, PortalPort: 8080, Iface: "eth0"}, false,
		discardLogger())
	srv, err := New(db, cfg, nft, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Person(pid)
	if err != nil {
		t.Fatal(err)
	}
	return srv, db, cfg, p
}

func do(srv http.Handler, method, target, remote string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, target, body)
	req.RemoteAddr = remote
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, c := range cookies {
		if c != nil {
			req.AddCookie(c)
		}
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// An OS connectivity probe must land on the portal, not on a 404, or the device
// stays silent instead of showing its sign-in sheet.
func TestCaptiveProbeRedirectsToPortal(t *testing.T) {
	srv, _, cfg, _ := tserver(t)
	h := srv.PortalHandler()
	want := "http://" + cfg.BoxIP + "/"
	for _, path := range []string{"/hotspot-detect.html", "/generate_204", "/connecttest.txt", "/success.txt"} {
		rec := do(h, "GET", path, "10.66.0.99:5000", nil, nil)
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
			t.Errorf("%s: code=%d location=%q, want 302 %q", path, rec.Code, rec.Header().Get("Location"), want)
		}
	}
	if rec := do(h, "GET", "/nope", "10.66.0.99:5000", nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("unrelated path should stay 404, got %d", rec.Code)
	}
}

func TestPortalShowsDeviceAndGuestState(t *testing.T) {
	srv, _, _, _ := tserver(t)
	h := srv.PortalHandler()

	rec := do(h, "GET", "/", "10.66.0.3:5000", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("portal status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "phone") {
		t.Error("portal should name the device")
	}
	// A guest device with no usage has its full guest allowance.
	if !strings.Contains(rec.Body.String(), "۱۰") && !strings.Contains(rec.Body.String(), "10.0 MB") {
		t.Errorf("expected a 10 MB guest allowance, got:\n%s", rec.Body.String())
	}

	// An unknown source is told so instead of seeing someone else's numbers.
	rec = do(h, "GET", "/", "10.66.0.99:5000", nil, nil)
	if !strings.Contains(rec.Body.String(), "شناسایی نشد") {
		t.Errorf("unknown device page missing notice:\n%s", rec.Body.String())
	}
}

func TestSubFetchBindsDeviceAndListsRoutes(t *testing.T) {
	srv, db, _, person := tserver(t)
	h := srv.PortalHandler()

	rec := do(h, "GET", "/sub/"+person.SubToken, "10.66.0.3:5000", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("sub status %d", rec.Code)
	}
	raw, err := base64.StdEncoding.DecodeString(rec.Body.String())
	if err != nil {
		t.Fatalf("subscription is not base64: %v", err)
	}
	if !strings.HasPrefix(string(raw), "vless://") || !strings.Contains(string(raw), "#primary") {
		t.Fatalf("unexpected subscription:\n%s", raw)
	}

	dev, err := db.DeviceByMAC("aa:bb:cc:00:00:03")
	if err != nil {
		t.Fatal(err)
	}
	if dev.Owner == nil || *dev.Owner != person.ID || dev.BindSource != "sub" {
		t.Fatalf("device not bound by the sub fetch: %+v", dev)
	}

	// A VLESS binding is stronger: a later HTTP fetch from another person's
	// device must not steal it.
	if err := db.UpsertLease("dd:ee:ff:00:00:09", "10.66.0.9", "friend", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	other, err := db.CreatePerson("other", 1.0, "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Bind("aa:bb:cc:00:00:03", other, "vless", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	do(h, "GET", "/sub/"+person.SubToken, "10.66.0.3:5000", nil, nil)
	dev, _ = db.DeviceByMAC("aa:bb:cc:00:00:03")
	if dev.Owner == nil || *dev.Owner != other {
		t.Fatalf("vless binding was stolen by a sub fetch: %+v", dev)
	}
}

func TestBadTokenIs404(t *testing.T) {
	srv, _, _, _ := tserver(t)
	rec := do(srv.PortalHandler(), "GET", "/sub/nope", "10.66.0.3:5000", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestAdminNeedsSession(t *testing.T) {
	srv, _, _, _ := tserver(t)
	h := srv.AdminHandler()

	rec := do(h, "GET", "/admin", "10.66.0.3:5000", nil, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login" {
		t.Fatalf("unauthenticated admin got %d %q", rec.Code, rec.Header().Get("Location"))
	}

	bad := do(h, "POST", "/admin/login", "10.66.0.3:5000", url.Values{"password": {"nope"}}, nil)
	if bad.Code != 200 || !strings.Contains(bad.Body.String(), "گذرواژه نادرست است") {
		t.Fatalf("bad password accepted: %d", bad.Code)
	}

	ok := do(h, "POST", "/admin/login", "10.66.0.3:5000", url.Values{"password": {"pw"}}, nil)
	if ok.Code != http.StatusSeeOther {
		t.Fatalf("login status %d", ok.Code)
	}
	var cookie *http.Cookie
	for _, c := range ok.Result().Cookies() {
		if c.Name == sessionCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie")
	}
	rec = do(h, "GET", "/admin", "10.66.0.3:5000", nil, cookie)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `<html lang="fa" dir="rtl">`) || !strings.Contains(body, "<h2>افراد</h2>") || !strings.Contains(body, `href="/assets/app.css?v=2"`) {
		t.Fatalf("dashboard: %d", rec.Code)
	}
	for _, want := range []string{
		`id="tab-dashboard"`,
		`class="panel is-active" id="dashboard"`,
		"<h2>داشبورد</h2>",
		"<strong>ali</strong>",
		"<strong>phone</strong>",
		"دستگاه‌های مهمان",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
}

func TestAdminRouteSavesRates(t *testing.T) {
	srv, db, _, _ := tserver(t)
	h := srv.AdminHandler()
	ok := do(h, "POST", "/admin/login", "10.66.0.3:5000", url.Values{"password": {"pw"}}, nil)
	var cookie *http.Cookie
	for _, c := range ok.Result().Cookies() {
		if c.Name == sessionCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie")
	}

	classes, err := db.Classes()
	if err != nil {
		t.Fatal(err)
	}
	var nimbaha int64
	for _, c := range classes {
		if c.Name == "nimbaha" {
			nimbaha = c.ID
		}
	}
	routes, err := db.Routes()
	if err != nil || len(routes) == 0 || nimbaha == 0 {
		t.Fatalf("fixture: routes=%v nimbaha=%d err=%v", routes, nimbaha, err)
	}
	rid := routes[0].ID
	key := "w_" + strconv.FormatInt(rid, 10) + "_" + strconv.FormatInt(nimbaha, 10)

	form := url.Values{
		"id": {strconv.FormatInt(rid, 10)}, "name": {"primary"}, "kind": {"direct"},
		"upstream": {"{}"}, "allow_all": {"1"}, "enabled": {"1"}, key: {"0.25"},
	}
	if rec := do(h, "POST", "/admin/route", "10.66.0.3:5000", form, cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("save route: %d", rec.Code)
	}
	rates, _ := db.RateMap()
	if rates[rid][nimbaha] != 0.25 {
		t.Fatalf("saved rate = %v, want 0.25", rates[rid][nimbaha])
	}

	// A new route posts w_0_<class>; the cells must land on the row it creates.
	do(h, "POST", "/admin/route", "10.66.0.3:5000", url.Values{
		"id": {"0"}, "name": {"second"}, "kind": {"direct"}, "upstream": {"{}"},
		"enabled": {"1"}, "w_0_" + strconv.FormatInt(nimbaha, 10): {"0.5"},
	}, cookie)
	routes, _ = db.Routes()
	rates, _ = db.RateMap()
	var newID int64
	for _, rt := range routes {
		if rt.Name == "second" {
			newID = rt.ID
		}
	}
	if newID == 0 || rates[newID][nimbaha] != 0.5 {
		t.Fatalf("new route rate = %v, want 0.5", rates[newID][nimbaha])
	}
}

func TestAdminCapsSwitchUnblocksEverybody(t *testing.T) {
	srv, db, _, _ := tserver(t)
	h := srv.AdminHandler()
	ok := do(h, "POST", "/admin/login", "10.66.0.3:5000", url.Values{"password": {"pw"}}, nil)
	var cookie *http.Cookie
	for _, c := range ok.Result().Cookies() {
		if c.Name == sessionCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie")
	}

	// A guest that is far over every configured cap.
	srv.SetPending(func() map[string]store.Totals {
		return map[string]store.Totals{"dev:aa:bb:cc:00:00:03": {Charged: 20e9, Raw: 20e9}}
	})

	rec := do(h, "POST", "/admin/caps", "10.66.0.3:5000", url.Values{"off": {"1"}}, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("caps off status %d", rec.Code)
	}
	if v, _ := db.GetSetting("caps_off"); v != "1" {
		t.Fatalf("caps_off = %q", v)
	}
	body := do(srv.PortalHandler(), "GET", "/", "10.66.0.3:5000", nil, nil).Body.String()
	if !strings.Contains(body, "نامحدود") || strings.Contains(body, "قطع شده است") {
		t.Errorf("caps off should unblock the guest:\n%s", body)
	}

	do(h, "POST", "/admin/caps", "10.66.0.3:5000", url.Values{"off": {"0"}}, cookie)
	body = do(srv.PortalHandler(), "GET", "/", "10.66.0.3:5000", nil, nil).Body.String()
	if !strings.Contains(body, "قطع شده است") {
		t.Errorf("caps back on should block the guest again:\n%s", body)
	}
}

func TestAdminDataCycle(t *testing.T) {
	srv, db, _, person := tserver(t)
	h := srv.AdminHandler()
	ok := do(h, "POST", "/admin/login", "10.66.0.3:5000", url.Values{"password": {"pw"}}, nil)
	var cookie *http.Cookie
	for _, c := range ok.Result().Cookies() {
		if c.Name == sessionCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie")
	}
	sara, err := db.CreatePerson("sara", 0.5, "en")
	if err != nil {
		t.Fatal(err)
	}

	// Reset: 30 days, 10 GB, all unallocated.
	if rec := do(h, "POST", "/admin/data/reset", "10.66.0.3:5000", url.Values{
		"length": {"30"}, "unit": {"days"}, "gb": {"10"},
	}, cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("reset: %d", rec.Code)
	}
	p, err := db.CurrentPeriod(time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	if p.AllocMode != "manual" || p.PoolCharged == nil || *p.PoolCharged != 10e9 {
		t.Fatalf("period after reset = %+v", p)
	}
	if p.EndUTC-p.StartUTC != 30*86400 {
		t.Fatalf("cycle = %d seconds", p.EndUTC-p.StartUTC)
	}
	if p.GuestPool == nil || *p.GuestPool != 0 {
		t.Fatalf("guest pool = %v, want 0", p.GuestPool)
	}
	if p.RawCeiling == nil || *p.RawCeiling != 10e9 {
		t.Fatalf("ceiling after reset = %v", p.RawCeiling)
	}

	// Add 5 GB to the unallocated pool.
	do(h, "POST", "/admin/data/add", "10.66.0.3:5000", url.Values{"gb": {"5"}}, cookie)
	p, _ = db.CurrentPeriod(time.Now().Unix())
	if p.PoolCharged == nil || *p.PoolCharged != 15e9 {
		t.Fatalf("pool after add = %v", p.PoolCharged)
	}
	if p.RawCeiling == nil || *p.RawCeiling != 15e9 {
		t.Fatalf("ceiling after add = %v", p.RawCeiling)
	}

	// Equal split: two people plus the guest pool, 5 GB each.
	do(h, "POST", "/admin/data/allocate-equal", "10.66.0.3:5000", url.Values{}, cookie)
	allocs, _ := db.Allocations(p.ID)
	if allocs[person.ID] != 5e9 || allocs[sara] != 5e9 {
		t.Fatalf("equal allocations = %v", allocs)
	}
	p, _ = db.CurrentPeriod(time.Now().Unix())
	if p.GuestPool == nil || *p.GuestPool != 5e9 {
		t.Fatalf("guest allocation = %v", p.GuestPool)
	}

	// Weighted top-up of 3 GB: 1 : 0.5 : 0.5.
	do(h, "POST", "/admin/data/add", "10.66.0.3:5000", url.Values{"gb": {"3"}}, cookie)
	do(h, "POST", "/admin/data/allocate-weight", "10.66.0.3:5000", url.Values{
		"weight_" + strconv.FormatInt(person.ID, 10): {"1"},
		"weight_" + strconv.FormatInt(sara, 10):      {"0.5"},
		"weight_guest":                               {"0.5"},
	}, cookie)
	allocs, _ = db.Allocations(p.ID)
	if allocs[person.ID] != 65e8 || allocs[sara] != 575e7 {
		t.Fatalf("weighted allocations = %v", allocs)
	}
	p, _ = db.CurrentPeriod(time.Now().Unix())
	if p.GuestPool == nil || *p.GuestPool != 575e7 {
		t.Fatalf("weighted guest allocation = %v", p.GuestPool)
	}

	body := do(h, "GET", "/admin?lang=en", "10.66.0.3:5000", nil, cookie).Body.String()
	for _, want := range []string{`id="tab-data"`, "Data cycle", "Data settings", "Allocate equally", "Allocate by weight", "Add / remove (GB)", "Start a new cycle", `id="data-charts"`, "Allocation by person", "Usage by person", `width: 36%`} {
		if !strings.Contains(body, want) {
			t.Errorf("data tab missing %q", want)
		}
	}
	for _, gone := range []string{`id="tab-period"`, `id="period"`} {
		if strings.Contains(body, gone) {
			t.Errorf("period tab still rendered: %q", gone)
		}
	}

	// + gives one person GB from the unallocated pool.
	do(h, "POST", "/admin/data/add", "10.66.0.3:5000", url.Values{"gb": {"2"}}, cookie)
	saraKey := "gb_" + strconv.FormatInt(sara, 10)
	do(h, "POST", "/admin/data/allocate", "10.66.0.3:5000", url.Values{
		"person": {strconv.FormatInt(sara, 10)}, saraKey: {"1"},
	}, cookie)
	allocs, _ = db.Allocations(p.ID)
	if allocs[sara] != 675e7 {
		t.Fatalf("allocate = %v", allocs)
	}
	// Asking for more than the pool has left gives the rest, not an error.
	do(h, "POST", "/admin/data/allocate", "10.66.0.3:5000", url.Values{
		"person": {strconv.FormatInt(sara, 10)}, saraKey: {"100"},
	}, cookie)
	allocs, _ = db.Allocations(p.ID)
	if allocs[sara] != 775e7 {
		t.Fatalf("allocate should clamp to the unallocated pool, got %v", allocs[sara])
	}
	// − can only take back unused data: 4 GB used, so 7.75 GB drops to 4 GB.
	if err := db.UpsertUsage(p.ID, []store.UsageRow{{
		Subject: "person:" + strconv.FormatInt(sara, 10), RouteID: 1, ClassID: 1, Charged: 4e9,
	}}); err != nil {
		t.Fatal(err)
	}
	do(h, "POST", "/admin/data/unallocate", "10.66.0.3:5000", url.Values{
		"person": {strconv.FormatInt(sara, 10)}, saraKey: {"100"},
	}, cookie)
	allocs, _ = db.Allocations(p.ID)
	if allocs[sara] != 4e9 {
		t.Fatalf("unallocate should stop at used data, got %v", allocs[sara])
	}
	rec := do(h, "POST", "/admin/data/unallocate", "10.66.0.3:5000", url.Values{
		"person": {strconv.FormatInt(sara, 10)}, saraKey: {"1"},
	}, cookie)
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "err=") {
		t.Fatalf("unallocate with nothing unused redirect = %q", loc)
	}
	// The guest row uses the same controls.
	do(h, "POST", "/admin/data/allocate", "10.66.0.3:5000", url.Values{
		"person": {"guest"}, "gb_guest": {"0.5"},
	}, cookie)
	p, _ = db.CurrentPeriod(time.Now().Unix())
	if p.GuestPool == nil || *p.GuestPool != 625e7 {
		t.Fatalf("guest allocate = %v", p.GuestPool)
	}
}

func TestAdminMonthlyDataSettings(t *testing.T) {
	srv, db, _, _ := tserver(t)
	h := srv.AdminHandler()
	ok := do(h, "POST", "/admin/login", "10.66.0.3:5000", url.Values{"password": {"pw"}}, nil)
	var cookie *http.Cookie
	for _, c := range ok.Result().Cookies() {
		if c.Name == sessionCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie")
	}

	if rec := do(h, "POST", "/admin/data/settings", "10.66.0.3:5000", url.Values{
		"pool_charged_gb": {"25"}, "guest_pool_gb": {"3"}, "guest_device_gb": {"0.02"},
	}, cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("settings: %d", rec.Code)
	}
	p, err := db.CurrentPeriod(time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	if p.AllocMode == "manual" || p.PoolCharged == nil || *p.PoolCharged != 25e9 {
		t.Fatalf("period after settings = %+v", p)
	}
	if p.GuestPool == nil || *p.GuestPool != 3e9 {
		t.Fatalf("guest pool after settings = %v", p.GuestPool)
	}
	if p.RawCeiling == nil || *p.RawCeiling != 25e9 {
		t.Fatalf("ceiling after settings = %v", p.RawCeiling)
	}
	if v, _ := db.GetSetting("guest_device_gb"); v != "0.02" {
		t.Fatalf("guest device = %q", v)
	}

	body := do(h, "GET", "/admin?lang=en", "10.66.0.3:5000", nil, cookie).Body.String()
	for _, want := range []string{"Data settings", "pool charged (GB)", "guest device (GB)", `value="25"`} {
		if !strings.Contains(body, want) {
			t.Errorf("monthly data tab missing %q", want)
		}
	}
	if strings.Contains(body, "Add / remove (GB)") {
		t.Error("allocate controls should only show inside a data cycle")
	}
}

func TestAdminLangSwitchAndFont(t *testing.T) {
	srv, _, _, _ := tserver(t)
	h := srv.AdminHandler()
	ok := do(h, "POST", "/admin/login", "10.66.0.3:5000", url.Values{"password": {"pw"}}, nil)
	var cookie *http.Cookie
	for _, c := range ok.Result().Cookies() {
		if c.Name == sessionCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie")
	}

	rec := do(h, "GET", "/admin?lang=en", "10.66.0.3:5000", nil, cookie)
	body := rec.Body.String()
	if !strings.Contains(body, `<html lang="en" dir="ltr">`) || !strings.Contains(body, "<h2>People</h2>") || !strings.Contains(body, `href="/assets/app.css?v=2"`) {
		t.Errorf("english admin missing:\n%s", body)
	}
	for _, want := range []string{`id="audit"`, `id="machine"`, `data-tab="dashboard"`, `data-tab="data"`, `class="panel is-active"`, "</html>", "class rates", "Days left", "Start a new cycle", "Usage by person"} {
		if !strings.Contains(body, want) {
			t.Errorf("admin page truncated or broken, missing %q", want)
		}
	}
	var langC *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == langCookie {
			langC = c
		}
	}
	if langC == nil || langC.Value != "en" {
		t.Fatalf("lang cookie not set: %+v", langC)
	}

	// no lang param: the cookie keeps English across the POST redirect
	rec = do(h, "GET", "/admin", "10.66.0.3:5000", nil, cookie, langC)
	if !strings.Contains(rec.Body.String(), `<html lang="en" dir="ltr">`) {
		t.Error("lang choice should survive the redirect back to /admin")
	}

	for _, asset := range []string{"estedad.woff2", "app.css"} {
		got := do(h, "GET", "/assets/"+asset, "10.66.0.3:5000", nil, nil)
		if got.Code != 200 || got.Body.Len() == 0 {
			t.Fatalf("%s not served: %d", asset, got.Code)
		}
		if cc := got.Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s Cache-Control = %q", asset, cc)
		}
	}
}

func TestDomesticTabRefreshAndPaging(t *testing.T) {
	srv, db, _, _ := tserver(t)
	h := srv.AdminHandler()

	ok := do(h, "POST", "/admin/login", "10.66.0.3:5000", url.Values{"password": {"pw"}}, nil)
	var cookie *http.Cookie
	for _, c := range ok.Result().Cookies() {
		if c.Name == sessionCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie")
	}

	var sites []store.DomesticSite
	for i := 0; i < 60; i++ {
		sites = append(sites, store.DomesticSite{
			Domain:  fmt.Sprintf("site%03d.ir", i),
			Network: fmt.Sprintf("10.0.%d.0/24", i),
			Updated: "1405/09/04",
			Due:     "1405/09/15",
		})
	}
	if _, err := db.ReplaceDomestic(sites); err != nil {
		t.Fatal(err)
	}

	rec := do(h, "GET", "/admin", "10.66.0.3:5000", nil, cookie)
	body := rec.Body.String()
	for _, want := range []string{`data-tab="domestic"`, "site000.ir", "10.0.0.0/24"} {
		if !strings.Contains(body, want) {
			t.Errorf("domestic tab missing %q", want)
		}
	}

	rec = do(h, "GET", "/admin?dpage=2", "10.66.0.3:5000", nil, cookie)
	body = rec.Body.String()
	if !strings.Contains(body, "site050.ir") || !strings.Contains(body, "2 / 2") {
		t.Errorf("page 2 not rendered:\n%s", body)
	}

	var fetched bool
	srv.fetchDomestic = func(context.Context) ([]store.DomesticSite, error) {
		fetched = true
		return []store.DomesticSite{{Domain: "refreshed.ir", Network: "10.9.0.0/24"}}, nil
	}
	rec = do(h, "POST", "/admin/domestic/refresh", "10.66.0.3:5000", url.Values{}, cookie)
	if rec.Code != http.StatusSeeOther || !fetched {
		t.Fatalf("refresh: code=%d fetched=%v", rec.Code, fetched)
	}
	if loc := rec.Header().Get("Location"); loc != "/admin?ok=domestic.refreshed" {
		t.Errorf("refresh redirect %q", loc)
	}
	page, err := db.DomesticSites("refreshed", 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 {
		t.Fatalf("refresh did not replace the list: %+v", page)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestPendingUsageIsVisibleImmediately(t *testing.T) {
	srv, _, _, _ := tserver(t)
	// Guest cap is 10 MB (0.01 GB); 3 MB is still in the accountant's buckets.
	srv.SetPending(func() map[string]store.Totals {
		return map[string]store.Totals{"dev:aa:bb:cc:00:00:03": {Charged: 3e6, Raw: 3e6}}
	})
	rec := do(srv.PortalHandler(), "GET", "/", "10.66.0.3:5000", nil, nil)
	body := rec.Body.String()
	if !strings.Contains(body, "۷٫۰ MB") {
		t.Errorf("remaining should already be 7 MB (10 - 3), got:\n%s", body)
	}
}
