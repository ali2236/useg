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

func TestSetDeviceLabel(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	mac := "aa:bb:cc:00:00:01"
	if err := db.UpsertLease(mac, "10.0.0.5", "Alis-MacBook-Pro", now); err != nil {
		t.Fatal(err)
	}
	if err := db.SetDeviceLabel(mac, " Living Room TV "); err != nil {
		t.Fatal(err)
	}
	// A later lease or mDNS announcement must not touch the override.
	if err := db.UpsertLease(mac, "10.0.0.6", "Alis-MacBook-Pro", now+60); err != nil {
		t.Fatal(err)
	}
	if err := db.SetDeviceName("10.0.0.6", "Ali’s MacBook Pro"); err != nil {
		t.Fatal(err)
	}
	dv, err := db.DeviceByMAC(mac)
	if err != nil {
		t.Fatal(err)
	}
	if dv.Label != "Living Room TV" || dv.Name != "Ali’s MacBook Pro" {
		t.Fatalf("device = %+v", dv)
	}

	// Clearing the label leaves the advertised name intact.
	if err := db.SetDeviceLabel(mac, ""); err != nil {
		t.Fatal(err)
	}
	dv, _ = db.DeviceByMAC(mac)
	if dv.Label != "" || dv.Name != "Ali’s MacBook Pro" {
		t.Fatalf("after clear: %+v", dv)
	}
}

func TestDeleteDevice(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	target, keep := "aa:bb:cc:00:00:01", "aa:bb:cc:00:00:02"
	for _, mac := range []string{target, keep} {
		if err := db.UpsertLease(mac, "", "", now); err != nil {
			t.Fatal(err)
		}
		for _, pid := range []int64{1, 2} {
			if err := db.UpsertUsage(pid, []UsageRow{{
				Subject: "dev:" + mac, RouteID: 1, ClassID: 1, Up: 10, Down: 20, Charged: 30,
			}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := db.SetBlock("dev:"+target, nil, "blocked by admin", "admin"); err != nil {
		t.Fatal(err)
	}
	for _, mac := range []string{target, keep} {
		if err := db.AddEvent("bind", "dev:"+mac, "by admin"); err != nil {
			t.Fatal(err)
		}
	}

	if err := db.DeleteDevice(target); err != nil {
		t.Fatal(err)
	}

	if _, err := db.DeviceByMAC(target); err == nil {
		t.Error("device row survived the delete")
	}
	var usage int
	if err := db.QueryRow(`SELECT COUNT(*) FROM usage WHERE subject = ?`, "dev:"+target).Scan(&usage); err != nil {
		t.Fatal(err)
	}
	if usage != 0 {
		t.Errorf("%d usage rows survived", usage)
	}
	var kept int
	if err := db.QueryRow(`SELECT COUNT(*) FROM usage WHERE subject = ?`, "dev:"+keep).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != 2 {
		t.Errorf("other device's usage = %d rows, want 2", kept)
	}
	blocks, err := db.Blocks()
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 0 {
		t.Errorf("block survived: %+v", blocks)
	}
	events, err := db.Events(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Subject != "dev:"+keep {
		t.Errorf("events = %+v, want only the other device's", events)
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
