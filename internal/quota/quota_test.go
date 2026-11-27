package quota

import (
	"testing"

	"useg/internal/store"
)

const gb = int64(1e9)

func ptr[T any](v T) *T { return &v }

func base() Input {
	return Input{
		Persons: []store.Person{
			{ID: 1, Name: "ali", Weight: 0.5, Enabled: true},
			{ID: 2, Name: "sara", Weight: 0.5, Enabled: true},
		},
		Devices: []store.Device{
			{MAC: "aa:aa:aa:aa:aa:aa", IP: "192.168.1.10", Owner: ptr(int64(1))},
			{MAC: "bb:bb:bb:bb:bb:bb", IP: "192.168.1.11"},
		},
		Totals:           map[string]store.Totals{},
		Daily:            map[string]float64{},
		Blocks:           map[string]store.Block{},
		PoolCharged:      ptr(100 * gb),
		GuestPool:        5 * gb,
		GuestDeviceBytes: 1 * gb,
		Now:              1000,
	}
}

func TestShareOfPool(t *testing.T) {
	in := base()
	dec := Compute(in)
	st := dec.Subjects[PersonSubject(1)]
	if st.Cap != 47_500_000_000 {
		t.Fatalf("cap = %d, want 47.5GB (weight x (pool - guest_pool))", st.Cap)
	}
	if st.Blocked {
		t.Fatal("nobody over cap yet")
	}
}

func TestOverCapBlocksAllPersonDevices(t *testing.T) {
	in := base()
	in.Totals["dev:aa:aa:aa:aa:aa:aa"] = store.Totals{Charged: 48 * float64(gb)}
	dec := Compute(in)
	if !dec.Subjects[PersonSubject(1)].Blocked {
		t.Fatal("person should be blocked")
	}
	if dec.Blocked["aa:aa:aa:aa:aa:aa"] == "" {
		t.Fatal("the person's device must be blocked too")
	}
	if dec.Blocked["bb:bb:bb:bb:bb:bb"] != "" {
		t.Fatal("unrelated guest must not be blocked")
	}
}

func TestCapOverride(t *testing.T) {
	in := base()
	in.Persons[0].CapOverride = ptr(10 * gb)
	in.Totals["dev:aa:aa:aa:aa:aa:aa"] = store.Totals{Charged: 11 * float64(gb)}
	dec := Compute(in)
	if dec.Subjects[PersonSubject(1)].Cap != 10*gb {
		t.Fatalf("cap = %d", dec.Subjects[PersonSubject(1)].Cap)
	}
	if dec.Blocked["aa:aa:aa:aa:aa:aa"] != ReasonQuota {
		t.Fatal("cap override must still block")
	}
}

func TestGuestAllowancePerDevice(t *testing.T) {
	in := base()
	in.Totals["dev:bb:bb:bb:bb:bb:bb"] = store.Totals{Charged: float64(gb)}
	dec := Compute(in)
	if dec.Blocked["bb:bb:bb:bb:bb:bb"] != ReasonGuest {
		t.Fatalf("guest reason = %q, want %q", dec.Blocked["bb:bb:bb:bb:bb:bb"], ReasonGuest)
	}
}

func TestGuestUnlimitedCap(t *testing.T) {
	in := base()
	in.GuestDeviceBytes = -1
	in.GuestPool = 0
	in.Totals["dev:bb:bb:bb:bb:bb:bb"] = store.Totals{Charged: 5000 * float64(gb)}
	dec := Compute(in)
	if dec.Blocked["bb:bb:bb:bb:bb:bb"] != "" {
		t.Fatalf("guest with an unlimited cap was blocked: %q", dec.Blocked["bb:bb:bb:bb:bb:bb"])
	}
	if dec.Subjects[DeviceSubject("bb:bb:bb:bb:bb:bb")].Cap != -1 {
		t.Fatal("unlimited guest should report cap -1")
	}
}

func TestGuestPoolIsHardCeiling(t *testing.T) {
	in := base()
	// One guest has eaten the whole pool; a fresh guest gets nothing.
	in.Totals["dev:cc:cc:cc:cc:cc:cc"] = store.Totals{Charged: 5 * float64(gb)}
	in.Devices = append(in.Devices, store.Device{MAC: "cc:cc:cc:cc:cc:cc", IP: "192.168.1.12"})
	in.Devices = append(in.Devices, store.Device{MAC: "dd:dd:dd:dd:dd:dd", IP: "192.168.1.13"})
	dec := Compute(in)
	if dec.Blocked["dd:dd:dd:dd:dd:dd"] != ReasonGuestPool {
		t.Fatalf("fresh guest reason = %q, want %q", dec.Blocked["dd:dd:dd:dd:dd:dd"], ReasonGuestPool)
	}
	if st := dec.Subjects[DeviceSubject("dd:dd:dd:dd:dd:dd")]; st.Cap != 0 {
		t.Fatalf("fresh guest cap = %d, want 0 with the pool exhausted", st.Cap)
	}
}

func TestGuestCapShrinksWithSharedPool(t *testing.T) {
	in := base()
	in.GuestPool = 1 * gb
	in.Devices = append(in.Devices, store.Device{MAC: "cc:cc:cc:cc:cc:cc", IP: "192.168.1.12"})
	in.Totals["dev:cc:cc:cc:cc:cc:cc"] = store.Totals{Charged: 0.5 * float64(gb)}
	dec := Compute(in)
	// Half the pool is gone: a fresh guest can only see the other half.
	if st := dec.Subjects[DeviceSubject("bb:bb:bb:bb:bb:bb")]; st.Cap != 500_000_000 {
		t.Fatalf("fresh guest cap = %d, want the 500MB pool headroom", st.Cap)
	}
	// The spending guest keeps its allowance but has the same 500MB left.
	if st := dec.Subjects[DeviceSubject("cc:cc:cc:cc:cc:cc")]; st.Cap != 1*gb || st.Used != 500_000_000 {
		t.Fatalf("spending guest = %+v, want a 1GB cap with 500MB used", st)
	}
	if dec.Blocked["bb:bb:bb:bb:bb:bb"] != "" {
		t.Fatalf("fresh guest blocked with pool left: %q", dec.Blocked["bb:bb:bb:bb:bb:bb"])
	}
}

func TestHouseholdCeilingBlocksEverybody(t *testing.T) {
	in := base()
	in.RawCeiling = ptr(100 * gb)
	in.RawUsed = 101 * gb
	dec := Compute(in)
	if dec.Blocked["aa:aa:aa:aa:aa:aa"] != ReasonHousehold {
		t.Fatal("bound device must be blocked by the household ceiling")
	}
	if dec.Blocked["bb:bb:bb:bb:bb:bb"] != ReasonHousehold {
		t.Fatal("guest must be blocked by the household ceiling")
	}
}

func TestManualBlockAndExpiry(t *testing.T) {
	in := base()
	in.Blocks[DeviceSubject("bb:bb:bb:bb:bb:bb")] = store.Block{Subject: DeviceSubject("bb:bb:bb:bb:bb:bb"), Until: ptr(int64(2000))}
	dec := Compute(in)
	if dec.Blocked["bb:bb:bb:bb:bb:bb"] != ReasonManual {
		t.Fatal("manual block not applied")
	}
	in.Now = 2001
	if Compute(in).Blocked["bb:bb:bb:bb:bb:bb"] != "" {
		t.Fatal("expired block still applied")
	}
}

func TestUnmeteredPoolNeverBlocksPeople(t *testing.T) {
	in := base()
	in.PoolCharged = nil
	in.Totals["dev:aa:aa:aa:aa:aa:aa"] = store.Totals{Charged: 5000 * float64(gb)}
	dec := Compute(in)
	if dec.Subjects[PersonSubject(1)].Blocked {
		t.Fatal("pool_charged=null must meter and display but never block")
	}
	if dec.Subjects[PersonSubject(1)].Cap != -1 {
		t.Fatalf("cap = %d, want -1 (unmetered)", dec.Subjects[PersonSubject(1)].Cap)
	}
}

func TestManualAllocationsDriveCaps(t *testing.T) {
	in := base()
	in.Manual = true
	in.PoolCharged = ptr(20 * gb)
	in.Allocations = map[int64]int64{1: 10 * gb}
	in.GuestPool = 0
	dec := Compute(in)
	if dec.Subjects[PersonSubject(1)].Cap != 10*gb {
		t.Fatalf("cap = %d, want the explicit allocation", dec.Subjects[PersonSubject(1)].Cap)
	}
	if !dec.Subjects[PersonSubject(2)].Blocked {
		t.Fatal("a person with no allocation must stay blocked")
	}
	// With a zero guest share, guests have no data at all.
	if dec.Blocked["bb:bb:bb:bb:bb:bb"] != ReasonGuestPool {
		t.Fatalf("guest reason = %q, want %q", dec.Blocked["bb:bb:bb:bb:bb:bb"], ReasonGuestPool)
	}
	in.GuestPool = 1 * gb
	dec = Compute(in)
	if dec.Blocked["bb:bb:bb:bb:bb:bb"] != "" {
		t.Fatal("a funded guest pool should unblock the guest")
	}
}

func TestTransfersMoveShareBetweenPeople(t *testing.T) {
	in := base()
	in.TransferNet = map[int64]int64{1: -gb, 2: gb}
	dec := Compute(in)
	if got := dec.Subjects[PersonSubject(1)].Cap; got != 46_500_000_000 {
		t.Fatalf("sender cap = %d, want 46.5GB", got)
	}
	if got := dec.Subjects[PersonSubject(2)].Cap; got != 48_500_000_000 {
		t.Fatalf("receiver cap = %d, want 48.5GB", got)
	}

	// The trimmed share can now block a sender who is still under the old cap.
	in.Totals["dev:aa:aa:aa:aa:aa:aa"] = store.Totals{Charged: 47 * float64(gb)}
	dec = Compute(in)
	if !dec.Subjects[PersonSubject(1)].Blocked {
		t.Fatal("sender must be blocked after giving its headroom away")
	}
}

func TestTransfersIgnoredWhileUnmetered(t *testing.T) {
	in := base()
	in.PoolCharged = nil
	in.TransferNet = map[int64]int64{1: -gb, 2: gb}
	dec := Compute(in)
	if got := dec.Subjects[PersonSubject(1)].Cap; got != -1 {
		t.Fatalf("unmetered sender cap = %d, want -1", got)
	}
}

func TestSplit(t *testing.T) {
	cases := []struct {
		total   int64
		weights []float64
		want    []int64
	}{
		{100, []float64{1, 1, 1}, []int64{34, 33, 33}},
		{100, []float64{1, 0, 1}, []int64{50, 0, 50}},
		{7, []float64{2, 1}, []int64{5, 2}},
		{0, []float64{1, 1}, []int64{0, 0}},
		{100, []float64{0, 0}, []int64{0, 0}},
	}
	for _, c := range cases {
		got := Split(c.total, c.weights)
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("Split(%d, %v) = %v, want %v", c.total, c.weights, got, c.want)
			}
		}
	}
}

func TestUsageFollowsThePersonAcrossDevices(t *testing.T) {
	in := base()
	in.Devices = append(in.Devices, store.Device{MAC: "ee:ee:ee:ee:ee:ee", IP: "192.168.1.14", Owner: ptr(int64(1))})
	in.Totals["dev:aa:aa:aa:aa:aa:aa"] = store.Totals{Charged: 30 * float64(gb)}
	in.Totals["dev:ee:ee:ee:ee:ee:ee"] = store.Totals{Charged: 30 * float64(gb)}
	dec := Compute(in)
	st := dec.Subjects[PersonSubject(1)]
	if st.Used != 60*gb {
		t.Fatalf("used = %d, want 60GB across both devices", st.Used)
	}
	if !st.Blocked {
		t.Fatal("five devices must buy no extra bytes")
	}
}

func TestDailyLimitBlocksPersonOnly(t *testing.T) {
	in := base()
	in.Persons[0].DailyLimit = ptr(2 * gb)
	in.Daily["dev:aa:aa:aa:aa:aa:aa"] = 2.5 * float64(gb)
	dec := Compute(in)
	st := dec.Subjects[PersonSubject(1)]
	if !st.Blocked || st.Reason != ReasonDaily {
		t.Fatalf("person status = %+v, want blocked by the daily limit", st)
	}
	if st.DailyUsed != int64(2.5e9) || st.DailyCap != 2*gb {
		t.Fatalf("daily used/cap = %d/%d", st.DailyUsed, st.DailyCap)
	}
	if dec.Blocked["aa:aa:aa:aa:aa:aa"] != ReasonDaily {
		t.Fatal("the person's device must be cut off")
	}
	if dec.Blocked["bb:bb:bb:bb:bb:bb"] != "" {
		t.Fatal("a guest must never inherit a person's daily limit")
	}
}

func TestDailyLimitUnderCapStaysOnline(t *testing.T) {
	in := base()
	in.Persons[0].DailyLimit = ptr(2 * gb)
	in.Daily["person:1"] = 1 * float64(gb)
	dec := Compute(in)
	if st := dec.Subjects[PersonSubject(1)]; st.Blocked {
		t.Fatalf("person blocked under the daily cap: %+v", st)
	}
}

func TestCapsOffIgnoresDailyLimit(t *testing.T) {
	in := base()
	in.CapsOff = true
	in.Persons[0].DailyLimit = ptr(1 * gb)
	in.Daily["person:1"] = 9 * float64(gb)
	dec := Compute(in)
	if dec.Subjects[PersonSubject(1)].Blocked {
		t.Fatal("caps_off must disable daily limits too")
	}
}

func TestMergePendingFeedsTodaysDailyCounter(t *testing.T) {
	in := base()
	MergePending(&in, map[string]store.Totals{
		"dev:aa:aa:aa:aa:aa:aa": {Charged: 0.5 * float64(gb), Raw: 1},
	})
	if in.Daily["dev:aa:aa:aa:aa:aa:aa"] != 0.5*float64(gb) {
		t.Fatalf("daily pending = %v", in.Daily)
	}
	if in.Totals["dev:aa:aa:aa:aa:aa:aa"].Charged != 0.5*float64(gb) {
		t.Fatal("monthly pending was not merged")
	}
}
