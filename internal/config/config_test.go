// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConf(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "suctl.conf")
	if err != nil {
		t.Fatalf("create temp conf: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("write temp conf: %v", err)
	}
	f.Close()
	return f.Name()
}

func TestLoadFrom_MissingFile_ReturnsDefaults(t *testing.T) {
	c := LoadFrom(filepath.Join(t.TempDir(), "nonexistent.conf"))
	if c.LogoURL != DefaultLogoURL {
		t.Errorf("LogoURL = %q; want %q", c.LogoURL, DefaultLogoURL)
	}
	if c.ContactURL != DefaultContactURL {
		t.Errorf("ContactURL = %q; want %q", c.ContactURL, DefaultContactURL)
	}
	if len(c.ModulePaths) != 0 {
		t.Errorf("ModulePaths = %v; want empty", c.ModulePaths)
	}
}

func TestLoadFrom_AllFields(t *testing.T) {
	yaml := `
logo_url: https://example.com/logo.png
contact_url: https://example.com/contact
module_paths:
  - /opt/custom/modules
  - /usr/local/lib/extra
`
	c := LoadFrom(writeConf(t, yaml))

	if c.LogoURL != "https://example.com/logo.png" {
		t.Errorf("LogoURL = %q", c.LogoURL)
	}
	if c.ContactURL != "https://example.com/contact" {
		t.Errorf("ContactURL = %q", c.ContactURL)
	}
	if len(c.ModulePaths) != 2 {
		t.Fatalf("ModulePaths len = %d; want 2", len(c.ModulePaths))
	}
	if c.ModulePaths[0] != "/opt/custom/modules" {
		t.Errorf("ModulePaths[0] = %q", c.ModulePaths[0])
	}
	if c.ModulePaths[1] != "/usr/local/lib/extra" {
		t.Errorf("ModulePaths[1] = %q", c.ModulePaths[1])
	}
}

func TestLoadFrom_PartialFile_DefaultsPreserved(t *testing.T) {
	yaml := "logo_url: https://custom.example/logo.png\n"
	c := LoadFrom(writeConf(t, yaml))

	if c.LogoURL != "https://custom.example/logo.png" {
		t.Errorf("LogoURL = %q", c.LogoURL)
	}
	// ContactURL not in file → default preserved
	if c.ContactURL != DefaultContactURL {
		t.Errorf("ContactURL = %q; want default %q", c.ContactURL, DefaultContactURL)
	}
}

func TestLoadFrom_EmptyLogoURL_RestoresDefault(t *testing.T) {
	yaml := "logo_url: \"\"\n"
	c := LoadFrom(writeConf(t, yaml))
	if c.LogoURL != DefaultLogoURL {
		t.Errorf("LogoURL = %q; want default restored", c.LogoURL)
	}
}

func TestLoadFrom_EmptyContactURL_RestoresDefault(t *testing.T) {
	yaml := "contact_url: \"\"\n"
	c := LoadFrom(writeConf(t, yaml))
	if c.ContactURL != DefaultContactURL {
		t.Errorf("ContactURL = %q; want default restored", c.ContactURL)
	}
}

func TestLoadFrom_ModulePaths_Empty(t *testing.T) {
	yaml := "logo_url: https://example.com/logo.png\n"
	c := LoadFrom(writeConf(t, yaml))
	if len(c.ModulePaths) != 0 {
		t.Errorf("ModulePaths = %v; want nil or empty", c.ModulePaths)
	}
}

