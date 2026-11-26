// Package domestic fetches the official ITO "domestic tariff" list — the
// sites and networks Iranian operators bill as نیم‌بها (half-price) traffic.
//
// The search page is captcha-gated, but its Excel export is not: a session
// cookie plus the page's antiforgery token is enough. The export flag is
// undocumented, so Fetch fails closed: a short or unparsable response is an
// error, never an empty list.
package domestic

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"

	"useg/internal/store"
)

const (
	DefaultBaseURL = "https://eservices.ito.gov.ir"
	searchPath     = "/Page/IPListSearch"
	exportPath     = "/Page/GetIPList"

	defaultMinRows = 1000
	maxPageBytes   = 2 << 20
	maxExportBytes = 32 << 20
)

type Fetcher struct {
	BaseURL string
	MinRows int
	Client  *http.Client
}

var (
	inputTagRe = regexp.MustCompile(`(?s)<input[^>]*>`)
	valueRe    = regexp.MustCompile(`\bvalue="([^"]*)"`)
	rowRe      = regexp.MustCompile(`(?s)<tr[^>]*>(.*?)</tr>`)
	cellRe     = regexp.MustCompile(`(?s)<t[dh][^>]*>(.*?)</t[dh]>`)
	tagRe      = regexp.MustCompile(`<[^>]*>`)
)

func (f Fetcher) Fetch(ctx context.Context) ([]store.DomesticSite, error) {
	base := strings.TrimRight(f.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	minRows := f.MinRows
	if minRows <= 0 {
		minRows = defaultMinRows
	}
	client := f.Client
	if client == nil || client.Jar == nil {
		jar, err := cookiejar.New(nil)
		if err != nil {
			return nil, err
		}
		if client == nil {
			client = &http.Client{Jar: jar, Timeout: 90 * time.Second}
		} else {
			c := *client
			c.Jar = jar
			client = &c
		}
	}

	token, err := f.token(ctx, client, base)
	if err != nil {
		return nil, err
	}

	form := url.Values{"__RequestVerificationToken": {token}, "ExportExcel": {"true"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+exportPath, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", base+searchPath)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("domestic: export status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxExportBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxExportBytes {
		return nil, errors.New("domestic: export exceeded size limit")
	}

	sites := Parse(body)
	if len(sites) < minRows {
		return nil, fmt.Errorf("domestic: parsed %d rows, need at least %d; the ITO export may have changed", len(sites), minRows)
	}
	return sites, nil
}

func (f Fetcher) token(ctx context.Context, client *http.Client, base string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+searchPath, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("domestic: search page status %d", resp.StatusCode)
	}
	page, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes))
	if err != nil {
		return "", err
	}
	for _, tag := range inputTagRe.FindAll(page, -1) {
		if !strings.Contains(string(tag), `name="__RequestVerificationToken"`) {
			continue
		}
		if m := valueRe.FindSubmatch(tag); m != nil {
			return string(m[1]), nil
		}
	}
	return "", errors.New("domestic: antiforgery token not found; the ITO page may have changed")
}

// Parse turns the Excel-HTML export into rows. It is exported for tests.
func Parse(body []byte) []store.DomesticSite {
	sites := make([]store.DomesticSite, 0, 60000)
	seen := make(map[string]struct{}, 60000)
	for _, row := range rowRe.FindAllSubmatch(body, -1) {
		cells := cellRe.FindAllSubmatch(row[1], -1)
		if len(cells) < 5 {
			continue // header or a malformed row
		}
		v := make([]string, 0, 5)
		for _, c := range cells[:5] {
			v = append(v, text(c[1]))
		}
		domain := normalizeDomain(v[0])
		network := v[2]
		if domain == "" || network == "" {
			continue
		}
		if _, err := netip.ParsePrefix(network); err != nil {
			continue
		}
		key := domain + "\x00" + network
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		sites = append(sites, store.DomesticSite{
			Domain: domain, Network: network, Updated: v[3], Due: v[4],
		})
	}
	return sites
}

func text(b []byte) string {
	s := tagRe.ReplaceAllString(string(b), "")
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, "\u00a0", " ")
	return strings.TrimSpace(s)
}

func normalizeDomain(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/*:"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSuffix(s, ".")
	if strings.ContainsAny(s, " \t\r\n") {
		return ""
	}
	return s
}
