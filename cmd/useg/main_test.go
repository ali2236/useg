package main

import (
	"path/filepath"
	"testing"

	"useg/internal/store"
)

// A box installed by make install has people (added in the admin) but no routes
// until someone adds one, which is how its subscription ended up with nothing
// but the quota row. ensureBase must leave it with routes and listeners, and a
// restart must not duplicate them.
func TestEnsureBaseGivesAFreshBoxListeners(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreatePerson("ali", 1, "fa"); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		if err := ensureBase(db); err != nil {
			t.Fatal(err)
		}
	}

	routes, err := db.Routes()
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 2 {
		t.Fatalf("want the primary and upstream routes, got %d", len(routes))
	}
	creds, err := db.Credentials()
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 2 {
		t.Fatalf("want one credential per person and route, got %d", len(creds))
	}
}
