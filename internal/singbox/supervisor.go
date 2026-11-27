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
	proc        *proc
	hash        string
	crashes     []time.Time
	badNotified bool
}

// proc is one child process. stopped is set under Supervisor.mu before the
// kill, so its Wait goroutine knows the exit was deliberate.
type proc struct {
	cmd     *exec.Cmd
	done    chan struct{}
	stopped bool
}

func NewSupervisor(bin, path string, build func() ([]byte, error), log *slog.Logger, onBad func(string)) *Supervisor {
	return &Supervisor{bin: bin, path: path, build: build, log: log, onBad: onBad}
}

func (s *Supervisor) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.proc != nil
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
	if s.proc == nil {
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
	p := &proc{cmd: cmd, done: make(chan struct{})}
	s.proc = p
	s.badNotified = false
	s.log.Info("sing-box started", "pid", cmd.Process.Pid, "config", s.path)
	go func() {
		err := cmd.Wait()
		close(p.done)
		s.mu.Lock()
		// Only the newest process may clear state; an old one that exits late
		// must not make the supervisor think nothing is running.
		if s.proc == p {
			s.proc = nil
		}
		if !p.stopped {
			s.crashes = append(s.crashes, time.Now())
			s.log.Warn("sing-box exited", "err", err)
		}
		s.mu.Unlock()
	}()
}

// stopLocked kills the child and waits for it to go away, so the next start
// cannot race it for the listen ports.
func (s *Supervisor) stopLocked() {
	p := s.proc
	if p == nil {
		return
	}
	p.stopped = true
	s.proc = nil
	_ = p.cmd.Process.Kill()
	select {
	case <-p.done:
	case <-time.After(3 * time.Second):
		s.log.Warn("sing-box did not exit after kill", "pid", p.cmd.Process.Pid)
	}
}

func (s *Supervisor) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
}
