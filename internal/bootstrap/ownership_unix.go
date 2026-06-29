// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package bootstrap

import (
	"os"
	"os/user"
	"strconv"
)

// chownToUser sets ownership of dir to the named system account and its primary
// group when that account exists. On Unix a service's writable directories are
// conventionally owned by the service user (here: the odoo account writes its
// own log alongside suctl.log). Missing user or chown failure is non-fatal —
// best effort, mirroring the rest of bootstrap.Run().
func chownToUser(dir, username string) {
	u, err := user.Lookup(username)
	if err != nil {
		return
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	os.Chown(dir, uid, gid) //nolint:errcheck
}
