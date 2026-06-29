// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSurface writes content as surface.json into a fresh temp dir and
// returns the dir. Test-only.
func writeSurface(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, SurfaceFileName), []byte(content), 0o644); err != nil {
		t.Fatalf("write surface.json: %v", err)
	}
	return dir
}

// TestLoadSurfaceFromDir_Surfaces verifies the canonical {"surfaces":[…]} form
// parses every surface in order with its own subject and survey/focus entries.
func TestLoadSurfaceFromDir_Surfaces(t *testing.T) {
	dir := writeSurface(t, `{
  "surfaces": [
    {
      "subject": "jail",
      "survey": { "entry": "fail2ban.jail.survey" },
      "focus":  { "entry": "fail2ban.jail.focus" }
    },
    {
      "subject": "ban",
      "survey": { "entry": "fail2ban.ban.survey" },
      "focus":  { "entry": "fail2ban.ban.focus" }
    }
  ]
}`)

	views, err := LoadSurfaceFromDir(dir)
	if err != nil {
		t.Fatalf("LoadSurfaceFromDir: %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("len(views) = %d, want 2", len(views))
	}
	if views[0].Subject != "jail" || views[1].Subject != "ban" {
		t.Errorf("subjects = %q,%q, want jail,ban", views[0].Subject, views[1].Subject)
	}
	if views[1].Survey.Entry != "fail2ban.ban.survey" {
		t.Errorf("views[1].Survey.Entry = %q, want fail2ban.ban.survey", views[1].Survey.Entry)
	}
}

// TestLoadSurfaceFromDir_ColumnFrom verifies a survey column's optional `from`
// source capability round-trips, and that a column without `from` decodes to an
// empty From (absent ⇒ the survey response sources the cell).
func TestLoadSurfaceFromDir_ColumnFrom(t *testing.T) {
	dir := writeSurface(t, `{
  "surfaces": [
    {
      "subject": "database",
      "survey": {
        "entry": "odoo.database.survey",
        "columns": [
          { "id": "modules", "label": "modules", "from": "odoo.database.overview" },
          { "id": "name",    "label": "name" }
        ]
      },
      "focus": { "entry": "odoo.database.focus" }
    }
  ]
}`)

	views, err := LoadSurfaceFromDir(dir)
	if err != nil {
		t.Fatalf("LoadSurfaceFromDir: %v", err)
	}
	cols := views[0].Survey.Columns
	if len(cols) != 2 {
		t.Fatalf("len(columns) = %d, want 2", len(cols))
	}
	if cols[0].From != "odoo.database.overview" {
		t.Errorf("cols[0].From = %q, want odoo.database.overview", cols[0].From)
	}
	if cols[1].From != "" {
		t.Errorf("cols[1].From = %q, want empty (no from declared)", cols[1].From)
	}
}

// TestLoadSurfaceFromDir_DuplicateSubject rejects two surfaces sharing a subject —
// surfaces are keyed by subject, so they must be unique within a module.
func TestLoadSurfaceFromDir_DuplicateSubject(t *testing.T) {
	dir := writeSurface(t, `{
  "surfaces": [
    { "subject": "jail", "survey": { "entry": "a.survey" }, "focus": { "entry": "a.focus" } },
    { "subject": "jail", "survey": { "entry": "b.survey" }, "focus": { "entry": "b.focus" } }
  ]
}`)

	_, err := LoadSurfaceFromDir(dir)
	if err == nil {
		t.Fatal("expected error for duplicate subject, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate subject") {
		t.Errorf("error = %q, want it to mention %q", err.Error(), "duplicate subject")
	}
}

// TestLoadSurfaceFromDir_MissingSubject verifies a surface.json without the
// subject field is rejected with a clear error.
func TestLoadSurfaceFromDir_MissingSubject(t *testing.T) {
	dir := writeSurface(t, `{
  "surfaces": [
    {
      "survey": { "entry": "nginx.domain.survey" },
      "focus":  { "entry": "nginx.domain.focus" }
    }
  ]
}`)

	_, err := LoadSurfaceFromDir(dir)
	if err == nil {
		t.Fatal("expected error for missing subject, got nil")
	}
	if !strings.Contains(err.Error(), "subject is required") {
		t.Errorf("error = %q, want it to mention %q", err.Error(), "subject is required")
	}
}

// TestLoadSurfaceFromDir_NestedDrills verifies a surface with nested drills
// parses recursively: the drill child round-trips its subject, label, and
// survey/focus entries, and a grandchild nests one level deeper.
func TestLoadSurfaceFromDir_NestedDrills(t *testing.T) {
	dir := writeSurface(t, `{
  "surfaces": [
    {
      "subject": "database",
      "survey": { "entry": "odoo.database.survey" },
      "focus":  { "entry": "odoo.database.focus" },
      "drills": [
        {
          "subject": "module", "label": "modules",
          "survey": { "entry": "odoo.module.survey" },
          "focus":  { "entry": "odoo.module.focus" },
          "drills": [
            {
              "subject": "model",
              "survey": { "entry": "odoo.model.survey" },
              "focus":  { "entry": "odoo.model.focus" }
            }
          ]
        }
      ]
    }
  ]
}`)

	views, err := LoadSurfaceFromDir(dir)
	if err != nil {
		t.Fatalf("LoadSurfaceFromDir: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("len(views) = %d, want 1 (drills must not be home roots)", len(views))
	}
	if len(views[0].Drills) != 1 {
		t.Fatalf("len(Drills) = %d, want 1", len(views[0].Drills))
	}
	child := views[0].Drills[0]
	if child.Subject != "module" || child.Label != "modules" {
		t.Errorf("child subject/label = %q/%q, want module/modules", child.Subject, child.Label)
	}
	if child.Survey.Entry != "odoo.module.survey" {
		t.Errorf("child Survey.Entry = %q, want odoo.module.survey", child.Survey.Entry)
	}
	if len(child.Drills) != 1 || child.Drills[0].Subject != "model" {
		t.Errorf("grandchild = %+v, want one drill with subject model", child.Drills)
	}
}

// TestLoadSurfaceFromDir_DuplicateSubjectInDrill rejects a drill whose subject
// collides with a root surface — subjects are unique module-wide, which forbids
// a surface from being both a home root and a drill child.
func TestLoadSurfaceFromDir_DuplicateSubjectInDrill(t *testing.T) {
	dir := writeSurface(t, `{
  "surfaces": [
    {
      "subject": "database",
      "survey": { "entry": "a.survey" }, "focus": { "entry": "a.focus" },
      "drills": [
        { "subject": "database", "survey": { "entry": "b.survey" }, "focus": { "entry": "b.focus" } }
      ]
    }
  ]
}`)

	_, err := LoadSurfaceFromDir(dir)
	if err == nil {
		t.Fatal("expected error for module-wide duplicate subject, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate subject") {
		t.Errorf("error = %q, want it to mention %q", err.Error(), "duplicate subject")
	}
}

// TestLoadSurfaceFromDir_DrillMissingFocus verifies validation recurses into
// drills — a drill missing focus.entry is rejected like a top-level surface.
func TestLoadSurfaceFromDir_DrillMissingFocus(t *testing.T) {
	dir := writeSurface(t, `{
  "surfaces": [
    {
      "subject": "database",
      "survey": { "entry": "a.survey" }, "focus": { "entry": "a.focus" },
      "drills": [
        { "subject": "module", "survey": { "entry": "b.survey" } }
      ]
    }
  ]
}`)

	_, err := LoadSurfaceFromDir(dir)
	if err == nil {
		t.Fatal("expected error for drill missing focus.entry, got nil")
	}
	if !strings.Contains(err.Error(), "focus.entry is required") {
		t.Errorf("error = %q, want it to mention %q", err.Error(), "focus.entry is required")
	}
}

// TestLoadSurfaceFromDir_Absent confirms a directory with no surface.json
// yields (nil, nil) — modules without a surface are valid.
func TestLoadSurfaceFromDir_Absent(t *testing.T) {
	views, err := LoadSurfaceFromDir(t.TempDir())
	if err != nil {
		t.Fatalf("LoadSurfaceFromDir: %v", err)
	}
	if len(views) != 0 {
		t.Errorf("got %d views, want 0", len(views))
	}
}
