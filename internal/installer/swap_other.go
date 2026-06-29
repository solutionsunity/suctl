// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package installer

import (
	"os"
	"path/filepath"
)

// replaceBinary installs the file at srcPath as dstPath. A running binary cannot
// be overwritten in place on Linux (open+truncate yields ETXTBSY), but its
// directory entry can be replaced: write the new content to a sibling temp file
// on the same filesystem, then rename it over dstPath. The rename is atomic and
// the running process keeps its original inode, so this is safe to call while the
// old binary is executing (e.g. `suctl upgrade` replacing itself).
func replaceBinary(srcPath, dstPath string) error {
	dir := filepath.Dir(dstPath)
	tmp, err := os.CreateTemp(dir, ".suctl-*.new")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	tmp.Close()              //nolint:errcheck
	defer os.Remove(tmpName) //nolint:errcheck // no-op once the rename succeeds

	if err := copyFile(srcPath, tmpName, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return err
	}
	return os.Rename(tmpName, dstPath)
}

// SweepStaleBinary is a no-op on Unix: the inode-swap replace leaves no leftover.
func SweepStaleBinary() {}
