// SPDX-License-Identifier: Apache-2.0

//go:build windows

package bootstrap

// chownToUser is a no-op on Windows: there is no uid/gid ownership model.
// Directories created under %ProgramData%\suctl inherit their ACL from the
// parent, which already grants service accounts the access they need. Any
// finer-grained ACL tuning belongs to the Windows service-registration seam.
func chownToUser(dir, username string) {}
