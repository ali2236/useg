package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSetDeviceName(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if err := db.UpsertLease("aa:bb:cc:00:00:01", "10.0.0.5", "Alis-MacBook-Pro", now); err != nil {
		t.Fatal(err)
	}
	if err := db.SetDeviceName("10.0.0.5", "Ali’s MacBook Pro"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetDeviceName("10.0.0.5", ""); err != nil {
		t.Fatal(err)
	}
	dv, err := db.DeviceByMAC("aa:bb:cc:00:00:01")
	if err != nil {
		t.Fatal(err)
	}
	if dv.Name != "Ali’s MacBook Pro" || dv.Hostname != "Alis-MacBook-Pro" {
		t.Fatalf("device = %+v", dv)
	}

	// A recycled address must not inherit or overwrite the old device's name.
	if err := db.UpsertLease("aa:bb:cc:00:00:02", "10.0.0.5", "", now); err != nil {
		t.Fatal(err)
	}
	if err := db.SetDeviceName("10.0.0.5", "Galaxy S25 FE"); err != nil {
		t.Fatal(err)
	}
	old, _ := db.DeviceByMAC("aa:bb:cc:00:00:01")
	if old.Name != "Ali’s MacBook Pro" {
		t.Errorf("old device name = %q", old.Name)
	}
	fresh, _ := db.DeviceByMAC("aa:bb:cc:00:00:02")
	if fresh.Name != "Galaxy S25 FE" {
		t.Errorf("new device name = %q", fresh.Name)
	}
}

func TestSetLastUsed(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	for _, d := range [][2]string{
		{"aa:bb:cc:00:00:01", "10.0.0.5"},
		{"aa:bb:cc:00:00:02", "10.0.0.6"},
	} {
		if err := db.UpsertLease(d[0], d[1], "", now); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SetLastUsed(now+60, []string{"aa:bb:cc:00:00:02"}); err != nil {
		t.Fatal(err)
	}
	first, _ := db.DeviceByMAC("aa:bb:cc:00:00:01")
	second, _ := db.DeviceByMAC("aa:bb:cc:00:00:02")
	if first.LastUsed != 0 || second.LastUsed != now+60 {
		t.Fatalf("last_used = %d / %d", first.LastUsed, second.LastUsed)
	}
}
