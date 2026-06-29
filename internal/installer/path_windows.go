// SPDX-License-Identifier: Apache-2.0

//go:build windows

package installer

import (
	"strings"
	"unsafe"

	"github.com/solutionsunity/suctl/sdk/paths"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// environmentKey is the machine-wide environment block in the registry. The
// machine PATH is the REG_EXPAND_SZ "Path" value here — distinct from the
// per-user PATH under HKCU. suctl installs machine-wide (admin), so it edits
// this value, never the process PATH: the latter is the merged user+machine
// view and writing it back would duplicate user entries into the machine block.
const environmentKey = `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`

// registerPath adds the suctl bin directory to the machine PATH if it is not
// already present. Returns true when the value was changed. Idempotent.
func registerPath() (bool, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, environmentKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return false, err
	}
	defer k.Close() //nolint:errcheck

	cur, _, err := k.GetStringValue("Path")
	if err != nil && err != registry.ErrNotExist {
		return false, err
	}
	if pathContains(cur, paths.BinDir) {
		return false, nil
	}

	updated := cur
	if updated != "" && !strings.HasSuffix(updated, ";") {
		updated += ";"
	}
	updated += paths.BinDir

	// SetExpandStringValue preserves REG_EXPAND_SZ so existing %VAR% entries in
	// PATH keep expanding for every process that reads it.
	if err := k.SetExpandStringValue("Path", updated); err != nil {
		return false, err
	}
	broadcastEnvChange()
	return true, nil
}

// unregisterPath removes the suctl bin directory from the machine PATH if it is
// present. Returns true when the value was changed. Idempotent.
func unregisterPath() (bool, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, environmentKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return false, err
	}
	defer k.Close() //nolint:errcheck

	cur, _, err := k.GetStringValue("Path")
	if err != nil {
		if err == registry.ErrNotExist {
			return false, nil
		}
		return false, err
	}

	parts := strings.Split(cur, ";")
	kept := parts[:0]
	removed := false
	for _, p := range parts {
		if strings.EqualFold(strings.TrimSpace(p), paths.BinDir) {
			removed = true
			continue
		}
		kept = append(kept, p)
	}
	if !removed {
		return false, nil
	}

	if err := k.SetExpandStringValue("Path", strings.Join(kept, ";")); err != nil {
		return false, err
	}
	broadcastEnvChange()
	return true, nil
}

// pathContains reports whether dir is already one of the ;-separated entries in
// path, compared case-insensitively and ignoring surrounding spaces.
func pathContains(path, dir string) bool {
	for _, p := range strings.Split(path, ";") {
		if strings.EqualFold(strings.TrimSpace(p), dir) {
			return true
		}
	}
	return false
}

// broadcastEnvChange notifies running processes that the environment changed so
// new shells pick up PATH without a reboot. Best-effort: any failure is ignored.
func broadcastEnvChange() {
	const (
		hwndBroadcast   = 0xffff
		wmSettingChange = 0x001A
		smtoAbortIfHung = 0x0002
	)
	env, err := windows.UTF16PtrFromString("Environment")
	if err != nil {
		return
	}
	proc := windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")
	var result uintptr
	proc.Call( //nolint:errcheck
		uintptr(hwndBroadcast),
		uintptr(wmSettingChange),
		0,
		uintptr(unsafe.Pointer(env)),
		uintptr(smtoAbortIfHung),
		uintptr(5000),
		uintptr(unsafe.Pointer(&result)),
	)
}
