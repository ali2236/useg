package singbox

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// A config change must leave exactly one live child: the old process gone and
// the replacement still tracked. Losing track of it starts duplicate sing-box
// processes that die with "address already in use" and flip the box to bypass.
func TestReconcileRestartsWithoutOrphan(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pids")
	bin := filepath.Join(dir, "fake-sing-box")
	script := "#!/bin/sh\necho $$ >> " + pidFile + "\nexec sleep 30\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	want := []byte("one")
	build := func() ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		return want, nil
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sup := NewSupervisor(bin, filepath.Join(dir, "sing-box.json"), build, log, nil)
	t.Cleanup(sup.Stop)

	sup.Reconcile()
	waitFor(t, func() bool { return sup.Running() && len(pids(pidFile)) == 1 })

	mu.Lock()
	want = []byte("two")
	mu.Unlock()
	sup.Reconcile()
	time.Sleep(200 * time.Millisecond) // let a stale Wait goroutine do its damage
	sup.Reconcile()                    // must be a no-op: a process is running
	time.Sleep(300 * time.Millisecond) // a duplicate process would show up here

	got := pids(pidFile)
	if len(got) != 2 {
		t.Fatalf("started %d children, want 2 (old one replaced, not orphaned): %v", len(got), got)
	}
	if !sup.Running() {
		t.Fatal("supervisor lost track of the running child")
	}
	if alive(got[0]) {
		t.Errorf("old child %d still running after the restart", got[0])
	}
	if !alive(got[1]) {
		t.Errorf("replacement child %d is not running", got[1])
	}
}

func pids(path string) []int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []int
	for _, line := range strings.Fields(string(raw)) {
		if pid, err := strconv.Atoi(line); err == nil {
			out = append(out, pid)
		}
	}
	return out
}

func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before the deadline")
}
