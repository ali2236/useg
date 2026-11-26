package store

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestDomesticReplacePageAndSearch(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}

	var sites []DomesticSite
	for i := 0; i < 120; i++ {
		sites = append(sites, DomesticSite{
			Domain:  fmt.Sprintf("site%03d.ir", i),
			Network: fmt.Sprintf("10.0.%d.0/24", i),
			Updated: "1405/09/04",
			Due:     "1405/09/15",
		})
	}
	n, err := db.ReplaceDomestic(sites)
	if err != nil || n != 120 {
		t.Fatalf("replace: n=%d err=%v", n, err)
	}

	page, err := db.DomesticSites("", 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 120 || page.Pages != 3 || page.Page != 1 || len(page.Sites) != 50 {
		t.Fatalf("page 1: %+v", page)
	}
	if page.Sites[0].Domain != "site000.ir" {
		t.Errorf("not ordered by domain: %+v", page.Sites[0])
	}

	page, err = db.DomesticSites("", 99, 50)
	if err != nil {
		t.Fatal(err)
	}
	if page.Page != 3 || len(page.Sites) != 20 {
		t.Fatalf("page clamp: page=%d rows=%d", page.Page, len(page.Sites))
	}

	page, err = db.DomesticSites("site119", 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Sites) != 1 || page.Sites[0].Domain != "site119.ir" {
		t.Fatalf("search: %+v", page)
	}

	// A literal % must not turn into a wildcard.
	sites = []DomesticSite{{Domain: "100%off.ir", Network: "10.9.0.0/24"}}
	if _, err := db.ReplaceDomestic(sites); err != nil {
		t.Fatal(err)
	}
	page, err = db.DomesticSites("%", 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 {
		t.Fatalf("escaped search: %+v", page)
	}
}
