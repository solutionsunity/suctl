// SPDX-License-Identifier: Apache-2.0

package supervisor_test

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/solutionsunity/suctl/internal/supervisor"
)

// --------------------------------------------------------------------------
// Subprocess helper
// --------------------------------------------------------------------------
// Tests that need a real child process re-invoke the test binary with
// SUCTL_TEST_SUBPROCESS=<role>. TestMain dispatches to the role handler.

func TestMain(m *testing.M) {
	switch os.Getenv("SUCTL_TEST_SUBPROCESS") {
	case "exit0":
		os.Exit(0)
	case "exit1":
		os.Exit(1)
	case "sleep":
		time.Sleep(10 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// selfCmd returns an exec.Cmd that re-runs the test binary in subprocess mode.
func selfCmd(role string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "SUCTL_TEST_SUBPROCESS="+role)
	return cmd
}

// --------------------------------------------------------------------------
// Config helpers
// --------------------------------------------------------------------------

func exitConfig(role string) supervisor.Config {
	c := selfCmd(role)
	return supervisor.Config{
		ShortName:  "test",
		Entrypoint: c.Args,
		Env:        []string{"SUCTL_TEST_SUBPROCESS=" + role},
	}
}

// --------------------------------------------------------------------------
// New / State
// --------------------------------------------------------------------------

func TestNew_InitialState(t *testing.T) {
	s := supervisor.New(exitConfig("exit0"))
	if s.State() != supervisor.StateIdle {
		t.Errorf("initial state = %q; want idle", s.State())
	}
}

// --------------------------------------------------------------------------
// Start
// --------------------------------------------------------------------------

func TestStart_EmptyEntrypoint_Error(t *testing.T) {
	s := supervisor.New(supervisor.Config{ShortName: "test"})
	err := s.Start()
	if err == nil {
		t.Error("expected error for empty entrypoint")
	}
}

func TestStart_NonexistentBinary_Error(t *testing.T) {
	s := supervisor.New(supervisor.Config{
		ShortName:  "test",
		Entrypoint: []string{"/nonexistent/binary/suctl-mod-test"},
	})
	err := s.Start()
	if err == nil {
		t.Error("expected error for nonexistent binary")
	}
}

func TestStart_SetsRunningState(t *testing.T) {
	s := supervisor.New(exitConfig("sleep"))
	if err := s.Start(); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	defer s.Stop()

	if s.State() != supervisor.StateRunning {
		t.Errorf("state after Start = %q; want running", s.State())
	}
}

// --------------------------------------------------------------------------
// Stop
// --------------------------------------------------------------------------

func TestStop_SetsStopped(t *testing.T) {
	s := supervisor.New(exitConfig("sleep"))
	if err := s.Start(); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	s.Stop()

	// Give the monitor goroutine a moment to update state.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s.State() == supervisor.StateStopped {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("state after Stop = %q; want stopped", s.State())
}

func TestStop_Idempotent(t *testing.T) {
	s := supervisor.New(exitConfig("sleep"))
	if err := s.Start(); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	s.Stop()
	s.Stop() // second call must not panic
}

// --------------------------------------------------------------------------
// Crash-loop guard (3 restarts in 60s → FAILED)
// --------------------------------------------------------------------------

func TestMonitor_FailedAfterTooManyRestarts(t *testing.T) {
	// Use a process that exits immediately — it will be restarted until the
	// guard trips.
	s := supervisor.New(exitConfig("exit0"))
	if err := s.Start(); err != nil {
		t.Fatalf("Start error: %v", err)
	}

	// Wait for FAILED state — with exit0 each restart is near-instant.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s.State() == supervisor.StateFailed {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("state = %q after restarts; want failed", s.State())
}

// --------------------------------------------------------------------------
// Constants
// --------------------------------------------------------------------------

func TestConstants(t *testing.T) {
	if supervisor.DefaultMaxRestarts != 3 {
		t.Errorf("DefaultMaxRestarts = %d; want 3", supervisor.DefaultMaxRestarts)
	}
	if supervisor.DefaultRestartWindow != 60*time.Second {
		t.Errorf("DefaultRestartWindow = %v; want 60s", supervisor.DefaultRestartWindow)
	}
	if supervisor.DefaultStopTimeout != 30*time.Second {
		t.Errorf("DefaultStopTimeout = %v; want 30s", supervisor.DefaultStopTimeout)
	}
	if supervisor.StateIdle != "idle" {
		t.Errorf("StateIdle = %q; want idle", supervisor.StateIdle)
	}
	if supervisor.StateFailed != "failed" {
		t.Errorf("StateFailed = %q; want failed", supervisor.StateFailed)
	}
}
