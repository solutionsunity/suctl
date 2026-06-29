// SPDX-License-Identifier: Apache-2.0

//go:build windows

package installer

import (
	"os"

	"github.com/solutionsunity/suctl/sdk/paths"
	"golang.org/x/sys/windows"
)

// replaceBinary installs the file at srcPath as dstPath on Windows. A running
// .exe cannot be overwritten, but it CAN be renamed within the same volume
// (MoveFileEx), so any existing dstPath is moved aside to dstPath+".old" first,
// then the new binary is written to the now-free dstPath. The old image stays
// locked until the running process exits, so it is scheduled for deletion at the
// next reboot and additionally swept best-effort at the next startup
// (SweepStaleBinary). Verified against a live machine.
func replaceBinary(srcPath, dstPath string) error {
	oldPath := dstPath + ".old"
	if _, err := os.Stat(dstPath); err == nil {
		os.Remove(oldPath) //nolint:errcheck // clear a prior leftover if unlocked
		if err := os.Rename(dstPath, oldPath); err != nil {
			return err
		}
		scheduleDeleteOnReboot(oldPath)
	}

	info, err := os.Stat(srcPath)
	if err != nil {
		return err
	}
	return copyFile(srcPath, dstPath, info.Mode())
}

// SweepStaleBinary removes a leftover suctl.exe.old from a previous self-replace.
// Best-effort: absent or still-locked leftovers are ignored (the reboot-scheduled
// delete is the guaranteed fallback). Called once at startup.
func SweepStaleBinary() {
	os.Remove(paths.SuctlBin + ".old") //nolint:errcheck
}

// scheduleDeleteOnReboot registers path for deletion on the next reboot via
// MoveFileEx(path, NULL, MOVEFILE_DELAY_UNTIL_REBOOT), which records a
// PendingFileRenameOperations entry the OS honours at boot. Best-effort.
func scheduleDeleteOnReboot(path string) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	windows.MoveFileEx(p, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT) //nolint:errcheck
}
