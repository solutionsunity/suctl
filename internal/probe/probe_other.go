// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package probe

// runtimeWarnings is a no-op off Linux: there is no systemd D-Bus or journald to
// probe. The systemd-backed svc and sys.log operations are Linux-only; their
// unavailability on other platforms is a property of the build, not a runtime
// fault to warn about here.
func runtimeWarnings() []string {
	return nil
}
