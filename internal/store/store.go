package store

import (
	"crypto/rand"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

type DB struct{ *sql.DB }

func Open(path string) (*DB, error) {
	dsn := "file:" + path +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)"
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	sdb.SetMaxOpenConns(1) // one writer, no SQLITE_BUSY games
	if err := sdb.Ping(); err != nil {
		return nil, err
	}
	return &DB{sdb}, nil
}

func (d *DB) Migrate() error {
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS schema_migration(
		name TEXT PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, n := range names {
		var one int
		err := d.QueryRow(`SELECT 1 FROM schema_migration WHERE name = ?`, n).Scan(&one)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		raw, err := migrations.ReadFile(n)
		if err != nil {
			return err
		}
		err = d.tx(func(tx *sql.Tx) error {
			for _, stmt := range strings.Split(stripComments(string(raw)), ";") {
				if strings.TrimSpace(stmt) == "" {
					continue
				}
				if _, err := tx.Exec(stmt); err != nil {
					return fmt.Errorf("%s: %w", n, err)
				}
			}
			_, err := tx.Exec(`INSERT INTO schema_migration(name, applied_at) VALUES(?, ?)`, n, time.Now().Unix())
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// stripComments removes -- comments before splitting on ';', so a comment
// containing a semicolon cannot truncate a statement.
func stripComments(sqlText string) string {
	var b strings.Builder
	for _, line := range strings.Split(sqlText, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

func (d *DB) tx(fn func(*sql.Tx) error) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ---- settings ---------------------------------------------------------------

func (d *DB) Settings() (map[string]string, error) {
	rows, err := d.Query(`SELECT k, v FROM setting`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

func (d *DB) GetSetting(k string) (string, error) {
	var v string
	err := d.QueryRow(`SELECT v FROM setting WHERE k = ?`, k).Scan(&v)
	return v, err
}

func (d *DB) SetSetting(k, v string) error {
	_, err := d.Exec(`INSERT INTO setting(k, v) VALUES(?, ?)
		ON CONFLICT(k) DO UPDATE SET v = excluded.v`, k, v)
	return err
}

// ---- persons ----------------------------------------------------------------

type Person struct {
	ID          int64
	Name        string
	Weight      float64
	CapOverride *int64
	Enabled     bool
	SubToken    string
	CreatedAt   int64
	Locale      string
}

const personCols = `id, name, weight, cap_override, enabled, sub_token, created_at, locale`

func scanPerson(sc interface{ Scan(...any) error }) (Person, error) {
	var p Person
	var capOv sql.NullInt64
	err := sc.Scan(&p.ID, &p.Name, &p.Weight, &capOv, &p.Enabled, &p.SubToken, &p.CreatedAt, &p.Locale)
	if capOv.Valid {
		p.CapOverride = &capOv.Int64
	}
	return p, err
}

func (d *DB) Persons() ([]Person, error) {
	rows, err := d.Query(`SELECT ` + personCols + ` FROM person ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Person
	for rows.Next() {
		p, err := scanPerson(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (d *DB) Person(id int64) (Person, error) {
	return scanPerson(d.QueryRow(`SELECT `+personCols+` FROM person WHERE id = ?`, id))
}

func (d *DB) PersonByToken(token string) (Person, error) {
	return scanPerson(d.QueryRow(`SELECT `+personCols+` FROM person WHERE sub_token = ?`, token))
}

func (d *DB) CreatePerson(name string, weight float64, locale string) (int64, error) {
	tok, err := randHex(16)
	if err != nil {
		return 0, err
	}
	if locale == "" {
		locale = "fa"
	}
	res, err := d.Exec(`INSERT INTO person(name, weight, sub_token, created_at, locale)
		VALUES(?, ?, ?, ?, ?)`, name, weight, tok, time.Now().Unix(), locale)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) RotateSubToken(id int64) (string, error) {
	tok, err := randHex(16)
	if err != nil {
		return "", err
	}
	_, err = d.Exec(`UPDATE person SET sub_token = ? WHERE id = ?`, tok, id)
	return tok, err
}

// ---- devices ----------------------------------------------------------------

type Device struct {
	MAC        string
	IP         string
	Hostname   string
	FirstSeen  int64
	LastSeen   int64
	Owner      *int64
	BoundAt    *int64
	BindSource string
}

const deviceCols = `mac, COALESCE(ip,''), COALESCE(hostname,''), COALESCE(first_seen,0),
	COALESCE(last_seen,0), owner_person_id, bound_at, COALESCE(bind_source,'')`

func scanDevice(sc interface{ Scan(...any) error }) (Device, error) {
	var dv Device
	var owner, bound sql.NullInt64
	err := sc.Scan(&dv.MAC, &dv.IP, &dv.Hostname, &dv.FirstSeen, &dv.LastSeen, &owner, &bound, &dv.BindSource)
	if owner.Valid {
		dv.Owner = &owner.Int64
	}
	if bound.Valid {
		dv.BoundAt = &bound.Int64
	}
	return dv, err
}

func (d *DB) Devices() ([]Device, error) {
	rows, err := d.Query(`SELECT ` + deviceCols + ` FROM device ORDER BY mac`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		dv, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, dv)
	}
	return out, rows.Err()
}

func (d *DB) DeviceByIP(ip string) (Device, error) {
	return scanDevice(d.QueryRow(`SELECT `+deviceCols+` FROM device WHERE ip = ?`, ip))
}

func (d *DB) DeviceByMAC(mac string) (Device, error) {
	return scanDevice(d.QueryRow(`SELECT `+deviceCols+` FROM device WHERE mac = ?`, mac))
}

// UpsertLease keeps the MAC key stable while the IP moves, and forgets a stale
// IP so a recycled address cannot inherit the old device's binding.
func (d *DB) UpsertLease(mac, ip, hostname string, now int64) error {
	if mac == "" {
		return nil
	}
	return d.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE device SET ip = NULL
			WHERE ip = ? AND mac <> ?`, ip, mac); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO device(mac, ip, hostname, first_seen, last_seen)
			VALUES(?, ?, ?, ?, ?)
			ON CONFLICT(mac) DO UPDATE SET
				ip = excluded.ip,
				hostname = COALESCE(NULLIF(excluded.hostname,''), device.hostname),
				last_seen = excluded.last_seen`, mac, ip, hostname, now, now)
		return err
	})
}

func (d *DB) Bind(mac string, personID int64, source string, now int64) error {
	_, err := d.Exec(`UPDATE device SET owner_person_id = ?, bound_at = ?, bind_source = ? WHERE mac = ?`,
		personID, now, source, mac)
	return err
}

func (d *DB) Unbind(mac string) error {
	_, err := d.Exec(`UPDATE device SET owner_person_id = NULL, bound_at = NULL, bind_source = NULL WHERE mac = ?`, mac)
	return err
}

func (d *DB) TouchBinding(mac string, now int64) error {
	_, err := d.Exec(`UPDATE device SET bound_at = ? WHERE mac = ?`, now, mac)
	return err
}

// ExpireBindings returns devices that have not refreshed their binding.
func (d *DB) ExpireBindings(before int64) ([]Device, error) {
	rows, err := d.Query(`SELECT `+deviceCols+` FROM device
		WHERE owner_person_id IS NOT NULL AND COALESCE(bound_at,0) < ?`, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		dv, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, dv)
	}
	return out, rows.Err()
}

// ---- routes / credentials ---------------------------------------------------

type Route struct {
	ID           int64
	Name         string
	Kind         string
	UpstreamJSON string
	AllowAll     bool
	Enabled      bool
}

const routeCols = `id, name, kind, upstream_json, allow_all, enabled`

func scanRoute(sc interface{ Scan(...any) error }) (Route, error) {
	var r Route
	err := sc.Scan(&r.ID, &r.Name, &r.Kind, &r.UpstreamJSON, &r.AllowAll, &r.Enabled)
	return r, err
}

func (d *DB) Routes() ([]Route, error) {
	rows, err := d.Query(`SELECT ` + routeCols + ` FROM route ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Route
	for rows.Next() {
		r, err := scanRoute(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (d *DB) Route(id int64) (Route, error) {
	return scanRoute(d.QueryRow(`SELECT `+routeCols+` FROM route WHERE id = ?`, id))
}

func (d *DB) SaveRoute(r Route) (int64, error) {
	if r.ID == 0 {
		res, err := d.Exec(`INSERT INTO route(name, kind, upstream_json, allow_all, enabled)
			VALUES(?, ?, ?, ?, ?)`, r.Name, r.Kind, r.UpstreamJSON, r.AllowAll, r.Enabled)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := d.Exec(`UPDATE route SET name = ?, kind = ?, upstream_json = ?,
		allow_all = ?, enabled = ? WHERE id = ?`,
		r.Name, r.Kind, r.UpstreamJSON, r.AllowAll, r.Enabled, r.ID)
	return r.ID, err
}

func (d *DB) DeleteRoute(id int64) error {
	_, err := d.Exec(`DELETE FROM route WHERE id = ?`, id)
	return err
}

type Credential struct {
	ID       int64
	PersonID int64
	RouteID  int64
	UUID     string
	Port     int
	Enabled  bool
}

const credCols = `id, person_id, route_id, uuid, port, enabled`

func scanCred(sc interface{ Scan(...any) error }) (Credential, error) {
	var c Credential
	err := sc.Scan(&c.ID, &c.PersonID, &c.RouteID, &c.UUID, &c.Port, &c.Enabled)
	return c, err
}

func (d *DB) Credentials() ([]Credential, error) {
	rows, err := d.Query(`SELECT ` + credCols + ` FROM credential ORDER BY person_id, route_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Credential
	for rows.Next() {
		c, err := scanCred(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (d *DB) CredentialFor(personID, routeID int64) (Credential, error) {
	return scanCred(d.QueryRow(`SELECT `+credCols+` FROM credential WHERE person_id = ? AND route_id = ?`,
		personID, routeID))
}

func (d *DB) EnsureCredential(personID, routeID int64) (Credential, error) {
	c, err := d.CredentialFor(personID, routeID)
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Credential{}, err
	}
	u, err := randUUID()
	if err != nil {
		return Credential{}, err
	}
	port, err := d.NextCredentialPort()
	if err != nil {
		return Credential{}, err
	}
	if _, err := d.Exec(`INSERT INTO credential(person_id, route_id, uuid, port) VALUES(?, ?, ?, ?)`,
		personID, routeID, u, port); err != nil {
		return Credential{}, err
	}
	return d.CredentialFor(personID, routeID)
}

func (d *DB) NextCredentialPort() (int, error) {
	base := 10443
	if v, err := d.GetSetting("credential_base_port"); err == nil && v != "" {
		fmt.Sscanf(v, "%d", &base)
	}
	var maxPort sql.NullInt64
	if err := d.QueryRow(`SELECT MAX(port) FROM credential`).Scan(&maxPort); err != nil {
		return 0, err
	}
	if maxPort.Valid && int(maxPort.Int64) >= base {
		return int(maxPort.Int64) + 1, nil
	}
	return base, nil
}

func (d *DB) SetCredentialEnabled(id int64, enabled bool) error {
	_, err := d.Exec(`UPDATE credential SET enabled = ? WHERE id = ?`, enabled, id)
	return err
}

func (d *DB) DeleteCredential(id int64) error {
	_, err := d.Exec(`DELETE FROM credential WHERE id = ?`, id)
	return err
}

// ---- classes / rates --------------------------------------------------------

type Class struct {
	ID   int64
	Name string
}

func (d *DB) Classes() ([]Class, error) {
	rows, err := d.Query(`SELECT id, name FROM class ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Class
	for rows.Next() {
		var c Class
		if err := rows.Scan(&c.ID, &c.Name); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RateMap returns route_id -> class_id -> weight.
func (d *DB) RateMap() (map[int64]map[int64]float64, error) {
	rows, err := d.Query(`SELECT route_id, class_id, weight FROM rate`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]map[int64]float64{}
	for rows.Next() {
		var r, c int64
		var w float64
		if err := rows.Scan(&r, &c, &w); err != nil {
			return nil, err
		}
		if out[r] == nil {
			out[r] = map[int64]float64{}
		}
		out[r][c] = w
	}
	return out, rows.Err()
}

func (d *DB) SetRate(routeID, classID int64, weight float64) error {
	_, err := d.Exec(`INSERT INTO rate(route_id, class_id, weight) VALUES(?, ?, ?)
		ON CONFLICT(route_id, class_id) DO UPDATE SET weight = excluded.weight`, routeID, classID, weight)
	return err
}

// ---- blocks / events --------------------------------------------------------

type Block struct {
	Subject   string
	Until     *int64
	Reason    string
	CreatedBy string
}

func (d *DB) Blocks() ([]Block, error) {
	rows, err := d.Query(`SELECT subject, until, COALESCE(reason,''), COALESCE(created_by,'') FROM block`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Block
	for rows.Next() {
		var b Block
		var until sql.NullInt64
		if err := rows.Scan(&b.Subject, &until, &b.Reason, &b.CreatedBy); err != nil {
			return nil, err
		}
		if until.Valid {
			b.Until = &until.Int64
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (d *DB) SetBlock(subject string, until *int64, reason, by string) error {
	_, err := d.Exec(`INSERT INTO block(subject, until, reason, created_by) VALUES(?, ?, ?, ?)
		ON CONFLICT(subject) DO UPDATE SET until = excluded.until, reason = excluded.reason,
		created_by = excluded.created_by`, subject, until, reason, by)
	return err
}

func (d *DB) ClearBlock(subject string) error {
	_, err := d.Exec(`DELETE FROM block WHERE subject = ?`, subject)
	return err
}

func (d *DB) AddEvent(kind, subject, detail string) error {
	_, err := d.Exec(`INSERT INTO event(at, kind, subject, detail) VALUES(?, ?, ?, ?)`,
		time.Now().Unix(), kind, subject, detail)
	return err
}

type Event struct {
	At      int64
	Kind    string
	Subject string
	Detail  string
}

func (d *DB) Events(limit int) ([]Event, error) {
	rows, err := d.Query(`SELECT at, kind, COALESCE(subject,''), COALESCE(detail,'')
		FROM event ORDER BY at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.At, &e.Kind, &e.Subject, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- random -----------------------------------------------------------------

func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", b), nil
}

func randUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
