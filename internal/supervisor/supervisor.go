// SPDX-License-Identifier: Apache-2.0

// Package supervisor manages the lifecycle of a single module process.
//
// Modules are always-on: every active module starts at core startup and
// stays running until core stops. There is no idle shutdown. A crash-loop guard
// hard-stops a module that crashes more than MaxRestarts times within
// RestartWindow.
//
// Identity: each launch creates a private, address-less socketpair. The child
// inherits one end as SUCTL_BROKER_FD (fd 3, its only inherited fd); core keeps
// the other end and hands it to the broker via OnChannel. Possession of the
// inherited end is the module's identity — there is no shared socket to dial.
//
// The child's stdout/stderr are piped into core's structured log, line by
// line — never to core's own stdout/stderr, which the TUI owns.
package supervisor

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/solutionsunity/suctl/internal/logpipe"
	"github.com/solutionsunity/suctl/sdk/channel"
	"github.com/solutionsunity/suctl/sdk/lifecycle"
)

// --------------------------------------------------------------------------
// Constants
// --------------------------------------------------------------------------

const (
	// DefaultMaxRestarts and DefaultRestartWindow are the crash-loop guard
	// fallbacks used when Config leaves them unset (zero). DefaultStopTimeout is
	// the graceful-stop→SIGKILL grace period. Core overrides all three from
	// suctl.conf (Gate D); a directly constructed supervisor (tests) keeps
	// these defaults.
	DefaultMaxRestarts   = 3
	DefaultRestartWindow = 60 * time.Second
	DefaultStopTimeout   = 30 * time.Second
)

// --------------------------------------------------------------------------
// State
// --------------------------------------------------------------------------

// State describes the current state of the supervised module process.
type State string

const (
	StateIdle    State = "idle"    // not yet started
	StateRunning State = "running" // process running
	StateStopped State = "stopped" // stopped cleanly via Stop
	StateFailed  State = "failed"  // hard-stopped after too many crashes
)

// --------------------------------------------------------------------------
// Config
// --------------------------------------------------------------------------

// Config holds the immutable configuration for a supervised module.
type Config struct {
	// ShortName is the module short name (e.g. "nginx").
	ShortName string
	// Entrypoint is the full command argv to launch the module process.
	Entrypoint []string
	// WorkDir is the working directory for the module process.
	// Typically the module directory so relative paths resolve correctly.
	// If empty, the process inherits core's working directory.
	WorkDir string
	// Env is additional environment variables for the child process.
	// SUCTL_BROKER_FD is always set automatically (the broker wire is the
	// child's only inherited fd, fd 3).
	Env []string
	// OnCrash is called in a new goroutine when the module process exits
	// unexpectedly, before any restart attempt. Optional.
	OnCrash func()
	// OnChannel is called with core's end of the broker wire each time the
	// module process (re)launches, so the broker can (re)bind the module's
	// possession-based identity to the live wire. A fresh channel is
	// created per launch, so this fires with a new wire on every restart. The
	// wire is an io.ReadWriteCloser — a net.Conn over the Unix socketpair, or
	// the bonded anonymous-pipe pair on Windows. Optional.
	OnChannel func(io.ReadWriteCloser)

	// MaxRestarts, RestartWindow, and StopTimeout tune the crash-loop guard and
	// shutdown grace period (Gate D). A zero value falls back to the matching
	// Default* constant, so tests and standalone callers may leave them unset.
	MaxRestarts   int
	RestartWindow time.Duration
	StopTimeout   time.Duration
}

// maxRestarts, restartWindow, and stopTimeout return the configured value or
// the compiled-in default when the Config field is left at its zero value.
func (s *Supervisor) maxRestarts() int {
	if s.cfg.MaxRestarts <= 0 {
		return DefaultMaxRestarts
	}
	return s.cfg.MaxRestarts
}

func (s *Supervisor) restartWindow() time.Duration {
	if s.cfg.RestartWindow <= 0 {
		return DefaultRestartWindow
	}
	return s.cfg.RestartWindow
}

func (s *Supervisor) stopTimeout() time.Duration {
	if s.cfg.StopTimeout <= 0 {
		return DefaultStopTimeout
	}
	return s.cfg.StopTimeout
}

// --------------------------------------------------------------------------
// Supervisor
// --------------------------------------------------------------------------

// Supervisor manages one module process: start on boot, monitor, restart on
// crash, stop on shutdown.
type Supervisor struct {
	cfg Config

	mu       sync.Mutex
	state    State
	cmd      *exec.Cmd
	restarts []time.Time // crash timestamps within RestartWindow
	// intentional marks the next process exit as an operator/health-driven
	// restart so the monitor relaunches without firing OnCrash and without
	// counting against the crash-loop guard (health escalation).
	intentional bool

	// channel is core's end of the current broker wire. A fresh socketpair is
	// created on every (re)launch; the prior core end is closed when the new
	// one replaces it. Read/written by the broker once bound via OnChannel.
	channel *channel.Pair

	exitCh chan struct{} // closed by monitor when process exits for good
	stopCh chan struct{} // closed by Stop to signal monitor: do not restart
}

// New creates a Supervisor for the given config. Call Start to launch.
func New(cfg Config) *Supervisor {
	return &Supervisor{
		cfg:    cfg,
		state:  StateIdle,
		exitCh: make(chan struct{}),
		stopCh: make(chan struct{}),
	}
}

// State returns the current supervisor state.
func (s *Supervisor) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// signalStop asks the module process to stop gracefully via the lifecycle seam
// (SIGTERM on Unix, Ctrl-Break on Windows). Must be called with s.mu held.
func (s *Supervisor) signalStop() {
	if s.cmd != nil && s.cmd.Process != nil {
		_ = lifecycle.Stop(s.cmd.Process)
	}
}

// Restart triggers an intentional restart of a wedged-but-live module: it asks
// the current process to stop and the monitor relaunches it without firing
// OnCrash and without counting against the crash-loop guard. Used by the
// lifecycle orchestrator's health escalation path. No-op unless the
// process is currently running.
func (s *Supervisor) Restart() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateRunning {
		return
	}
	s.intentional = true
	s.signalStop()
}

// Stop signals the monitor not to restart the module, asks it to stop
// gracefully, and waits for the process to exit (30 s timeout → SIGKILL).
// Stop is idempotent and safe to call from any goroutine.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	state := s.state
	s.mu.Unlock()
	if state == StateIdle {
		return // process was never started — nothing to do
	}

	// Signal the monitor not to restart after the next process exit.
	select {
	case <-s.stopCh:
	default:
		close(s.stopCh)
	}

	// Ask the live process to stop gracefully (lifecycle seam).
	s.mu.Lock()
	s.signalStop()
	s.mu.Unlock()

	// Wait for the monitor goroutine to finish (it closes exitCh on exit).
	// If the process does not exit within stopTimeout, escalate to SIGKILL.
	select {
	case <-s.exitCh:
		// clean exit
	case <-time.After(s.stopTimeout()):
		s.mu.Lock()
		if s.cmd != nil && s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		s.mu.Unlock()
	}

	// Release core's end of the broker wire — the process that held the other
	// end is gone, so the wire carries no more traffic.
	s.mu.Lock()
	ch := s.channel
	s.channel = nil
	s.mu.Unlock()
	if ch != nil {
		ch.Local.Close()
	}
}

// launch starts the module process. Must NOT be called with s.mu held.
func (s *Supervisor) launch() error {
	if len(s.cfg.Entrypoint) == 0 {
		return fmt.Errorf("supervisor %s: entrypoint is empty", s.cfg.ShortName)
	}

	cmd := exec.Command(s.cfg.Entrypoint[0], s.cfg.Entrypoint[1:]...)
	cmd.Stdin = nil
	// Child output is forwarded line-by-line to slog — never to os.Stdout/Stderr,
	// which the TUI owns. logpipe.Pipe creates the os.Pipe and starts the
	// forwarding goroutine; we only keep the write ends to assign and then close.
	outW, err := logpipe.Pipe("module output", "module", s.cfg.ShortName, "stream", "stdout")
	if err != nil {
		return fmt.Errorf("supervisor %s: stdout pipe: %w", s.cfg.ShortName, err)
	}
	errW, err := logpipe.Pipe("module output", "module", s.cfg.ShortName, "stream", "stderr")
	if err != nil {
		outW.Close()
		return fmt.Errorf("supervisor %s: stderr pipe: %w", s.cfg.ShortName, err)
	}
	closePipes := func() {
		outW.Close()
		errW.Close()
	}
	cmd.Stdout = outW
	cmd.Stderr = errW
	if s.cfg.WorkDir != "" {
		cmd.Dir = s.cfg.WorkDir
	}

	// Graceful-stop prerequisite (lifecycle seam): on Windows this puts the child
	// in its own process group so Stop can target it with Ctrl-Break; on Unix it
	// is a no-op. Composes with the channel seam's SysProcAttr use below.
	lifecycle.Configure(cmd)

	// Broker wire: a fresh, address-less socketpair per launch. The
	// child inherits its end as SUCTL_BROKER_FD; core keeps the other end and
	// hands it to the broker via OnChannel. Possession of the inherited end is
	// the module's identity — no peer-cred, no shared socket to dial.
	ch, err := channel.Spawn()
	if err != nil {
		closePipes()
		return fmt.Errorf("supervisor %s: broker wire: %w", s.cfg.ShortName, err)
	}

	// Hand the child its end of the broker wire. The OS-specific seam
	// (sdk/channel) wires inheritance onto cmd and returns the env naming the
	// inherited end(s): a single SUCTL_BROKER_FD=3 on Unix, the read/write handle
	// pair on Windows. Shared code stays OS-agnostic.
	env := append(os.Environ(), s.cfg.Env...)
	extraEnv, err := ch.Attach(cmd)
	if err != nil {
		closePipes()
		ch.Local.Close() //nolint:errcheck
		ch.CloseRemote()
		return fmt.Errorf("supervisor %s: attach broker wire: %w", s.cfg.ShortName, err)
	}
	env = append(env, extraEnv...)
	cmd.Env = env

	if err := cmd.Start(); err != nil {
		closePipes()
		ch.Local.Close() //nolint:errcheck
		ch.CloseRemote()
		// State is untouched: a first launch that never started stays StateIdle
		// (Stop no-ops); the monitor's relaunch paths set StateFailed themselves.
		return fmt.Errorf("supervisor %s: start: %w", s.cfg.ShortName, err)
	}

	// The process exists — only now is the supervisor running.
	s.mu.Lock()
	s.state = StateRunning
	s.cmd = cmd
	s.mu.Unlock()

	// Close parent's write ends — child is now the sole writer.
	// The logpipe forwarder goroutines exit on EOF when the child closes its ends.
	outW.Close()
	errW.Close()

	// The child inherited its own copy of the broker-wire end(s); close ours.
	// Swap in the new channel, closing any prior (now-stale) core end, then
	// notify the broker so it binds the module's identity to the live wire.
	ch.CloseRemote()
	s.mu.Lock()
	old := s.channel
	s.channel = ch
	s.mu.Unlock()
	if old != nil {
		old.Local.Close()
	}
	if s.cfg.OnChannel != nil {
		s.cfg.OnChannel(ch.Local)
	}

	return nil
}

// Start launches the module process and starts the monitor goroutine.
func (s *Supervisor) Start() error {
	if err := s.launch(); err != nil {
		return err
	}
	go s.monitor()
	return nil
}

// monitor waits for the process to exit and restarts it unless stopped or
// the restart health guard trips (MaxRestarts crashes in RestartWindow → StateFailed).
func (s *Supervisor) monitor() {
	for {
		s.mu.Lock()
		cmd := s.cmd
		s.mu.Unlock()

		if cmd == nil {
			return
		}

		_ = cmd.Wait() // blocks until exit

		// Check stop signal.
		select {
		case <-s.stopCh:
			s.mu.Lock()
			s.state = StateStopped
			s.mu.Unlock()
			close(s.exitCh)
			return
		default:
		}

		// Intentional restart (health escalation): relaunch without firing
		// on-crash and without counting against the crash-loop guard.
		s.mu.Lock()
		intentional := s.intentional
		s.intentional = false
		s.mu.Unlock()
		if intentional {
			if err := s.launch(); err != nil {
				s.mu.Lock()
				s.state = StateFailed
				s.mu.Unlock()
				close(s.exitCh)
				return
			}
			s.stopFreshChildIfStopping()
			continue
		}

		// Fire on-crash hook (non-blocking) before any restart attempt:
		// the process exited unexpectedly, so attempt a restart concurrently.
		if s.cfg.OnCrash != nil {
			go s.cfg.OnCrash()
		}

		// Restart health guard: count restarts within the restart window.
		now := time.Now()
		s.mu.Lock()
		cutoff := now.Add(-s.restartWindow())
		filtered := s.restarts[:0]
		for _, rt := range s.restarts {
			if rt.After(cutoff) {
				filtered = append(filtered, rt)
			}
		}
		filtered = append(filtered, now)
		s.restarts = filtered

		if len(s.restarts) > s.maxRestarts() {
			s.state = StateFailed
			s.mu.Unlock()
			close(s.exitCh)
			return
		}
		s.mu.Unlock()

		// Restart.
		if err := s.launch(); err != nil {
			s.mu.Lock()
			s.state = StateFailed
			s.mu.Unlock()
			close(s.exitCh)
			return
		}
		s.stopFreshChildIfStopping()
	}
}

// stopFreshChildIfStopping closes the Stop-vs-relaunch window: a Stop that
// raced a relaunch signalled the old, already-dead process, so the fresh child
// never received the graceful-stop request. Deliver it now; the next Wait then
// lands in the monitor's stop branch and finishes the shutdown normally.
func (s *Supervisor) stopFreshChildIfStopping() {
	select {
	case <-s.stopCh:
		s.mu.Lock()
		s.signalStop()
		s.mu.Unlock()
	default:
	}
}


