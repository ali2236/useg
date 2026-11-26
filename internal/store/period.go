package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"useg/internal/jalali"
)

type Period struct {
	ID          int64
	StartUTC    int64
	EndUTC      int64
	AnchorDay   int
	PoolCharged *int64
	GuestPool   *int64
	RawCeiling  *int64
	ClosedAt    *int64
	AllocMode   string // "weight" (Jalali months) or "manual" (data cycle)
}

const periodCols = `id, start_utc, end_utc, anchor_jalali_day,
	pool_charged, guest_pool, raw_ceiling, closed_at, alloc_mode`

func scanPeriod(sc interface{ Scan(...any) error }) (Period, error) {
	var p Period
	var pool, guest, ceil, closed sql.NullInt64
	err := sc.Scan(&p.ID, &p.StartUTC, &p.EndUTC, &p.AnchorDay, &pool, &guest, &ceil, &closed, &p.AllocMode)
	if pool.Valid {
		p.PoolCharged = &pool.Int64
	}
	if guest.Valid {
		p.GuestPool = &guest.Int64
	}
	if ceil.Valid {
		p.RawCeiling = &ceil.Int64
	}
	if closed.Valid {
		p.ClosedAt = &closed.Int64
	}
	return p, err
}

func (d *DB) CurrentPeriod(now int64) (*Period, error) {
	p, err := scanPeriod(d.QueryRow(`SELECT `+periodCols+` FROM period
		WHERE start_utc <= ? AND end_utc > ? AND closed_at IS NULL
		ORDER BY start_utc DESC LIMIT 1`, now, now))
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (d *DB) Periods() ([]Period, error) {
	rows, err := d.Query(`SELECT ` + periodCols + ` FROM period ORDER BY start_utc DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Period
	for rows.Next() {
		p, err := scanPeriod(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (d *DB) Anchor(loc *time.Location) (jalali.Date, error) {
	v, err := d.GetSetting("anchor_start_jalali")
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return jalali.Date{}, err
	}
	if v == "" {
		today := jalali.FromTime(time.Now(), loc)
		if err := d.SetSetting("anchor_start_jalali", today.String()); err != nil {
			return jalali.Date{}, err
		}
		return today, nil
	}
	var y, m, day int
	if _, err := fmt.Sscanf(v, "%d-%d-%d", &y, &m, &day); err != nil {
		return jalali.Date{}, fmt.Errorf("anchor_start_jalali %q: %w", v, err)
	}
	return jalali.Date{Y: y, M: m, D: day}, nil
}

func (d *DB) SetAnchor(date jalali.Date) error {
	return d.SetSetting("anchor_start_jalali", date.String())
}

// quotaSettings snapshots the admin's knobs into bytes for one period.
func (d *DB) quotaSettings() (pool, guest, ceiling *int64, err error) {
	s, err := d.Settings()
	if err != nil {
		return nil, nil, nil, err
	}
	gb := func(key string) *int64 {
		v, ok := s[key]
		if !ok || v == "" {
			return nil
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil
		}
		b := int64(f * 1e9) // decimal GB, matching how the ISP quotes allowances
		return &b
	}
	// The ceiling mirrors the pool: it trips on raw bytes if metering ever
	// diverges from the charged ledger.
	pool = gb("pool_charged_gb")
	return pool, gb("guest_pool_gb"), pool, nil
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func insertPeriod(ex execer, start, end int64, anchorDay int,
	pool, guest, ceiling *int64, mode string) error {
	_, err := ex.Exec(`INSERT INTO period(start_utc, end_utc, anchor_jalali_day,
			pool_charged, guest_pool, raw_ceiling, alloc_mode)
		VALUES(?, ?, ?, ?, ?, ?, ?) ON CONFLICT(start_utc) DO NOTHING`,
		start, end, anchorDay, pool, guest, ceiling, mode)
	return err
}

const (
	cycleStartKey  = "cycle_start_utc"
	cycleLengthKey = "cycle_length"
	cycleUnitKey   = "cycle_unit"
)

// Cycle is the admin-chosen fixed-length data cycle. Starts as days or Jalali
// calendar months, so "3 months" is not the same as "90 days".
type Cycle struct {
	Start  int64
	Length int
	Unit   string
}

// Cycle reports the running fixed-length cycle; ok is false while the install
// still uses Jalali months.
func (d *DB) Cycle() (Cycle, bool, error) {
	v, err := d.GetSetting(cycleStartKey)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Cycle{}, false, err
	}
	if strings.TrimSpace(v) == "" {
		return Cycle{}, false, nil
	}
	start, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return Cycle{}, false, nil // corrupt setting: keep the monthly cycle
	}
	c := Cycle{Start: start, Length: 30, Unit: "days"}
	if s, e := d.GetSetting(cycleLengthKey); e == nil && s != "" {
		if n, e := strconv.Atoi(s); e == nil && n > 0 {
			c.Length = n
		}
	}
	if s, e := d.GetSetting(cycleUnitKey); e == nil && (s == "days" || s == "months") {
		c.Unit = s
	}
	return c, true, nil
}

// cycleWindow returns the cycle containing now. Day cycles keep the exact
// instant; month cycles use Jalali calendar months and keep the reset's clock
// time, so anchor day 31 clamps only in short months.
func cycleWindow(c Cycle, now time.Time, loc *time.Location) (time.Time, time.Time) {
	if c.Unit == "months" {
		st := time.Unix(c.Start, 0).In(loc)
		anchor := jalali.FromTime(st, loc)
		clock := time.Duration(st.Hour())*time.Hour +
			time.Duration(st.Minute())*time.Minute + time.Duration(st.Second())*time.Second
		boundary := func(k int) time.Time {
			return jalali.Boundary(anchor, k*c.Length, loc).Add(clock)
		}
		if now.Before(st) {
			return boundary(0), boundary(1)
		}
		n := jalali.FromTime(now, loc)
		k := ((n.Y-anchor.Y)*12 + n.M - anchor.M) / c.Length
		if k < 0 {
			k = 0
		}
		for boundary(k).After(now) {
			k--
		}
		for !boundary(k + 1).After(now) {
			k++
		}
		return boundary(k), boundary(k + 1)
	}
	dur := int64(c.Length) * 86400
	if now.Unix() < c.Start {
		return time.Unix(c.Start, 0).In(loc), time.Unix(c.Start+dur, 0).In(loc)
	}
	k := (now.Unix() - c.Start) / dur
	start := c.Start + k*dur
	return time.Unix(start, 0).In(loc), time.Unix(start+dur, 0).In(loc)
}

func gbString(bytes int64) string {
	return strconv.FormatFloat(float64(bytes)/1e9, 'f', -1, 64)
}

// EnsurePeriod opens the period containing now. Idempotent: the UNIQUE
// start_utc makes a concurrent or repeated call harmless.
func (d *DB) EnsurePeriod(now time.Time, loc *time.Location) (*Period, error) {
	if p, err := d.CurrentPeriod(now.Unix()); err == nil {
		return p, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	pool, guest, ceiling, err := d.quotaSettings()
	if err != nil {
		return nil, err
	}
	if c, ok, err := d.Cycle(); err != nil {
		return nil, err
	} else if ok {
		start, end := cycleWindow(c, now, loc)
		anchor := jalali.FromTime(start, loc)
		// Every cycle opens unallocated: the admin allocates from the Data tab.
		if err := insertPeriod(d, start.Unix(), end.Unix(), anchor.D, pool, guest, ceiling, "manual"); err != nil {
			return nil, err
		}
		return d.CurrentPeriod(now.Unix())
	}
	anchor, err := d.Anchor(loc)
	if err != nil {
		return nil, err
	}
	start, end := jalali.Window(anchor, now, loc)
	if err := insertPeriod(d, start.Unix(), end.Unix(), anchor.D, pool, guest, ceiling, "weight"); err != nil {
		return nil, err
	}
	return d.CurrentPeriod(now.Unix())
}

// StartCycle closes the running period and opens a fresh data cycle with a
// clean, fully unallocated pool. Old usage keeps its old period_id.
func (d *DB) StartCycle(now time.Time, length int, unit string, pool int64, loc *time.Location) (*Period, error) {
	if length <= 0 {
		return nil, fmt.Errorf("cycle length must be positive")
	}
	if unit != "days" && unit != "months" {
		return nil, fmt.Errorf("cycle unit must be days or months")
	}
	if pool <= 0 {
		return nil, fmt.Errorf("data amount must be positive")
	}
	start := now.Unix()
	c := Cycle{Start: start, Length: length, Unit: unit}
	_, end := cycleWindow(c, now, loc)
	anchor := jalali.FromTime(now, loc)
	err := d.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE period SET closed_at = ?, end_utc = ?
			WHERE start_utc < ? AND end_utc > ? AND closed_at IS NULL`,
			start, start, start, start); err != nil {
			return err
		}
		for k, v := range map[string]string{
			cycleStartKey:     strconv.FormatInt(start, 10),
			cycleLengthKey:    strconv.Itoa(length),
			cycleUnitKey:      unit,
			"pool_charged_gb": gbString(pool),
			"guest_pool_gb":   "0",
		} {
			if _, err := tx.Exec(`INSERT INTO setting(k, v) VALUES(?, ?)
				ON CONFLICT(k) DO UPDATE SET v = excluded.v`, k, v); err != nil {
				return err
			}
		}
		guest := int64(0)
		return insertPeriod(tx, start, end.Unix(), anchor.D, &pool, &guest, &pool, "manual")
	})
	if err != nil {
		return nil, err
	}
	return d.CurrentPeriod(now.Unix())
}

// ResetPeriod closes the running period at now and opens a fresh one from the
// new anchor. It also leaves fixed-length cycle mode.
func (d *DB) ResetPeriod(anchor jalali.Date, now time.Time, loc *time.Location) (*Period, error) {
	if err := d.SetSetting(cycleStartKey, ""); err != nil {
		return nil, err
	}
	if err := d.SetAnchor(anchor); err != nil {
		return nil, err
	}
	if _, err := d.Exec(`UPDATE period SET closed_at = ?, end_utc = ?
		WHERE start_utc < ? AND end_utc > ? AND closed_at IS NULL`,
		now.Unix(), now.Unix(), now.Unix(), now.Unix()); err != nil {
		return nil, err
	}
	pool, guest, ceiling, err := d.quotaSettings()
	if err != nil {
		return nil, err
	}
	next := jalali.Boundary(anchor, 0, loc)
	for k := 0; !next.After(now); k++ {
		next = jalali.Boundary(anchor, k+1, loc)
	}
	if err := insertPeriod(d, now.Unix(), next.Unix(), anchor.D, pool, guest, ceiling, "weight"); err != nil {
		return nil, err
	}
	return d.CurrentPeriod(now.Unix())
}

// AddPool puts more data into a manual cycle's unallocated pool. The setting is
// updated too, so the next automatic cycle starts with the same N.
func (d *DB) AddPool(periodID, bytes int64) (int64, error) {
	if bytes <= 0 {
		return 0, fmt.Errorf("data amount must be positive")
	}
	res, err := d.Exec(`UPDATE period SET pool_charged = COALESCE(pool_charged, 0) + ?,
		raw_ceiling = COALESCE(pool_charged, 0) + ?
		WHERE id = ? AND alloc_mode = 'manual'`, bytes, bytes, periodID)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, fmt.Errorf("start a data cycle first")
	}
	var total sql.NullInt64
	if err := d.QueryRow(`SELECT pool_charged FROM period WHERE id = ?`, periodID).Scan(&total); err != nil {
		return 0, err
	}
	if err := d.SetSetting("pool_charged_gb", gbString(total.Int64)); err != nil {
		return 0, err
	}
	return total.Int64, nil
}

// Allocations returns person_id -> bytes for one period.
func (d *DB) Allocations(periodID int64) (map[int64]int64, error) {
	rows, err := d.Query(`SELECT person_id, bytes FROM allocation WHERE period_id = ?`, periodID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var id, b int64
		if err := rows.Scan(&id, &b); err != nil {
			return nil, err
		}
		out[id] = b
	}
	return out, rows.Err()
}

// ApplyAllocation tops up person shares and the guest share in one transaction.
func (d *DB) ApplyAllocation(periodID int64, persons map[int64]int64, guest int64) error {
	return d.tx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE period SET guest_pool = COALESCE(guest_pool, 0) + ?
			WHERE id = ? AND alloc_mode = 'manual'`, guest, periodID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("start a data cycle first")
		}
		stmt, err := tx.Prepare(`INSERT INTO allocation(period_id, person_id, bytes)
			VALUES(?, ?, ?) ON CONFLICT(period_id, person_id) DO UPDATE SET
			bytes = allocation.bytes + excluded.bytes`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for pid, b := range persons {
			if b <= 0 {
				continue
			}
			if _, err := stmt.Exec(periodID, pid, b); err != nil {
				return err
			}
		}
		return nil
	})
}
