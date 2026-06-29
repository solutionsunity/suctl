// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package privilege

import "os"

// isAdmin reports whether the effective user is root. Geteuid (not Getuid) is
// used so the check honours setuid execution.
func isAdmin() bool { return os.Geteuid() == 0 }

// escalationHint is the Unix way to re-run with privilege.
func escalationHint() string { return "sudo" }
