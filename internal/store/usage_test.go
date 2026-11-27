package store

import (
	"path/filepath"
	"testing"
)

func TestChargedBytesNeverUndercounts(t *testing.T) {
	for _, c := range []struct {
		charged float64
		want    int64
	}{
		{-1, 0},
		{0, 0},
		{1, 1},
		{1.0001, 2},
		{4e9 + 0.5, 4000000001},
	} {
		if got := ChargedBytes(c.charged); got != c.want {
			t.Errorf("ChargedBytes(%v) = %d, want %d", c.charged, got, c.want)
		}
	}
}

func TestDailyUsageAccumulatesPerDayAndSubject(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		day string
		row UsageRow
	}{
		{"2026-11-26", UsageRow{Subject: "person:1", Charged: 1.5e9}},
		{"2026-11-26", UsageRow{Subject: "dev:aa", Charged: 0.25e9}},
		{"2026-11-26", UsageRow{Subject: "person:1", Charged: 0.5e9}},
		{"2026-11-27", UsageRow{Subject: "person:1", Charged: 2e9}},
	} {
		if err := db.UpsertDailyUsage(c.day, []UsageRow{c.row}); err != nil {
			t.Fatal(err)
		}
	}
	totals, err := db.DailySubjectTotals("2026-11-26")
	if err != nil {
		t.Fatal(err)
	}
	if totals["person:1"] != 2e9 || totals["dev:aa"] != 0.25e9 {
		t.Fatalf("daily totals = %v", totals)
	}
	rows, err := db.DailyUsageSince("2026-11-26")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("history rows = %d, want 3", len(rows))
	}
	if rows[0].Day != "2026-11-26" || rows[2].Day != "2026-11-27" {
		t.Fatalf("history order = %+v", rows)
	}
}

func TestPersonDailyLimitRoundTrip(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	id, err := db.CreatePerson("ali", 1, "fa")
	if err != nil {
		t.Fatal(err)
	}
	limit := int64(2.5e9)
	if err := db.SetDailyLimit(id, &limit); err != nil {
		t.Fatal(err)
	}
	p, err := db.Person(id)
	if err != nil {
		t.Fatal(err)
	}
	if p.DailyLimit == nil || *p.DailyLimit != limit {
		t.Fatalf("daily limit = %v", p.DailyLimit)
	}
	if err := db.SetDailyLimit(id, nil); err != nil {
		t.Fatal(err)
	}
	if p, err = db.Person(id); err != nil || p.DailyLimit != nil {
		t.Fatalf("cleared daily limit = %v, %v", p.DailyLimit, err)
	}
}
