package store

import (
	"path/filepath"
	"testing"
)

// TestMigrateFreshSchema guards the squashed 001 schema: the columns and
// tables the code reads must be there, and the retired ones must stay gone.
func TestMigrateFreshSchema(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}

	for _, q := range []string{
		`SELECT alloc_mode FROM period`,
		`SELECT period_id, person_id, bytes FROM allocation`,
		`SELECT weight FROM rate`,
		`SELECT domain, network FROM domestic_site`,
		`SELECT daily_limit FROM person`,
		`SELECT day, subject, charged FROM daily_usage`,
	} {
		rows, err := db.Query(q)
		if err != nil {
			t.Errorf("%s: %v", q, err)
			continue
		}
		rows.Close() // one connection only: hold it and the next query blocks
	}
	for _, q := range []string{
		`SELECT weight FROM route`,
		`SELECT 1 FROM class_pattern`,
		`SELECT 1 FROM class WHERE name = 'cdn'`,
	} {
		var one int
		if err := db.QueryRow(q).Scan(&one); err == nil {
			t.Errorf("%s: still present after squash", q)
		}
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM class WHERE name IN ('default', 'nimbaha')`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("class seeds = %d, %v", n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM setting WHERE k = 'credential_base_port'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("setting seeds = %d, %v", n, err)
	}
}
