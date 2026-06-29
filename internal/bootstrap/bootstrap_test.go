// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/solutionsunity/suctl/internal/config"
	"github.com/solutionsunity/suctl/sdk/paths"
)

// TestConstantPaths verifies that every required path constant is defined and
// non-empty. This catches accidental zero-value regressions.
func TestConstantPaths(t *testing.T) {
	paths := map[string]string{
		"ConfigDir":          ConfigDir,
		"ServicesDir":        ServicesDir,
		"NginxDir":           NginxDir,
		"ModuleConfDir":      ModuleConfDir,
		"LogDir":             LogDir,
		"ModuleStateDir":     ModuleStateDir,
		"RunDir":             RunDir,
		"WebrootSuspended":   WebrootSuspended,
		"WebrootMaintenance": WebrootMaintenance,
	}
	for name, val := range paths {
		if val == "" {
			t.Errorf("constant %s is empty", name)
		}
	}
}

// TestNewPathConstants verifies the exact values of the four new Phase-1 paths
// so that any future rename is caught by a test failure, not a silent drift.
func TestNewPathConstants(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"ModuleConfDir", ModuleConfDir, paths.ModuleConfDir},
		{"ModuleStateDir", ModuleStateDir, paths.ModuleStateDir},
		{"RunDir", RunDir, paths.RunDir},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %q; want %q", tc.name, tc.got, tc.want)
		}
	}
}

// TestModuleConfDirUnderConfigDir verifies that module conf.d lives under
// the main config directory.
func TestModuleConfDirUnderConfigDir(t *testing.T) {
	if !strings.HasPrefix(ModuleConfDir, ConfigDir+"/") {
		t.Errorf("ModuleConfDir %q is not under ConfigDir %q", ModuleConfDir, ConfigDir)
	}
}

// TestRenderPage verifies that {{LOGO_URL}} and {{CONTACT_URL}} are replaced.
func TestRenderPage(t *testing.T) {
	cfg := &config.Config{
		LogoURL:    "https://example.com/logo.png",
		ContactURL: "https://example.com/contact",
	}
	tmpl := "logo={{LOGO_URL}} contact={{CONTACT_URL}}"
	got := renderPage(tmpl, cfg)
	want := "logo=https://example.com/logo.png contact=https://example.com/contact"
	if got != want {
		t.Errorf("renderPage = %q; want %q", got, want)
	}
}

// TestEnsurePage_CreatesFile verifies that ensurePage writes the file when it
// does not exist.
func TestEnsurePage_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "index.html")
	ensurePage(path, "<html>hello</html>")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if string(data) != "<html>hello</html>" {
		t.Errorf("content = %q; want <html>hello</html>", string(data))
	}
}

// TestEnsurePage_DoesNotOverwrite verifies that ensurePage leaves an existing
// file untouched (preserves operator customisations).
func TestEnsurePage_DoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.html")

	original := "<html>custom</html>"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	ensurePage(path, "<html>new content</html>")

	data, _ := os.ReadFile(path)
	if string(data) != original {
		t.Errorf("content changed to %q; want original %q preserved", string(data), original)
	}
}
