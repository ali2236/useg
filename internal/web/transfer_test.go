package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"useg/internal/quota"
)

// A bound person can move part of their remaining share to another person, and
// the new caps are what the quota engine and the portal both report.
func TestPortalTransferMovesShare(t *testing.T) {
	srv, db, cfg, person := tserver(t)
	now := time.Now()
	if err := db.Bind("aa:bb:cc:00:00:03", person.ID, "test", now.Unix()); err != nil {
		t.Fatal(err)
	}
	sara, err := db.CreatePerson("sara", 1.0, "fa")
	if err != nil {
		t.Fatal(err)
	}
	period, err := db.EnsurePeriod(now, cfg.Location())
	if err != nil {
		t.Fatal(err)
	}

	rec := do(srv.PortalHandler(), "POST", "/transfer", "10.66.0.3:5000", url.Values{
		"to": {strconv.FormatInt(sara, 10)}, "gb": {"1"},
	}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/?ok=transfer" {
		t.Fatalf("transfer: code=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}
	net, err := db.TransferNet(period.ID)
	if err != nil {
		t.Fatal(err)
	}
	if net[person.ID] != -1e9 || net[sara] != 1e9 {
		t.Fatalf("transfer net = %v", net)
	}

	in, err := quota.Gather(db, period)
	if err != nil {
		t.Fatal(err)
	}
	dec := quota.Compute(in)
	// Both weigh 1.0 over (20 - 2) GB: 9 GB each before the transfer.
	if got := dec.Subjects[quota.PersonSubject(person.ID)].Cap; got != 8e9 {
		t.Fatalf("sender cap = %d, want 8GB", got)
	}
	if got := dec.Subjects[quota.PersonSubject(sara)].Cap; got != 10e9 {
		t.Fatalf("receiver cap = %d, want 10GB", got)
	}

	body := do(srv.PortalHandler(), "GET", "/?ok=transfer", "10.66.0.3:5000", nil, nil).Body.String()
	if !strings.Contains(body, "داده منتقل شد") {
		t.Errorf("portal misses the transfer notice:\n%s", body)
	}
	if !strings.Contains(body, "/transfer?to="+strconv.FormatInt(sara, 10)) {
		t.Errorf("portal misses the transfer button for sara:\n%s", body)
	}
	if strings.Contains(body, "/transfer?to="+strconv.FormatInt(person.ID, 10)) {
		t.Error("the sender must not get a transfer button on their own tile")
	}
}

func TestPortalTransferRejectsMoreThanRemaining(t *testing.T) {
	srv, db, _, person := tserver(t)
	now := time.Now()
	if err := db.Bind("aa:bb:cc:00:00:03", person.ID, "test", now.Unix()); err != nil {
		t.Fatal(err)
	}
	sara, err := db.CreatePerson("sara", 1.0, "fa")
	if err != nil {
		t.Fatal(err)
	}

	rec := do(srv.PortalHandler(), "POST", "/transfer", "10.66.0.3:5000", url.Values{
		"to": {strconv.FormatInt(sara, 10)}, "gb": {"100"},
	}, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "باقی‌مانده کافی ندارید") {
		t.Fatalf("over-transfer was not rejected: %d\n%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "داده منتقل شد") {
		t.Fatal("over-transfer reported success")
	}
}

// A manual cycle already moves allocation rows, so the transfer row must not be
// applied a second time as a cap delta.
func TestPortalTransferMovesManualAllocation(t *testing.T) {
	srv, db, cfg, person := tserver(t)
	now := time.Now()
	if err := db.Bind("aa:bb:cc:00:00:03", person.ID, "test", now.Unix()); err != nil {
		t.Fatal(err)
	}
	sara, err := db.CreatePerson("sara", 1.0, "fa")
	if err != nil {
		t.Fatal(err)
	}
	period, err := db.StartCycle(now, 30, "days", 10e9, cfg.Location())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyAllocation(period.ID, map[int64]int64{person.ID: 5e9, sara: 5e9}, 0); err != nil {
		t.Fatal(err)
	}

	rec := do(srv.PortalHandler(), "POST", "/transfer", "10.66.0.3:5000", url.Values{
		"to": {strconv.FormatInt(sara, 10)}, "gb": {"1"},
	}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("transfer: %d", rec.Code)
	}
	allocs, err := db.Allocations(period.ID)
	if err != nil {
		t.Fatal(err)
	}
	if allocs[person.ID] != 4e9 || allocs[sara] != 6e9 {
		t.Fatalf("allocations after transfer = %v", allocs)
	}

	in, err := quota.Gather(db, period)
	if err != nil {
		t.Fatal(err)
	}
	dec := quota.Compute(in)
	if got := dec.Subjects[quota.PersonSubject(person.ID)].Cap; got != 4e9 {
		t.Fatalf("sender cap = %d, want 4GB (no double counting)", got)
	}
	if got := dec.Subjects[quota.PersonSubject(sara)].Cap; got != 6e9 {
		t.Fatalf("receiver cap = %d, want 6GB", got)
	}
}
