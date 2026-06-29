// SPDX-License-Identifier: Apache-2.0

// Package lifecycle is the process-control seam — the single, audited home of
// all OS-specific logic for stopping a child process gracefully. It is the
// sibling of sdk/channel: channel owns the wire (bytes), lifecycle owns process
// control (signals). They are different axes and are kept apart on purpose.
//
// The problem. A faithful graceful stop is "ask the process to exit, give it a
// grace period, then kill it". The "ask" step is OS-specific: Unix sends
// SIGTERM directly to the pid; Windows has no SIGTERM, so the equivalent is a
// Ctrl-Break console control event delivered to the child's own process group.
//
// Shape. There are two halves, mirrored on both platforms:
//
//   - The PARENT side (core's supervisor, the BIST harness). At spawn it calls
//     Configure(cmd) so the child can later be targeted precisely (a no-op on
//     Unix; a new process group on Windows). To stop, it calls Stop(proc), then
//     waits and escalates to proc.Kill() on timeout.
//
//   - The CHILD side (core's wait loop, a module's modserver). It blocks
//     on StopSignals() and begins shutdown when one arrives. os.Interrupt is the
//     portable carrier: on Windows the runtime synthesises it from the Ctrl-Break
//     that Stop sends, so a module shuts down identically on both platforms.
//
// Like sdk/channel, this seam depends only on the standard library, so the SDK
// stays dependency-free and the OS-specific difference is contained here.
package lifecycle
