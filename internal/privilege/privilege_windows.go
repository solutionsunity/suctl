// SPDX-License-Identifier: Apache-2.0

//go:build windows

package privilege

import "golang.org/x/sys/windows"

// isAdmin reports whether the current process token is a member of the built-in
// Administrators group with the privilege enabled — i.e. the process is running
// elevated. It builds the well-known Administrators SID and tests the current
// process token (the 0 pseudo-handle) for membership.
func isAdmin() bool {
	var sid *windows.SID
	if err := windows.AllocateAndInitializeSid(
		&windows.SECURITY_NT_AUTHORITY,
		2,
		windows.SECURITY_BUILTIN_DOMAIN_RID,
		windows.DOMAIN_ALIAS_RID_ADMINS,
		0, 0, 0, 0, 0, 0,
		&sid,
	); err != nil {
		return false
	}
	defer windows.FreeSid(sid) //nolint:errcheck

	member, err := windows.Token(0).IsMember(sid)
	return err == nil && member
}

// escalationHint is the Windows way to re-run with privilege.
func escalationHint() string { return "an elevated (Run as administrator) prompt" }
