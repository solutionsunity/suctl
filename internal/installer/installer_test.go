// SPDX-License-Identifier: Apache-2.0

package installer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/solutionsunity/suctl/sdk/paths"
)

// TestSamePath_IdenticalFile: the same existing file by two spellings resolves
// equal (EvalSymlinks on both sides canonicalises to one truth).
func TestSamePath_IdenticalFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "suctl")
	if err := os.WriteFile(f, []byte("x"), 0o755); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if !samePath(f, filepath.Join(dir, ".", "suctl")) {
		t.Fatal("identical file by two spellings should match")
	}
}

// TestSamePath_Symlink: a symlink and its target are the same file — the gate
// must treat a symlinked launch as the installed binary.
func TestSamePath_Symlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "suctl")
	if err := os.WriteFile(target, []byte("x"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(dir, "suctl-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable on this host: %v", err)
	}
	if !samePath(link, target) {
		t.Fatal("symlink and its target should match")
	}
}

// TestSamePath_Distinct: two different existing files never match.
func TestSamePath_Distinct(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	for _, f := range []string{a, b} {
		if err := os.WriteFile(f, []byte("x"), 0o755); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	if samePath(a, b) {
		t.Fatal("distinct files must not match")
	}
}

// TestSamePath_Missing: when a path cannot be resolved (e.g. SuctlBin when suctl
// is not installed) samePath falls back to the cleaned form — identical cleaned
// inputs match, different ones do not.
func TestSamePath_Missing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if !samePath(missing, missing) {
		t.Fatal("identical missing paths should match via clean fallback")
	}
	if samePath(missing, missing+"-other") {
		t.Fatal("different missing paths must not match")
	}
}

// TestIsInstalledBinary_ReleaseCopy: the test binary lives in a temp dir, not at
// paths.SuctlBin, so it is a "release copy" — the gate must report false. (Skips
// in the unlikely event the test binary IS the installed path.)
func TestIsInstalledBinary_ReleaseCopy(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable unavailable: %v", err)
	}
	if samePath(exe, paths.SuctlBin) {
		t.Skip("test binary happens to be the installed binary; gate trivially true")
	}
	if IsInstalledBinary() {
		t.Fatal("a release copy must not be reported as the installed binary")
	}
}
