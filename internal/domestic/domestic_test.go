package domestic

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const tokenPage = `<html><body>
<form action="/Page/GetAllIPListSearch" method="post">
<input name="__RequestVerificationToken" type="hidden" value="tok123" />
</form></body></html>`

const exportTable = `<div><table>
<tr><th>نشانی وب‌سایت</th><th>IPv4</th><th>IPNetwork</th><th>تاریخ بروزرسانی</th><th>حداکثر مهلت</th></tr>
<tr><td>http://Example.COM/lms46781</td><td>185.159.152.5/25</td><td>185.159.152.0/25</td><td>1405/09/04</td><td>1405/09/15</td></tr>
<tr><td>HTTPS://prom.tabanbin.ir:9090/*</td><td>2.144.1.2/14</td><td>2.144.0.0/14</td><td>1405/09/03</td><td>1405/09/15</td></tr>
<tr><td>https://صاد.com</td><td>95.38.195.197/32</td><td>95.38.195.197/32</td><td>1405/09/02</td><td>1405/09/15</td></tr>
<tr><td>http://dupe.ir</td><td>95.38.195.197/32</td><td>95.38.195.197/32</td><td>1405/09/02</td><td>1405/09/15</td></tr>
<tr><td>http://dupe.ir/</td><td>95.38.195.197/32</td><td>95.38.195.197/32</td><td>1405/09/02</td><td>1405/09/15</td></tr>
<tr><td>http://broken.ir</td><td>nope</td><td>nope</td><td>1405/09/02</td><td>1405/09/15</td></tr>
</table></div>`

func TestFetchParsesAndNormalizes(t *testing.T) {
	var gotToken, gotExport string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case searchPath:
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "1"})
			fmt.Fprint(w, tokenPage)
		case exportPath:
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse form: %v", err)
				return
			}
			gotToken = r.FormValue("__RequestVerificationToken")
			gotExport = r.FormValue("ExportExcel")
			if _, err := r.Cookie("sid"); err != nil {
				t.Errorf("export request lost the session cookie: %v", err)
			}
			fmt.Fprint(w, exportTable)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	sites, err := Fetcher{BaseURL: srv.URL, MinRows: 1}.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotToken != "tok123" || gotExport != "true" {
		t.Fatalf("export form: token=%q export=%q", gotToken, gotExport)
	}
	if len(sites) != 4 {
		t.Fatalf("want 4 unique rows, got %d: %+v", len(sites), sites)
	}
	byDomain := map[string]string{}
	for _, s := range sites {
		byDomain[s.Domain] = s.Network
	}
	for domain, network := range map[string]string{
		"example.com":      "185.159.152.0/25",
		"prom.tabanbin.ir": "2.144.0.0/14",
		"صاد.com":          "95.38.195.197/32",
		"dupe.ir":          "95.38.195.197/32",
	} {
		if byDomain[domain] != network {
			t.Errorf("%s: got %q, want %q", domain, byDomain[domain], network)
		}
	}
}

func TestFetchRefusesShortExport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == searchPath {
			fmt.Fprint(w, tokenPage)
			return
		}
		fmt.Fprint(w, exportTable)
	}))
	defer srv.Close()

	if _, err := (Fetcher{BaseURL: srv.URL, MinRows: 10}).Fetch(context.Background()); err == nil {
		t.Fatal("a short export must not be accepted")
	}
}

func TestFetchFailsWithoutToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html><body>maintenance</body></html>")
	}))
	defer srv.Close()

	_, err := (Fetcher{BaseURL: srv.URL, MinRows: 1}).Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("want a token error, got %v", err)
	}
}
