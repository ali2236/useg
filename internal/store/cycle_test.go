package store

import (
	"path/filepath"
	"testing"
	"time"

	"useg/internal/jalali"
)

const testGB = int64(1e9)

func cycleDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"pool_charged_gb": "10", "guest_pool_gb": "2", "raw_ceiling_gb": "",
	} {
		if err := db.SetSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestStartCycleAndRollover(t *testing.T) {
	db := cycleDB(t)
	loc := time.FixedZone("IRST", 3*3600+1800)
	now := time.Date(2026, 11, 26, 20, 0, 0, 0, loc)

	first, err := db.StartCycle(now, 30, "days", 10*testGB, loc)
	if err != nil {
		t.Fatal(err)
	}
	if first.AllocMode != "manual" {
		t.Fatalf("mode = %q, want manual", first.AllocMode)
	}
	if first.EndUTC-first.StartUTC != 30*86400 {
		t.Fatalf("cycle = %d seconds, want 30 days", first.EndUTC-first.StartUTC)
	}
	if first.PoolCharged == nil || *first.PoolCharged != 10*testGB {
		t.Fatalf("pool = %v", first.PoolCharged)
	}
	if first.GuestPool == nil || *first.GuestPool != 0 {
		t.Fatalf("guest pool = %v, want 0", first.GuestPool)
	}

	if total, err := db.AddPool(first.ID, 5*testGB); err != nil || total != 15*testGB {
		t.Fatalf("AddPool = %d, %v", total, err)
	}
	if err := db.ApplyAllocation(first.ID, map[int64]int64{1: 4 * testGB}, 2*testGB); err != nil {
		t.Fatal(err)
	}
	allocs, err := db.Allocations(first.ID)
	if err != nil || allocs[1] != 4*testGB {
		t.Fatalf("allocations = %v, %v", allocs, err)
	}
	cur, _ := db.CurrentPeriod(now.Unix())
	if cur.GuestPool == nil || *cur.GuestPool != 2*testGB {
		t.Fatalf("guest allocation = %v", cur.GuestPool)
	}

	// The next 30-day window opens unattended and starts unallocated.
	later := now.Add(31 * 24 * time.Hour)
	next, err := db.EnsurePeriod(later, loc)
	if err != nil {
		t.Fatal(err)
	}
	if next.ID == first.ID || next.StartUTC != now.Add(30*24*time.Hour).Unix() {
		t.Fatalf("rollover period = %+v", next)
	}
	if next.EndUTC-next.StartUTC != 30*86400 || next.AllocMode != "manual" {
		t.Fatalf("rollover window = %+v", next)
	}
	if next.PoolCharged == nil || *next.PoolCharged != 15*testGB {
		t.Fatalf("rollover pool = %v", next.PoolCharged)
	}
	if next.GuestPool == nil || *next.GuestPool != 0 {
		t.Fatalf("rollover guest = %v, want unallocated", next.GuestPool)
	}
	if allocs, _ := db.Allocations(next.ID); len(allocs) != 0 {
		t.Fatalf("rollover allocations = %v, want none", allocs)
	}
}

func TestMonthCycleClampsAndKeepsClock(t *testing.T) {
	db := cycleDB(t)
	loc := time.FixedZone("IRST", 3*3600+1800)
	// Anchor on 31 Shahrivar; 3 months later is 30 Azar, not 31.
	start := jalali.Date{Y: 1405, M: 6, D: 31}.Midnight(loc).Add(20 * time.Hour)

	p, err := db.StartCycle(start, 3, "months", 10*testGB, loc)
	if err != nil {
		t.Fatal(err)
	}
	end := time.Unix(p.EndUTC, 0).In(loc)
	if got := jalali.FromTime(end, loc); got != (jalali.Date{Y: 1405, M: 9, D: 30}) {
		t.Fatalf("end = %v, want 1405-09-30", got)
	}
	if end.Hour() != 20 {
		t.Fatalf("end clock = %d:00, want 20:00", end.Hour())
	}

	// Reopening inside the same window is idempotent.
	again, err := db.EnsurePeriod(start.Add(time.Hour), loc)
	if err != nil || again.ID != p.ID {
		t.Fatalf("EnsurePeriod = %+v, %v", again, err)
	}
}

func TestStartCycleRejectsBadInput(t *testing.T) {
	db := cycleDB(t)
	loc := time.UTC
	now := time.Now()
	for _, c := range []struct {
		length int
		unit   string
		pool   int64
	}{
		{0, "days", testGB},
		{30, "weeks", testGB},
		{30, "days", 0},
	} {
		if _, err := db.StartCycle(now, c.length, c.unit, c.pool, loc); err == nil {
			t.Fatalf("StartCycle(%+v) should fail", c)
		}
	}
}
