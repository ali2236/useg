// Package jalali owns every Jalali/UTC conversion. Period boundaries are
// always computed from the anchor, never chained from a previous (clamped)
// boundary, so anchor day 31 does not drift down after a 30-day month.
package jalali

import (
	"fmt"
	"time"

	j "github.com/jalaali/go-jalaali"
)

type Date struct {
	Y, M, D int
}

func (d Date) String() string { return fmt.Sprintf("%04d-%02d-%02d", d.Y, d.M, d.D) }

func FromTime(t time.Time, loc *time.Location) Date {
	t = t.In(loc)
	y, m, d, err := j.ToJalaali(t.Year(), t.Month(), t.Day())
	if err != nil {
		panic(err) // only for years outside 1..9999, impossible from time.Time
	}
	return Date{y, int(m), d}
}

// Midnight returns the first instant of this Jalali day in loc.
func (d Date) Midnight(loc *time.Location) time.Time {
	gy, gm, gd, err := j.ToGregorian(d.Y, j.Month(d.M), d.D)
	if err != nil {
		panic(err)
	}
	return time.Date(gy, gm, gd, 0, 0, 0, 0, loc)
}

func DaysInMonth(y, m int) int {
	if n, err := j.MonthLength(y, m); err == nil {
		return n
	}
	if m <= 6 {
		return 31
	}
	if m <= 11 {
		return 30
	}
	if leap, _ := j.IsLeapYear(y); leap {
		return 30
	}
	return 29
}

func floorDiv(a, b int) int {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// Boundary is the start instant of the period k months after the anchor month.
// The anchor day is clamped per month and never remembered.
func Boundary(anchor Date, k int, loc *time.Location) time.Time {
	mm := anchor.M + k
	q := floorDiv(mm-1, 12)
	m := mm - 12*q
	return Date{anchor.Y + q, m, min(anchor.D, DaysInMonth(anchor.Y+q, m))}.Midnight(loc)
}

// BoundaryRange returns every boundary instant in [from, to).
func BoundaryRange(anchor Date, from, to time.Time, loc *time.Location) []time.Time {
	var out []time.Time
	// Walk from an anchor near `from` instead of scanning centuries.
	k := 12*(FromTime(from, loc).Y-anchor.Y) + FromTime(from, loc).M - anchor.M
	for Boundary(anchor, k, loc).After(from) {
		k--
	}
	for {
		b := Boundary(anchor, k, loc)
		if !b.Before(to) {
			return out
		}
		if !b.Before(from) {
			out = append(out, b)
		}
		k++
	}
}

// Window returns the [start, end) instants of the period containing t.
func Window(anchor Date, t time.Time, loc *time.Location) (time.Time, time.Time) {
	k := 12*(FromTime(t, loc).Y-anchor.Y) + FromTime(t, loc).M - anchor.M
	for {
		if !Boundary(anchor, k, loc).After(t) {
			if Boundary(anchor, k+1, loc).After(t) {
				return Boundary(anchor, k, loc), Boundary(anchor, k+1, loc)
			}
			k++
			continue
		}
		k--
	}
}

var months = [][2]string{
	{},
	{"فروردین", "Farvardin"},
	{"اردیبهشت", "Ordibehesht"},
	{"خرداد", "Khordad"},
	{"تیر", "Tir"},
	{"مرداد", "Mordad"},
	{"شهریور", "Shahrivar"},
	{"مهر", "Mehr"},
	{"آبان", "Aban"},
	{"آذر", "Azar"},
	{"دی", "Dey"},
	{"بهمن", "Bahman"},
	{"اسفند", "Esfand"},
}

func MonthName(m int, lang string) string {
	if m < 1 || m > 12 {
		return ""
	}
	if lang == "en" {
		return months[m][1]
	}
	return months[m][0]
}

// Format renders a UTC instant as a Jalali date. Styles: "short" (12 Azar),
// "long" (12 Azar 1405), "numeric" (1405/12/12), "month-day" (12/12),
// "numeric-time" (12/12-14:02).
func Format(t time.Time, style string, loc *time.Location) string {
	d := FromTime(t, loc)
	switch style {
	case "short":
		return fmt.Sprintf("%d %s", d.D, MonthName(d.M, "fa"))
	case "long":
		return fmt.Sprintf("%d %s %d", d.D, MonthName(d.M, "fa"), d.Y)
	case "month-day":
		return fmt.Sprintf("%02d/%02d", d.M, d.D)
	case "numeric-time":
		lt := t.In(loc)
		return fmt.Sprintf("%02d/%02d-%02d:%02d", d.M, d.D, lt.Hour(), lt.Minute())
	default:
		return fmt.Sprintf("%d/%02d/%02d", d.Y, d.M, d.D)
	}
}
