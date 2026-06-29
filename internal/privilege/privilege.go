// SPDX-License-Identifier: Apache-2.0

// Package privilege is the OS-agnostic seam for the administrative-privilege
// check suctl needs to install itself and manage system services.
//
// "Administrative" means root on Unix and an elevated Administrator token on
// Windows. Callers should branch on IsAdmin() and, when it is false, use
// EscalationHint() to tell the operator how to re-run with privilege in a way
// that reads correctly on the host OS ("sudo …" vs an elevated prompt).
package privilege

// IsAdmin reports whether the current process holds the OS administrative
// privilege required for install/uninstall and system-service management.
func IsAdmin() bool { return isAdmin() }

// EscalationHint returns the OS-appropriate way to re-run suctl with the
// required privilege, suitable for embedding in an operator-facing message.
func EscalationHint() string { return escalationHint() }
