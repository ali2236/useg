package singbox

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Supervisor keeps sing-box alive and reloads it only when the generated
// config actually changes. Fail-closed: if it cannot stay up, the caller is
// told so it can flip to bypass and alarm.
type Supervisor struct {
	bin   string
	path  string
	build func() ([]byte, error)
	log   *slog.Logger
	onBad func(string)

	mu          sync.Mutex
	cmd         *exec.Cmd
	running     bool
	stopping    bool
	hash        string
	crashes     []time.Time
	badNotified bool
}

func NewSupervisor(bin, path string, build func() ([]byte, error), log *slog.Logger, onBad func(string)) *Supervisor {
	return &Supervisor{bin: bin, path: path, build: build, log: log, onBad: onBad}
}

func (s *Supervisor) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Reconcile is safe to call every few seconds.
func (s *Supervisor) Reconcile() {
	want, err := s.build()
	if err != nil {
		s.log.Error("build sing-box config", "err", err)
		return
	}
	sum := sha256.Sum256(want)
	h := hex.EncodeToString(sum[:8])

	s.mu.Lock()
	defer s.mu.Unlock()
	if h != s.hash {
		if err := os.WriteFile(s.path+".tmp", want, 0o600); err != nil {
			s.log.Error("write sing-box config", "err", err)
			return
		}
		if err := os.Rename(s.path+".tmp", s.path); err != nil {
			s.log.Error("install sing-box config", "err", err)
			return
		}
		s.hash = h
		s.log.Info("sing-box config updated", "bytes", len(want))
		s.stopLocked()
	}
	if !s.running {
		if len(s.crashes) >= 3 && time.Since(s.crashes[len(s.crashes)-1]) < time.Minute {
			if s.onBad != nil && !s.badNotified {
				s.badNotified = true
				s.onBad("sing-box keeps dying; switching to bypass")
			}
			return
		}
		s.startLocked()
	}
}

func (s *Supervisor) startLocked() {
	cmd := exec.Command(s.bin, "run", "-c", s.path)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		s.log.Error("start sing-box", "err", err, "bin", s.bin)
		s.crashes = append(s.crashes, time.Now())
		return
	}
	s.cmd = cmd
	s.running = true
	s.stopping = false
	s.badNotified = false
	s.log.Info("sing-box started", "pid", cmd.Process.Pid, "config", s.path)
	go func() {
		err := cmd.Wait()
		s.mu.Lock()
		s.running = false
		if !s.stopping {
			s.crashes = append(s.crashes, time.Now())
			s.log.Warn("sing-box exited", "err", err)
		}
		s.mu.Unlock()
	}()
}

func (s *Supervisor) stopLocked() {
	if s.cmd == nil || s.cmd.Process == nil || !s.running {
		return
	}
	s.stopping = true
	_ = s.cmd.Process.Kill()
	s.running = false
	s.cmd = nil
}

func (s *Supervisor) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
}
