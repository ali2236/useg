package web

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
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

// fragment is the percent-decoded #name of a subscription row.
func fragment(row string) string {
	s, _ := url.PathUnescape(row[strings.LastIndex(row, "#")+1:])
	return s
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
	if strings.Contains(rec.Body.String(), "افراد") {
		t.Error("a guest must not see the household's people section")
	}
	// A guest device with no usage has its full guest allowance.
	if !strings.Contains(rec.Body.String(), "۱۰ مگابایت") {
		t.Errorf("expected a 10 MB guest allowance, got:\n%s", rec.Body.String())
	}

	// An unknown source is told so instead of seeing someone else's numbers.
	rec = do(h, "GET", "/", "10.66.0.99:5000", nil, nil)
	if !strings.Contains(rec.Body.String(), "شناسایی نشد") {
		t.Errorf("unknown device page missing notice:\n%s", rec.Body.String())
	}
}

// A person's portal greets them by name and lists the devices on their share,
// biggest consumer first, with the same numbers the subscription reports.
func TestPortalIdentifiedPersonSeesTheirDevices(t *testing.T) {
	srv, db, cfg, person := tserver(t)
	now := time.Now()
	period, err := db.EnsurePeriod(now, cfg.Location())
	if err != nil {
		t.Fatal(err)
	}
	phone, tablet := "aa:bb:cc:00:00:03", "aa:bb:cc:00:00:04"
	if err := db.UpsertLease(tablet, "10.66.0.4", "tablet", now.Unix()); err != nil {
		t.Fatal(err)
	}
	for _, mac := range []string{phone, tablet} {
		if err := db.Bind(mac, person.ID, "test", now.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.UpsertUsage(period.ID, []store.UsageRow{
		{Subject: "dev:" + phone, RouteID: 1, ClassID: 1, Charged: 500_000_000},
		{Subject: "dev:" + tablet, RouteID: 1, ClassID: 1, Charged: 1_500_000_000},
	}); err != nil {
		t.Fatal(err)
	}
	saraID, err := db.CreatePerson("sara", 1.0, "fa")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertUsage(period.ID, []store.UsageRow{{
		Subject: fmt.Sprintf("person:%d", saraID), RouteID: 1, ClassID: 1, Charged: 2_500_000_000,
	}}); err != nil {
		t.Fatal(err)
	}

	body := do(srv.PortalHandler(), "GET", "/", "10.66.0.3:5000", nil, nil).Body.String()
	if !strings.Contains(body, "سلام، "+person.Name) {
		t.Errorf("portal should greet the identified person:\n%s", body)
	}
	for _, want := range []string{"phone", "tablet", "۱٫۵ گیگابایت", "افراد", "sara", "شما"} {
		if !strings.Contains(body, want) {
			t.Errorf("portal misses %q:\n%s", want, body)
		}
	}
	// People rows are for transferring, not for showing anyone's usage.
	if people := body[strings.Index(body, "افراد"):]; strings.Contains(people, "۲٫۵ گیگابایت") {
		t.Error("people section must not show usage")
	}
	if people := body[strings.Index(body, "افراد"):]; !strings.Contains(people, "/transfer?to=") {
		t.Error("people section should put the transfer control in the row's end slot")
	}
	if strings.Index(body, "tablet") > strings.Index(body, "phone") {
		t.Error("devices should be sorted by usage, biggest first")
	}
	if people := body[strings.Index(body, "افراد"):]; strings.Index(people, "sara") > strings.Index(people, "ali") {
		t.Error("people should be sorted by usage, biggest first")
	}
	if !strings.Contains(body, "<details") || strings.Contains(body, "<details open") {
		t.Error("people section should be collapsed by default")
	}
	if strings.Contains(body, "/sub/") {
		t.Error("subscription link should stay hidden on the portal")
	}
}

// The portal must carry a valid web app manifest and every icon it promises,
// or the phone falls back to a screenshot for the home-screen shortcut.
func TestPortalIsInstallable(t *testing.T) {
	srv, _, _, _ := tserver(t)
	h := srv.PortalHandler()

	rec := do(h, "GET", "/assets/manifest.json", "10.66.0.3:5000", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("manifest status %d", rec.Code)
	}
	var m struct {
		Name     string
		StartURL string `json:"start_url"`
		Display  string
		Icons    []struct {
			Src   string
			Sizes string
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("manifest is not JSON: %v", err)
	}
	if m.Name == "" || m.StartURL != "/" || m.Display != "standalone" {
		t.Errorf("manifest head = %+v", m)
	}
	sizes := map[string]bool{}
	for _, ic := range m.Icons {
		sizes[ic.Sizes] = true
		if got := do(h, "GET", ic.Src, "10.66.0.3:5000", nil, nil); got.Code != http.StatusOK {
			t.Errorf("icon %s status %d", ic.Src, got.Code)
		}
	}
	if !sizes["192x192"] || !sizes["512x512"] {
		t.Errorf("manifest icons lack 192/512: %v", sizes)
	}

	body := do(h, "GET", "/", "10.66.0.3:5000", nil, nil).Body.String()
	if !strings.Contains(body, `rel="manifest"`) || !strings.Contains(body, `rel="apple-touch-icon"`) {
		t.Error("portal does not link the manifest or an apple-touch-icon")
	}
}

func TestSubListsRoutesQuotaAndDeviceUsage(t *testing.T) {
	srv, db, cfg, person := tserver(t)
	period, err := db.EnsurePeriod(time.Now(), cfg.Location())
	if err != nil {
		t.Fatal(err)
	}
	routeID := int64(1)
	if rs, _ := db.Routes(); len(rs) > 0 {
		routeID = rs[0].ID
	}
	if err := db.UpsertUsage(period.ID, []store.UsageRow{{
		Subject: "dev:aa:bb:cc:00:00:03", RouteID: routeID, ClassID: 1,
		Up: 2_000_000_000, Down: 5_000_000_000, Charged: 7_000_000_000,
	}}); err != nil {
		t.Fatal(err)
	}

	rec := do(srv.PortalHandler(), "GET", "/sub/"+person.SubToken, "10.66.0.3:5000", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("sub status %d", rec.Code)
	}
	info := rec.Header().Get("Subscription-Userinfo")
	for _, want := range []string{
		"upload=0", "download=7000000000", "total=18000000000",
		"expire=" + strconv.FormatInt(period.EndUTC, 10),
	} {
		if !strings.Contains(info, want) {
			t.Errorf("Subscription-Userinfo %q misses %q", info, want)
		}
	}

	raw := rec.Body.String()
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if len(lines) != 3 {
		t.Fatalf("want route + quota + device rows, got %d:\n%s", len(lines), raw)
	}
	cred, err := db.CredentialFor(person.ID, routeID)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("vless://%s@%s:%d?", cred.UUID, cfg.BoxIP, cred.Port)
	if !strings.HasPrefix(lines[0], want) || !strings.HasSuffix(lines[0], "#primary") {
		t.Errorf("route row is not a real config: %s", lines[0])
	}
	if !strings.Contains(fragment(lines[1]), "باقی‌مانده") || !strings.Contains(fragment(lines[1]), "گیگابایت") {
		t.Errorf("quota row misses the remaining share: %s", lines[1])
	}
	if !strings.Contains(fragment(lines[2]), "phone") || !strings.Contains(fragment(lines[2]), "گیگابایت") {
		t.Errorf("row title misses the device name or its usage: %s", lines[2])
	}
}

func TestSubWithoutDevicesIsNotEmpty(t *testing.T) {
	srv, _, cfg, person := tserver(t)

	rec := do(srv.PortalHandler(), "GET", "/sub/"+person.SubToken, "10.66.0.99:5000", nil, nil)
	raw := rec.Body.String()
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if len(lines) != 2 {
		t.Fatalf("want route + quota rows without a device, got %d:\n%s", len(lines), raw)
	}
	if !strings.HasPrefix(lines[0], "vless://") || !strings.Contains(lines[0], "@"+cfg.BoxIP+":") ||
		!strings.HasSuffix(lines[0], "#primary") {
		t.Errorf("route row is not a real config: %s", lines[0])
	}
	if !strings.Contains(fragment(lines[1]), "باقی‌مانده") {
		t.Errorf("quota row missing: %s", lines[1])
	}
}

func TestSubBase64IsOptIn(t *testing.T) {
	srv, _, _, person := tserver(t)

	rec := do(srv.PortalHandler(), "GET", "/sub/"+person.SubToken+"?b64=1", "10.66.0.3:5000", nil, nil)
	raw, err := base64.StdEncoding.DecodeString(rec.Body.String())
	if err != nil {
		t.Fatalf("opt-in subscription is not base64: %v", err)
	}
	if !strings.Contains(string(raw), "#primary") {
		t.Errorf("decoded opt-in subscription misses the route row:\n%s", raw)
	}
	if rec.Header().Get("Subscription-Userinfo") == "" {
		t.Error("opt-in subscription lost the quota header")
	}
}

func TestSubFetchBindsDevice(t *testing.T) {
	srv, db, _, person := tserver(t)
	h := srv.PortalHandler()

	do(h, "GET", "/sub/"+person.SubToken, "10.66.0.3:5000", nil, nil)

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

func TestSubURLFollowsTLS(t *testing.T) {
	cfg := &config.Config{PortalPort: 80}
	if got := cfg.SubURL("10.66.0.2", "tok"); got != "http://10.66.0.2:80/sub/tok" {
		t.Errorf("http link = %q", got)
	}
	cfg.TLSListen = ":443"
	if got := cfg.SubURL("10.66.0.2", "tok"); got != "https://10.66.0.2/sub/tok" {
		t.Errorf("https link = %q", got)
	}
	cfg.TLSListen = ":8443"
	if got := cfg.SubURL("10.66.0.2", "tok"); got != "https://10.66.0.2:8443/sub/tok" {
		t.Errorf("https link with port = %q", got)
	}
}

func TestEnsureCertReusesSelfSigned(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, err := EnsureCert(dir, "10.66.0.2")
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(mustRead(t, certFile))
	if block == nil {
		t.Fatal("cert is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(cert.IPAddresses) != 1 || cert.IPAddresses[0].String() != "10.66.0.2" {
		t.Errorf("cert SANs = %v", cert.IPAddresses)
	}
	if !cert.IsCA {
		t.Error("self-signed cert must be importable as a trusted CA")
	}

	// A second start must keep the same files, or every restart invalidates
	// the cert the phones were told to trust.
	before := string(mustRead(t, certFile)) + string(mustRead(t, keyFile))
	if _, _, err := EnsureCert(dir, "10.66.0.2"); err != nil {
		t.Fatal(err)
	}
	after := string(mustRead(t, certFile)) + string(mustRead(t, keyFile))
	if before != after {
		t.Error("cert files were rewritten on a second call")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
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
	if rec.Code != 200 || !strings.Contains(body, `<html lang="fa" dir="rtl">`) || !strings.Contains(body, "<h2>افراد</h2>") || !strings.Contains(body, `href="/assets/app.css?v=4"`) {
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

func TestAdminUpdateRequestsDeploy(t *testing.T) {
	srv, _, cfg, _ := tserver(t)
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
	if rec := do(h, "POST", "/admin/update", "10.66.0.3:5000", nil, cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("update status %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "update.request")); err != nil {
		t.Fatalf("request file: %v", err)
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
	for _, want := range []string{`id="tab-data"`, "Data cycle", "Data settings", "Allocate equally", "Allocate by weight", "Add / remove (GB)", "Start a new cycle", "Sync with ISP", "ISP remaining (GB)", `formaction="/admin/data/remove"`, `id="data-charts"`, "Allocation by person", "Usage by person", `width: 37%`} {
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

	// Sync adds the ISP's larger remaining figure to the unallocated pool, and
	// never takes data away when the ISP reports less.
	do(h, "POST", "/admin/data/sync", "10.66.0.3:5000", url.Values{"remaining_gb": {"17"}}, cookie)
	p, _ = db.CurrentPeriod(time.Now().Unix())
	if p.PoolCharged == nil || *p.PoolCharged != 21e9 {
		t.Fatalf("pool after sync = %v, want 21 GB", p.PoolCharged)
	}
	rec = do(h, "POST", "/admin/data/sync", "10.66.0.3:5000", url.Values{"remaining_gb": {"10"}}, cookie)
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "err=") {
		t.Fatalf("lower ISP remaining redirect = %q", loc)
	}
	p, _ = db.CurrentPeriod(time.Now().Unix())
	if p.PoolCharged == nil || *p.PoolCharged != 21e9 {
		t.Fatalf("lower ISP remaining changed the pool to %v", p.PoolCharged)
	}
	// Half a charged byte rounds up, so the same ISP figure adds one less byte
	// of headroom than truncation would leave.
	if err := db.UpsertUsage(p.ID, []store.UsageRow{{
		Subject: "person:" + strconv.FormatInt(sara, 10), RouteID: 1, ClassID: 1, Charged: 0.5,
	}}); err != nil {
		t.Fatal(err)
	}
	do(h, "POST", "/admin/data/sync", "10.66.0.3:5000", url.Values{"remaining_gb": {"17"}}, cookie)
	p, _ = db.CurrentPeriod(time.Now().Unix())
	if p.PoolCharged == nil || *p.PoolCharged != 21000000001 {
		t.Fatalf("pool after fractional sync = %v, want one extra byte from rounding up", p.PoolCharged)
	}

	// Removing only touches the unallocated remainder: it never dips into
	// allocated shares, and stops at zero unallocated.
	do(h, "POST", "/admin/data/remove", "10.66.0.3:5000", url.Values{"gb": {"1"}}, cookie)
	p, _ = db.CurrentPeriod(time.Now().Unix())
	if p.PoolCharged == nil || *p.PoolCharged != 20000000001 {
		t.Fatalf("pool after remove = %v, want 20 GB", p.PoolCharged)
	}
	if p.RawCeiling == nil || *p.RawCeiling != 20000000001 {
		t.Fatalf("ceiling after remove = %v, want it to mirror the pool", p.RawCeiling)
	}
	do(h, "POST", "/admin/data/remove", "10.66.0.3:5000", url.Values{"gb": {"100"}}, cookie)
	p, _ = db.CurrentPeriod(time.Now().Unix())
	if p.PoolCharged == nil || *p.PoolCharged != 1675e7 {
		t.Fatalf("remove should clamp to the unallocated remainder, got %v", p.PoolCharged)
	}
	rec = do(h, "POST", "/admin/data/remove", "10.66.0.3:5000", url.Values{"gb": {"1"}}, cookie)
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "err=") {
		t.Fatalf("remove with nothing unallocated redirect = %q", loc)
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
	if strings.Contains(body, "Sync with ISP") {
		t.Error("ISP sync should only show inside a data cycle")
	}
	if strings.Contains(body, `formaction="/admin/data/remove"`) {
		t.Error("removing pool data should only show inside a data cycle")
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
	if !strings.Contains(body, `<html lang="en" dir="ltr">`) || !strings.Contains(body, "<h2>People</h2>") || !strings.Contains(body, `href="/assets/app.css?v=4"`) {
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

func TestDevicesTabSortsByLastUse(t *testing.T) {
	srv, db, _, _ := tserver(t)
	h := srv.AdminHandler()
	now := time.Now().Unix()
	for _, d := range [][2]string{
		{"dd:ee:ff:00:00:01", "10.66.0.7"},
		{"dd:ee:ff:00:00:02", "10.66.0.8"},
	} {
		if err := db.UpsertLease(d[0], d[1], "phone", now-3600); err != nil {
			t.Fatal(err)
		}
	}
	// The device that just moved bytes must come before one that never did,
	// even though both have leases.
	if err := db.SetLastUsed(now+60, []string{"dd:ee:ff:00:00:01"}); err != nil {
		t.Fatal(err)
	}

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
	body := do(h, "GET", "/admin?lang=en", "10.66.0.3:5000", nil, cookie).Body.String()
	if !strings.Contains(body, "last used") {
		t.Error("devices tab needs a last-used column")
	}
	// The pencil only toggles .is-editing, so the form must not start with the
	// hidden attribute or it can never be revealed.
	if !strings.Contains(body, `class="rename-form"`) || strings.Contains(body, `class="rename-form" hidden`) {
		t.Error("rename form must be hidden by CSS, not the hidden attribute")
	}
	used := strings.Index(body, "dd:ee:ff:00:00:01")
	never := strings.Index(body, "dd:ee:ff:00:00:02")
	if used < 0 || never < 0 || used > never {
		t.Errorf("devices order: recently used at %d, never used at %d", used, never)
	}
}

func TestDeviceOwnerSelectedAndPersonSubtree(t *testing.T) {
	srv, db, _, p := tserver(t)
	h := srv.AdminHandler()
	mac := "aa:bb:cc:00:00:03"
	if err := db.Bind(mac, p.ID, "admin", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}

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
	body := do(h, "GET", "/admin?lang=en", "10.66.0.3:5000", nil, cookie).Body.String()

	// The devices tab must preselect the current owner, not reset to guest.
	if !strings.Contains(body, fmt.Sprintf(`<option value="%d" selected>`, p.ID)) {
		t.Errorf("owner %d is not selected in the device form", p.ID)
	}

	// The people tab lists each person's devices in the manage popup with an
	// unbind button, and never exposes subscription links.
	start := strings.Index(body, `id="persons"`)
	end := strings.Index(body[start:], `id="devices"`)
	if start < 0 || end < 0 {
		t.Fatal("persons or devices section not found")
	}
	people := body[start : start+end]
	if !strings.Contains(people, mac) || !strings.Contains(people, `value="unbind"`) {
		t.Errorf("device %s missing from the person subtree:\n%s", mac, people)
	}
	if !strings.Contains(people, `class="person-device"`) || !strings.Contains(people, `dev-bar-num`) {
		t.Errorf("person rows should show devices and used/of labels:\n%s", people)
	}
	if strings.Contains(people, "/sub/") {
		t.Errorf("admin people tab should not show subscription links:\n%s", people)
	}
}

func TestAdminRenamesAndDeletesPerson(t *testing.T) {
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
	pid := strconv.FormatInt(person.ID, 10)

	rec := do(h, "POST", "/admin/person/rename", "10.66.0.3:5000",
		url.Values{"person": {pid}, "name": {"Renamed"}}, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("rename: %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/admin?ok=persons.renamed_ok" {
		t.Errorf("rename redirect = %q", loc)
	}
	if p, err := db.Person(person.ID); err != nil || p.Name != "Renamed" {
		t.Fatalf("name = %q, %v", p.Name, err)
	}

	rec = do(h, "POST", "/admin/person/delete", "10.66.0.3:5000",
		url.Values{"person": {pid}}, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("delete: %d", rec.Code)
	}
	if _, err := db.Person(person.ID); err == nil {
		t.Error("person still exists after delete")
	}
	// The bound device survives as a guest.
	devices, err := db.Devices()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, dv := range devices {
		if dv.Owner != nil && *dv.Owner == person.ID {
			found = true
		}
	}
	if found {
		t.Error("deleted person still owns a device")
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
	if !strings.Contains(body, "۷ مگابایت") {
		t.Errorf("remaining should already be 7 MB (10 - 3), got:\n%s", body)
	}
}
