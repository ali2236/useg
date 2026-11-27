package store

import (
	"database/sql"
	"math"
)

// ChargedBytes rounds a charged ledger value up: a fraction of a byte is still
// bytes spent, so measurement must never come out below the ledger.
func ChargedBytes(charged float64) int64 {
	if charged <= 0 {
		return 0
	}
	return int64(math.Ceil(charged))
}

type UsageRow struct {
	Subject string
	RouteID int64
	ClassID int64
	Up      int64
	Down    int64
	Charged float64
}

// UpsertUsage is called once per flush window, so the DB sees a few hundred
// writes a day instead of one per poll.
func (d *DB) UpsertUsage(periodID int64, rows []UsageRow) error {
	if len(rows) == 0 {
		return nil
	}
	return d.tx(func(tx *sql.Tx) error {
		stmt, err := tx.Prepare(`INSERT INTO usage(period_id, subject, route_id, class_id,
				bytes_up, bytes_down, charged)
			VALUES(?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(period_id, subject, route_id, class_id) DO UPDATE SET
				bytes_up = usage.bytes_up + excluded.bytes_up,
				bytes_down = usage.bytes_down + excluded.bytes_down,
				charged = usage.charged + excluded.charged`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, r := range rows {
			if _, err := stmt.Exec(periodID, r.Subject, r.RouteID, r.ClassID,
				r.Up, r.Down, r.Charged); err != nil {
				return err
			}
		}
		return nil
	})
}

type Totals struct {
	Charged float64
	Raw     int64
}

func (d *DB) SubjectTotals(periodID int64) (map[string]Totals, error) {
	rows, err := d.Query(`SELECT subject, SUM(charged), SUM(bytes_up + bytes_down)
		FROM usage WHERE period_id = ? GROUP BY subject`, periodID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Totals{}
	for rows.Next() {
		var s string
		var t Totals
		if err := rows.Scan(&s, &t.Charged, &t.Raw); err != nil {
			return nil, err
		}
		out[s] = t
	}
	return out, rows.Err()
}

func (d *DB) PeriodRaw(periodID int64) (int64, error) {
	var raw sql.NullInt64
	err := d.QueryRow(`SELECT SUM(bytes_up + bytes_down) FROM usage WHERE period_id = ?`,
		periodID).Scan(&raw)
	return raw.Int64, err
}

// UsageDetail is the admin table: what was spent, on which route, in which class.
func (d *DB) UsageDetail(periodID int64) ([]UsageRow, error) {
	rows, err := d.Query(`SELECT subject, route_id, class_id, bytes_up, bytes_down, charged
		FROM usage WHERE period_id = ? ORDER BY charged DESC`, periodID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageRow
	for rows.Next() {
		var r UsageRow
		if err := rows.Scan(&r.Subject, &r.RouteID, &r.ClassID, &r.Up, &r.Down, &r.Charged); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
