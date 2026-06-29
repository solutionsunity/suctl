// SPDX-License-Identifier: Apache-2.0

//go:build windows

package lifecycle

import (
	"os"
	"os/exec"
	"syscall"
)

// GenerateConsoleCtrlEvent is not exposed by the standard syscall package, so it
// is bound dynamically from kernel32.dll. Doing so keeps the SDK dependency-free
// (no golang.org/x/sys), matching the sdk/channel seam.
var (
	kernel32                    = syscall.NewLazyDLL("kernel32.dll")
	procGenerateConsoleCtrlEvnt = kernel32.NewProc("GenerateConsoleCtrlEvent")
)

// Configure puts the child in its own process group (CREATE_NEW_PROCESS_GROUP)
// so a later Stop can target it precisely with a console control event without
// disturbing the parent. Windows has no SIGTERM; this group is the prerequisite
// for the Ctrl-Break delivery in Stop. It composes with the channel seam's
// SysProcAttr use (handle inheritance): both guard nil and only add to it.
func Configure(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}

// Stop asks the process to terminate gracefully. Windows has no SIGTERM, so the
// faithful equivalent is a Ctrl-Break console control event sent to the child's
// process group (the group id equals the child's pid because Configure made it a
// group leader). The Go runtime in the child synthesises os.Interrupt from it
// (Ctrl-Break maps to SIGINT), so a module waiting on StopSignals shuts down
// exactly as it would on Unix. The caller owns the grace period: it waits for
// exit and escalates to p.Kill() on timeout.
func Stop(p *os.Process) error {
	// The group id equals the child's pid because Configure made it a group leader.
	r1, _, err := procGenerateConsoleCtrlEvnt.Call(uintptr(syscall.CTRL_BREAK_EVENT), uintptr(p.Pid))
	if r1 == 0 {
		return err
	}
	return nil
}
