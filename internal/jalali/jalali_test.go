package jalali

import (
	"testing"
	"time"
)

var tehran = func() *time.Location {
	l, err := time.LoadLocation("Asia/Tehran")
	if err != nil {
		panic(err)
	}
	return l
}()

func TestBoundaryClampDoesNotDrift(t *testing.T) {
	anchor := Date{1405, 5, 31}
	// DESIGN.md's worked example says 1405/08/31, but Mehr..Bahman are 30 days,
	// so the anchor clamps to 30 in every month after Shahrivar.
	want := []Date{{1405, 5, 31}, {1405, 6, 31}, {1405, 7, 30}, {1405, 8, 30}, {1405, 9, 30}, {1405, 10, 30}}
	for k, w := range want {
		if got := FromTime(Boundary(anchor, k, tehran), tehran); got != w {
			t.Fatalf("k=%d got %v want %v", k, got, w)
		}
	}
}

func TestAnchorDayOne(t *testing.T) {
	anchor := Date{1405, 8, 1}
	for _, k := range []int{0, 1, 6, 12} {
		if got := FromTime(Boundary(anchor, k, tehran), tehran); got.D != 1 {
			t.Fatalf("k=%d day %d", k, got.D)
		}
	}
}

func TestEsfandLeapAndCommon(t *testing.T) {
	// 1403 is a leap Jalali year (Esfand 30), 1405 is not (Esfand 29).
	if n := DaysInMonth(1403, 12); n != 30 {
		t.Fatalf("1403/12 length %d", n)
	}
	if n := DaysInMonth(1405, 12); n != 29 {
		t.Fatalf("1405/12 length %d", n)
	}
	anchor := Date{1403, 12, 31}
	if got := FromTime(Boundary(anchor, 0, tehran), tehran); got != (Date{1403, 12, 30}) {
		t.Fatalf("leap esfand clamp got %v", got)
	}
	if got := FromTime(Boundary(anchor, 1, tehran), tehran); got != (Date{1404, 1, 31}) {
		t.Fatalf("nowruz boundary got %v", got)
	}
}

func TestWindowSpansNowruz(t *testing.T) {
	anchor := Date{1405, 12, 5}
	// 1405/12/5 .. 1406/1/5 crosses Nowruz.
	start := Date{1405, 12, 5}.Midnight(tehran)
	mid := Date{1405, 12, 20}.Midnight(tehran)
	s, e := Window(anchor, mid, tehran)
	if !s.Equal(start) {
		t.Fatalf("start %v want %v", s, start)
	}
	if !e.Equal(Date{1406, 1, 5}.Midnight(tehran)) {
		t.Fatalf("end %v", e)
	}
	if !mid.After(s) || !mid.Before(e) {
		t.Fatal("window must contain mid")
	}
}

func TestBoundaryRange(t *testing.T) {
	anchor := Date{1405, 8, 12}
	got := BoundaryRange(anchor, Date{1405, 7, 1}.Midnight(tehran), Date{1406, 2, 1}.Midnight(tehran), tehran)
	if len(got) != 7 {
		t.Fatalf("got %d boundaries: %v", len(got), got)
	}
	if FromTime(got[0], tehran) != (Date{1405, 7, 12}) {
		t.Fatalf("first %v", got[0])
	}
}

func TestBoundaryIsMidnightTehran(t *testing.T) {
	b := Boundary(Date{1405, 8, 12}, 1, tehran).UTC()
	if h := b.In(tehran).Hour(); h != 0 {
		t.Fatalf("boundary not at local midnight: %v", b.In(tehran))
	}
}
