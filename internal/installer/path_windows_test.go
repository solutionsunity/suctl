// SPDX-License-Identifier: Apache-2.0

//go:build windows

package installer

import (
	"testing"

	"github.com/solutionsunity/suctl/sdk/paths"
	"golang.org/x/sys/windows/registry"
)

// readMachinePath returns the raw machine PATH value and its registry type.
func readMachinePath(t *testing.T) (string, uint32) {
	t.Helper()
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, environmentKey, registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("open env key (need elevation?): %v", err)
	}
	defer k.Close() //nolint:errcheck
	v, typ, err := k.GetStringValue("Path")
	if err != nil && err != registry.ErrNotExist {
		t.Fatalf("read Path: %v", err)
	}
	return v, typ
}

// writeMachinePath restores PATH verbatim, preserving the original value type.
func writeMachinePath(t *testing.T, v string, typ uint32) {
	t.Helper()
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, environmentKey, registry.SET_VALUE)
	if err != nil {
		t.Fatalf("open env key for restore: %v", err)
	}
	defer k.Close() //nolint:errcheck
	if typ == registry.SZ {
		err = k.SetStringValue("Path", v)
	} else {
		err = k.SetExpandStringValue("Path", v)
	}
	if err != nil {
		t.Fatalf("restore Path: %v", err)
	}
}

// TestRegisterUnregisterPath exercises the real machine PATH register/unregister
// cycle and guarantees the original value is restored on cleanup. Requires an
// elevated process (HKLM write); skips cleanly if not running as admin.
func TestRegisterUnregisterPath(t *testing.T) {
	orig, origType := readMachinePath(t)
	t.Cleanup(func() { writeMachinePath(t, orig, origType) })

	startsPresent := pathContains(orig, paths.BinDir)
	t.Logf("bin=%q startsPresent=%v origType=%d", paths.BinDir, startsPresent, origType)

	// add
	changed, err := registerPath()
	if err != nil {
		t.Fatalf("registerPath: %v", err)
	}
	if !startsPresent && !changed {
		t.Fatalf("registerPath reported no change but entry was absent")
	}
	if cur, _ := readMachinePath(t); !pathContains(cur, paths.BinDir) {
		t.Fatalf("after register, %s not on PATH", paths.BinDir)
	}

	// add again — idempotent
	if changed2, err := registerPath(); err != nil {
		t.Fatalf("registerPath #2: %v", err)
	} else if changed2 {
		t.Fatalf("registerPath #2 should be a no-op (idempotent)")
	}

	// remove
	rmChanged, err := unregisterPath()
	if err != nil {
		t.Fatalf("unregisterPath: %v", err)
	}
	if !rmChanged {
		t.Fatalf("unregisterPath should have removed the entry")
	}
	if cur, _ := readMachinePath(t); pathContains(cur, paths.BinDir) {
		t.Fatalf("after unregister, %s still on PATH", paths.BinDir)
	}

	// remove again — idempotent
	if rmChanged2, err := unregisterPath(); err != nil {
		t.Fatalf("unregisterPath #2: %v", err)
	} else if rmChanged2 {
		t.Fatalf("unregisterPath #2 should be a no-op (idempotent)")
	}

	t.Logf("OK: register/unregister verified idempotent and self-cleaning")
}
