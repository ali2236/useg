package store

import (
	"database/sql"
	"strings"
)

// DomesticSite is one row of the ITO domestic-tariff list: a site and the
// canonical network it resolves to. Updated/Due are the Jalali dates ITO
// publishes, kept as text.
type DomesticSite struct {
	Domain  string
	Network string
	Updated string
	Due     string
}

type DomesticPage struct {
	Sites   []DomesticSite
	Total   int
	Page    int
	PerPage int
	Pages   int
}

// ReplaceDomestic swaps the whole list in one transaction, so a failed refresh
// never leaves a half-written table behind.
func (d *DB) ReplaceDomestic(sites []DomesticSite) (int, error) {
	err := d.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM domestic_site`); err != nil {
			return err
		}
		stmt, err := tx.Prepare(`INSERT OR REPLACE INTO domestic_site(domain, network, updated, due)
			VALUES(?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, s := range sites {
			if _, err := stmt.Exec(s.Domain, s.Network, s.Updated, s.Due); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(sites), nil
}

// DomesticDomains returns every listed domain once, for classification.
func (d *DB) DomesticDomains() ([]string, error) {
	rows, err := d.Query(`SELECT DISTINCT domain FROM domestic_site`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var domain string
		if err := rows.Scan(&domain); err != nil {
			return nil, err
		}
		out = append(out, domain)
	}
	return out, rows.Err()
}

// DomesticSites returns one page of the list, filtered by q on domain or
// network. Page is clamped to the data that exists.
func (d *DB) DomesticSites(q string, page, perPage int) (DomesticPage, error) {
	if perPage <= 0 {
		perPage = 50
	}
	if page <= 0 {
		page = 1
	}
	where := ""
	var args []any
	if q = strings.TrimSpace(q); q != "" {
		like := "%" + escapeLike(q) + "%"
		where = ` WHERE domain LIKE ? ESCAPE '\' OR network LIKE ? ESCAPE '\'`
		args = []any{like, like}
	}

	out := DomesticPage{Page: page, PerPage: perPage}
	if err := d.QueryRow(`SELECT COUNT(*) FROM domestic_site`+where, args...).Scan(&out.Total); err != nil {
		return DomesticPage{}, err
	}
	out.Pages = (out.Total + perPage - 1) / perPage
	if out.Pages < 1 {
		out.Pages = 1
	}
	if out.Page > out.Pages {
		out.Page = out.Pages
	}

	rows, err := d.Query(`SELECT domain, network, updated, due FROM domestic_site`+where+
		` ORDER BY domain, network LIMIT ? OFFSET ?`,
		append(args, perPage, (out.Page-1)*perPage)...)
	if err != nil {
		return DomesticPage{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var s DomesticSite
		if err := rows.Scan(&s.Domain, &s.Network, &s.Updated, &s.Due); err != nil {
			return DomesticPage{}, err
		}
		out.Sites = append(out.Sites, s)
	}
	return out, rows.Err()
}

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	return strings.ReplaceAll(s, `_`, `\_`)
}
