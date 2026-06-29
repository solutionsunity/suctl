// SPDX-License-Identifier: Apache-2.0

package installer

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
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

// TestCompareSemver covers the ordering the upgrade gate relies on: newer cores
// win, an explicit pin equal to the build is "not newer", and a pre-release
// ranks below the same final core.
func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v0.5.2", "v0.5.1", 1},
		{"v0.5.1", "v0.5.1", 0},
		{"v0.5.0", "v0.6.0", -1},
		{"v1.0.0", "v0.9.9", 1},
		{"v1.2.3", "v1.2.3-rc1", 1},  // final outranks pre-release
		{"v1.0.0-rc1", "v1.0.0", -1}, // pre-release ranks below final
		{"v1.0.0-rc1", "v1.0.0-rc2", -1},
	}
	for _, c := range cases {
		got, err := compareSemver(c.a, c.b)
		if err != nil {
			t.Fatalf("compareSemver(%q,%q): %v", c.a, c.b, err)
		}
		if got != c.want {
			t.Errorf("compareSemver(%q,%q)=%d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// TestParseSemver_Invalid: non-semver strings must surface an error rather than
// silently comparing as zero.
func TestParseSemver_Invalid(t *testing.T) {
	for _, v := range []string{"latest", "v1.2", "1.x.0", ""} {
		if _, _, err := parseSemver(v); err == nil {
			t.Errorf("parseSemver(%q): expected error, got nil", v)
		}
	}
}

// TestVerifySHA256: a matching digest verifies; a tampered file is rejected.
func TestVerifySHA256(t *testing.T) {
	dir := t.TempDir()
	payload := filepath.Join(dir, "suctl.tar.gz")
	if err := os.WriteFile(payload, []byte("release-bytes"), 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	sum := sha256.Sum256([]byte("release-bytes"))
	shaFile := payload + ".sha256"
	line := hex.EncodeToString(sum[:]) + "  suctl.tar.gz\n"
	if err := os.WriteFile(shaFile, []byte(line), 0o644); err != nil {
		t.Fatalf("write sha: %v", err)
	}
	if err := verifySHA256(payload, shaFile); err != nil {
		t.Fatalf("verifySHA256 on a good file: %v", err)
	}
	if err := os.WriteFile(payload, []byte("tampered"), 0o644); err != nil {
		t.Fatalf("rewrite payload: %v", err)
	}
	if err := verifySHA256(payload, shaFile); err == nil {
		t.Fatal("verifySHA256 must reject a tampered file")
	}
}

// TestExtractTarGz round-trips a small archive and confirms its contents land
// on disk with the recorded layout.
func TestExtractTarGz(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "a.tar.gz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	body := []byte("hi")
	hdr := &tar.Header{Name: "suctl-vX/suctl", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatalf("write body: %v", err)
	}
	tw.Close() //nolint:errcheck
	gz.Close() //nolint:errcheck
	f.Close()  //nolint:errcheck

	dest := filepath.Join(dir, "out")
	if err := extractArchive(archive, dest); err != nil {
		t.Fatalf("extractArchive: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "suctl-vX", "suctl"))
	if err != nil {
		t.Fatalf("read extracted: %v", err)
	}
	if string(got) != "hi" {
		t.Errorf("extracted content=%q, want %q", got, "hi")
	}
}

// TestExtractZip_SlipRejected: an entry whose path escapes the destination is
// refused (zip-slip / path traversal guard).
func TestExtractZip_SlipRejected(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "evil.zip")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("../escape")
	if err != nil {
		t.Fatalf("zip create entry: %v", err)
	}
	w.Write([]byte("x")) //nolint:errcheck
	zw.Close()           //nolint:errcheck
	f.Close()            //nolint:errcheck

	if err := extractArchive(archive, filepath.Join(dir, "out")); err == nil {
		t.Fatal("extractArchive must reject a traversal entry")
	}
}

// TestReplaceBinary_Unix swaps a "running" binary's directory entry and confirms
// the new content is live afterwards (the inode-swap path on Unix).
func TestReplaceBinary_Unix(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "suctl")
	if err := os.WriteFile(dst, []byte("old"), 0o755); err != nil {
		t.Fatalf("seed dst: %v", err)
	}
	src := filepath.Join(dir, "new")
	if err := os.WriteFile(src, []byte("new"), 0o755); err != nil {
		t.Fatalf("seed src: %v", err)
	}
	if err := replaceBinary(src, dst); err != nil {
		t.Fatalf("replaceBinary: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != "new" {
		t.Errorf("dst content=%q, want %q", got, "new")
	}
}
