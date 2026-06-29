// SPDX-License-Identifier: Apache-2.0

package activation

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// --------------------------------------------------------------------------
// IsActivated
// --------------------------------------------------------------------------

func TestIsActivated_NotPresent(t *testing.T) {
	dir := t.TempDir()
	ok, err := IsActivated(dir, "nginx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected not activated for missing flag file")
	}
}

func TestIsActivated_Present(t *testing.T) {
	dir := t.TempDir()
	if err := Activate(dir, "nginx"); err != nil {
		t.Fatal(err)
	}
	ok, err := IsActivated(dir, "nginx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected activated after Activate()")
	}
}

func TestIsActivated_EmptyName_Error(t *testing.T) {
	_, err := IsActivated(t.TempDir(), "")
	if err == nil {
		t.Error("expected error for empty module name")
	}
}

// --------------------------------------------------------------------------
// Activate
// --------------------------------------------------------------------------

func TestActivate_CreatesFlag(t *testing.T) {
	dir := t.TempDir()
	if err := Activate(dir, "nginx"); err != nil {
		t.Fatalf("Activate error: %v", err)
	}
	ok, _ := IsActivated(dir, "nginx")
	if !ok {
		t.Error("flag not created by Activate")
	}
}

func TestActivate_Idempotent(t *testing.T) {
	dir := t.TempDir()
	if err := Activate(dir, "nginx"); err != nil {
		t.Fatal(err)
	}
	// Second call must not error.
	if err := Activate(dir, "nginx"); err != nil {
		t.Errorf("second Activate should be no-op, got error: %v", err)
	}
}

func TestActivate_CreatesStateDirIfMissing(t *testing.T) {
	// Use a sub-directory that doesn't exist yet.
	parent := t.TempDir()
	dir := parent + "/modules"
	if err := Activate(dir, "nginx"); err != nil {
		t.Fatalf("Activate with missing dir: %v", err)
	}
	ok, _ := IsActivated(dir, "nginx")
	if !ok {
		t.Error("flag not created in newly-created stateDir")
	}
}

func TestActivate_EmptyName_Error(t *testing.T) {
	if err := Activate(t.TempDir(), ""); err == nil {
		t.Error("expected error for empty module name")
	}
}

// --------------------------------------------------------------------------
// Deactivate
// --------------------------------------------------------------------------

func TestDeactivate_RemovesFlag(t *testing.T) {
	dir := t.TempDir()
	Activate(dir, "nginx")
	if err := Deactivate(dir, "nginx"); err != nil {
		t.Fatalf("Deactivate error: %v", err)
	}
	ok, _ := IsActivated(dir, "nginx")
	if ok {
		t.Error("flag still present after Deactivate")
	}
}

func TestDeactivate_Idempotent(t *testing.T) {
	dir := t.TempDir()
	// Not activated — should not error.
	if err := Deactivate(dir, "nginx"); err != nil {
		t.Errorf("Deactivate on non-existent flag should be no-op, got: %v", err)
	}
}

func TestDeactivate_EmptyName_Error(t *testing.T) {
	if err := Deactivate(t.TempDir(), ""); err == nil {
		t.Error("expected error for empty module name")
	}
}

// --------------------------------------------------------------------------
// List
// --------------------------------------------------------------------------

func TestList_Empty(t *testing.T) {
	dir := t.TempDir()
	names, err := List(dir)
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("expected empty list, got %v", names)
	}
}

func TestList_MissingDir(t *testing.T) {
	names, err := List(t.TempDir() + "/nonexistent")
	if err != nil {
		t.Fatalf("missing dir should return nil, not error: %v", err)
	}
	if names != nil {
		t.Errorf("expected nil, got %v", names)
	}
}

func TestList_ReturnsActivatedNames(t *testing.T) {
	dir := t.TempDir()
	Activate(dir, "nginx")
	Activate(dir, "fail2ban")
	Activate(dir, "certbot")

	names, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	want := []string{"certbot", "fail2ban", "nginx"}
	if len(names) != len(want) {
		t.Fatalf("List len = %d; want %d: %v", len(names), len(want), names)
	}
	for i, n := range names {
		if n != want[i] {
			t.Errorf("names[%d] = %q; want %q", i, n, want[i])
		}
	}
}

func TestList_IgnoresSubdirs(t *testing.T) {
	dir := t.TempDir()
	Activate(dir, "nginx")
	// Create a sub-directory — should not appear in List.
	if err := (func() error {
		return nil
	})(); err != nil {
		t.Fatal(err)
	}
	names, _ := List(dir)
	for _, n := range names {
		if n == "" {
			t.Error("List returned empty string name")
		}
	}
}

// --------------------------------------------------------------------------
// Checksum
// --------------------------------------------------------------------------

// writeModule lays out a module directory with the given relpath→content files
// and returns its path. Files are created with 0644 unless the path is marked
// executable by the caller via a later chmod.
func writeModule(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestChecksum_Deterministic(t *testing.T) {
	dir := writeModule(t, map[string]string{
		"manifest.json": `{"name":"nginx"}`,
		"hooks/on-start": "#!/bin/sh\necho hi\n",
	})
	a, err := Checksum(dir)
	if err != nil {
		t.Fatalf("Checksum error: %v", err)
	}
	b, err := Checksum(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("Checksum not deterministic: %q != %q", a, b)
	}
}

func TestChecksum_ContentChangeDiffers(t *testing.T) {
	d1 := writeModule(t, map[string]string{"manifest.json": `{"v":1}`})
	d2 := writeModule(t, map[string]string{"manifest.json": `{"v":2}`})
	c1, _ := Checksum(d1)
	c2, _ := Checksum(d2)
	if c1 == c2 {
		t.Error("expected different checksums for different content")
	}
}

func TestChecksum_ModeChangeDiffers(t *testing.T) {
	dir := writeModule(t, map[string]string{"hooks/on-start": "#!/bin/sh\n"})
	before, _ := Checksum(dir)
	if err := os.Chmod(filepath.Join(dir, "hooks/on-start"), 0755); err != nil {
		t.Fatal(err)
	}
	after, _ := Checksum(dir)
	if before == after {
		t.Error("expected different checksum after exec-bit flip")
	}
}

func TestChecksum_MtimeIgnored(t *testing.T) {
	d1 := writeModule(t, map[string]string{"manifest.json": `{"v":1}`})
	d2 := writeModule(t, map[string]string{"manifest.json": `{"v":1}`})
	old := time.Unix(1000, 0)
	if err := os.Chtimes(filepath.Join(d2, "manifest.json"), old, old); err != nil {
		t.Fatal(err)
	}
	c1, _ := Checksum(d1)
	c2, _ := Checksum(d2)
	if c1 != c2 {
		t.Error("checksum must ignore mtime: identical content differed")
	}
}

func TestChecksum_FileSetDiffers(t *testing.T) {
	d1 := writeModule(t, map[string]string{"manifest.json": `{}`})
	d2 := writeModule(t, map[string]string{"manifest.json": `{}`, "extra": "x"})
	c1, _ := Checksum(d1)
	c2, _ := Checksum(d2)
	if c1 == c2 {
		t.Error("expected different checksum when the file set changes")
	}
}

func TestChecksum_MissingDir_Error(t *testing.T) {
	if _, err := Checksum(t.TempDir() + "/nonexistent"); err == nil {
		t.Error("expected error for missing module directory")
	}
}

// --------------------------------------------------------------------------
// GetChecksum / SetChecksum
// --------------------------------------------------------------------------

func TestGetChecksum_MissingFlag_Empty(t *testing.T) {
	cs, err := GetChecksum(t.TempDir(), "nginx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cs != "" {
		t.Errorf("expected empty checksum for missing flag, got %q", cs)
	}
}

func TestGetChecksum_EmptyFlag_Empty(t *testing.T) {
	dir := t.TempDir()
	if err := Activate(dir, "nginx"); err != nil {
		t.Fatal(err)
	}
	cs, err := GetChecksum(dir, "nginx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cs != "" {
		t.Errorf("expected empty checksum for legacy empty flag, got %q", cs)
	}
}

func TestSetChecksum_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := SetChecksum(dir, "nginx", "abc123"); err != nil {
		t.Fatalf("SetChecksum error: %v", err)
	}
	cs, err := GetChecksum(dir, "nginx")
	if err != nil {
		t.Fatal(err)
	}
	if cs != "abc123" {
		t.Errorf("round-trip mismatch: got %q want %q", cs, "abc123")
	}
}

func TestSetChecksum_MarksActivated(t *testing.T) {
	dir := t.TempDir()
	if err := SetChecksum(dir, "nginx", "abc123"); err != nil {
		t.Fatal(err)
	}
	ok, _ := IsActivated(dir, "nginx")
	if !ok {
		t.Error("SetChecksum should create the flag, marking the module activated")
	}
}

func TestSetChecksum_CreatesStateDirIfMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "modules")
	if err := SetChecksum(dir, "nginx", "abc123"); err != nil {
		t.Fatalf("SetChecksum with missing dir: %v", err)
	}
	cs, _ := GetChecksum(dir, "nginx")
	if cs != "abc123" {
		t.Errorf("got %q want %q", cs, "abc123")
	}
}

func TestSetChecksum_Overwrites(t *testing.T) {
	dir := t.TempDir()
	if err := SetChecksum(dir, "nginx", "old"); err != nil {
		t.Fatal(err)
	}
	if err := SetChecksum(dir, "nginx", "new"); err != nil {
		t.Fatal(err)
	}
	cs, _ := GetChecksum(dir, "nginx")
	if cs != "new" {
		t.Errorf("expected overwrite to win, got %q", cs)
	}
}

func TestGetChecksum_EmptyName_Error(t *testing.T) {
	if _, err := GetChecksum(t.TempDir(), ""); err == nil {
		t.Error("expected error for empty module name")
	}
}

func TestSetChecksum_EmptyName_Error(t *testing.T) {
	if err := SetChecksum(t.TempDir(), "", "abc"); err == nil {
		t.Error("expected error for empty module name")
	}
}
