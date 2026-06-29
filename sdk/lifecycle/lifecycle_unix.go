// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package lifecycle

import (
	"os"
	"os/exec"
	"syscall"
)

// Configure applies any spawn-time process attributes that a later Stop needs.
// On Unix none are required — SIGTERM reaches the child by pid directly — so
// this is a no-op. The Windows counterpart puts the child in its own process
// group so it can be targeted by a console control event.
func Configure(cmd *exec.Cmd) {}

// Stop asks the process to terminate gracefully by sending SIGTERM. The caller
// owns the grace period: it waits for exit and escalates to p.Kill() on timeout.
func Stop(p *os.Process) error {
	return p.Signal(syscall.SIGTERM)
}
