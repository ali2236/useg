package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"useg/internal/jalali"
	"useg/internal/store"
)

// A bound person sees their last seven days, can set their own daily cap, and
// the cap immediately cuts their devices off through the normal quota path.
func TestPortalDailyHistoryAndLimit(t *testing.T) {
	srv, db, cfg, person := tserver(t)
	now := time.Now()
	phone := "aa:bb:cc:00:00:03"
	if err := db.Bind(phone, person.ID, "test", now.Unix()); err != nil {
		t.Fatal(err)
	}
	loc := cfg.Location()
	today := now.In(loc).Format("2006-01-02")
	yesterday := now.In(loc).AddDate(0, 0, -1).Format("2006-01-02")
	if err := db.UpsertDailyUsage(today, []store.UsageRow{{
		Subject: "dev:" + phone, Charged: 0.5e9,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertDailyUsage(yesterday, []store.UsageRow{{
		Subject: "dev:" + phone, Charged: 1.2e9,
	}}); err != nil {
		t.Fatal(err)
	}

	h := srv.PortalHandler()
	rec := do(h, "POST", "/daily-limit", "10.66.0.3:5000", url.Values{"gb": {"0.4"}}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/?ok=daily" {
		t.Fatalf("set limit: code=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}
	p, err := db.Person(person.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.DailyLimit == nil || *p.DailyLimit != 0.4e9 {
		t.Fatalf("daily limit = %v", p.DailyLimit)
	}

	body := do(h, "GET", "/?ok=daily", "10.66.0.3:5000", nil, nil).Body.String()
	todayLabel := persianDigits(jalali.Format(now, "short", loc))
	yesterdayLabel := persianDigits(jalali.Format(now.AddDate(0, 0, -1), "short", loc))
	for _, want := range []string{"سقف روزانه ذخیره شد", "سقف مصرف روزانه", "۱٫۲ گیگابایت", "۵۰۰ مگابایت",
		`popovertarget="daily-limit-pop"`, todayLabel, yesterdayLabel} {
		if !strings.Contains(body, want) {
			t.Errorf("portal misses %q:\n%s", want, body)
		}
	}
	if daily := body[strings.Index(body, `popovertarget="daily-limit-pop"`):strings.Index(body, `id="daily-limit-pop"`)]; strings.Count(daily, `<li class="dev">`) != 2 {
		t.Errorf("daily list should hold only the two days with usage:\n%s", daily)
	}
	if strings.Index(body, `popovertarget="daily-limit-pop"`) < strings.Index(body, "دستگاه‌ها") {
		t.Error("the daily section should come after the devices section")
	}

	// Raising the cap clears the block at the next render.
	if rec := do(h, "POST", "/daily-limit", "10.66.0.3:5000", url.Values{"gb": {"2"}}, nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("raise limit: %d", rec.Code)
	}
	body = do(h, "GET", "/", "10.66.0.3:5000", nil, nil).Body.String()
	if strings.Contains(body, "سقف مصرف روزانه") {
		t.Errorf("raised daily limit still blocks:\n%s", body)
	}

	// A guest has no person and cannot set anyone's limit.
	rec = do(h, "POST", "/daily-limit", "10.66.0.99:5000", url.Values{"gb": {"9"}}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("guest set limit: %d", rec.Code)
	}
	if p, _ = db.Person(person.ID); p.DailyLimit == nil || *p.DailyLimit != 2e9 {
		t.Fatalf("guest changed a person's limit: %v", p.DailyLimit)
	}
}

func TestAdminSetsDailyLimit(t *testing.T) {
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
	rec := do(h, "POST", "/admin/person/daily-limit", "10.66.0.3:5000",
		url.Values{"person": {pid}, "gb": {"3.5"}}, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("admin set limit: %d", rec.Code)
	}
	if p, err := db.Person(person.ID); err != nil || p.DailyLimit == nil || *p.DailyLimit != 3.5e9 {
		t.Fatalf("daily limit = %v, %v", p.DailyLimit, err)
	}
	body := do(h, "GET", "/admin", "10.66.0.3:5000", nil, cookie).Body.String()
	if !strings.Contains(body, fmt.Sprintf(`value="%s"`, pid)) || !strings.Contains(body, "روزانه") {
		t.Errorf("admin people tab misses the daily limit control:\n%s", body)
	}

	// Empty clears it.
	do(h, "POST", "/admin/person/daily-limit", "10.66.0.3:5000",
		url.Values{"person": {pid}, "gb": {""}}, cookie)
	if p, _ := db.Person(person.ID); p.DailyLimit != nil {
		t.Fatalf("cleared daily limit = %v", p.DailyLimit)
	}
}
