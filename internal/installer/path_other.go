// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package installer

// On Unix the install directory is /usr/local/bin, which is already on PATH, so
// PATH registration is a no-op. These stubs keep Run/Uninstall platform-neutral.

// registerPath does nothing on non-Windows platforms. Returns (false, nil).
func registerPath() (bool, error) { return false, nil }

// unregisterPath does nothing on non-Windows platforms. Returns (false, nil).
func unregisterPath() (bool, error) { return false, nil }
