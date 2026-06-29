// SPDX-License-Identifier: Apache-2.0

// Package repl — correlation_test.go covers the REPL orchestrator's inbound
// correlation: the order-independent parking area that makes async survey
// completion deterministic regardless of whether the terminal job_update beats
// the async accept (the fast-survey race) or follows it. It also covers the
// "sync is the degenerate case of async" projection: an inline result and a
// pushed terminal "done" carry the same output and paint through one path.
package repl

import (
	"encoding/json"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	surfacecore "github.com/solutionsunity/suctl/internal/surface"
	"github.com/solutionsunity/suctl/sdk/manifest"
	"github.com/solutionsunity/suctl/sdk/protocol"
	"github.com/solutionsunity/suctl/sdk/surface"
	sdksystem "github.com/solutionsunity/suctl/sdk/system"
)

// doneParams is a terminal "done" job_update carrying a two-subject survey —
// the same {subjects,…} payload a sync invoke returns inline.
func doneParams() protocol.JobUpdateParams {
	return protocol.JobUpdateParams{State: "done", Output: json.RawMessage(
		`{"total":2,"status_summary":"2 domains",` +
			`"subjects":[{"id":"a","name":"alpha"},{"id":"b","name":"beta"}]}`)}
}

// assertPainted asserts a mod has been projected with the two-subject survey.
func assertPainted(t *testing.T, mod *modSt) {
	t.Helper()
	if mod.Loading {
		t.Fatalf("terminal/inline result must clear the spinner")
	}
	if mod.Total != 2 || len(mod.Subjects) != 2 || mod.StatusSummary != "2 domains" {
		t.Fatalf("result must project the survey, got total=%d subjects=%d summary=%q",
			mod.Total, len(mod.Subjects), mod.StatusSummary)
	}
	if mod.Err != "" {
		t.Fatalf("success must leave no error, got %q", mod.Err)
	}
}

func TestApplyLoadAsyncAcceptHoldsSpinner(t *testing.T) {
	o := newOrchestrator(nil)
	mod := &modSt{ShortName: "nginx", Loading: true}
	o.applyLoad(moduleSurveyLoadedMsg{mod: mod, token: "tok-1", accepted: true})

	if !mod.Loading {
		t.Fatalf("async accept must hold the spinner (Loading), got false")
	}
	if mod.Subjects != nil {
		t.Fatalf("accept carries no inline rows, got %+v", mod.Subjects)
	}
	if o.known["tok-1"] != mod {
		t.Fatalf("accept must register token→mod in the orchestrator")
	}
}

func TestRegisterBeforeDone(t *testing.T) {
	o := newOrchestrator(nil)
	mod := &modSt{ShortName: "nginx", Loading: true}
	// accept arrives first (the slow-survey ordering), then the terminal done.
	o.applyLoad(moduleSurveyLoadedMsg{mod: mod, token: "tok-1", accepted: true})
	o.ingest("tok-1", doneParams())

	assertPainted(t, mod)
	if _, ok := o.known["tok-1"]; ok {
		t.Fatalf("terminal done must clear the known token")
	}
}

func TestDoneBeforeRegister(t *testing.T) {
	o := newOrchestrator(nil)
	mod := &modSt{ShortName: "nginx", Loading: true}
	// The fast-survey race: the terminal done arrives before the accept. It must
	// be parked, not dropped, then replayed the moment the accept registers.
	o.ingest("tok-1", doneParams())
	if mod.Loading != true {
		t.Fatalf("a parked update must not touch any mod yet")
	}
	o.applyLoad(moduleSurveyLoadedMsg{mod: mod, token: "tok-1", accepted: true})

	assertPainted(t, mod)
	if len(o.parked) != 0 || len(o.order) != 0 {
		t.Fatalf("register must drain and clear the parking area, got parked=%v", o.parked)
	}
	if _, ok := o.known["tok-1"]; ok {
		t.Fatalf("a parked terminal must clear the known token after replay")
	}
}

func TestProgressThenTerminalParkedReplayInOrder(t *testing.T) {
	o := newOrchestrator(nil)
	mod := &modSt{ShortName: "nginx", Loading: true}
	// Both arrive before the accept: running (ignored on apply) then done.
	o.ingest("tok-1", protocol.JobUpdateParams{State: "running", Progress: 50})
	o.ingest("tok-1", doneParams())
	o.applyLoad(moduleSurveyLoadedMsg{mod: mod, token: "tok-1", accepted: true})

	assertPainted(t, mod)
	if len(o.parked) != 0 {
		t.Fatalf("replay must clear parked, got %v", o.parked)
	}
}

func TestSupersededTokenDropped(t *testing.T) {
	o := newOrchestrator(nil)
	mod := &modSt{ShortName: "nginx", Loading: true}
	o.applyLoad(moduleSurveyLoadedMsg{mod: mod, token: "tok-1", accepted: true})

	// A fresh load supersedes the in-flight async survey: beginLoad forgets the
	// old token and resets the mod. (The returned Cmd is not run; face is nil.)
	o.beginLoad(mod)
	if _, ok := o.known["tok-1"]; ok {
		t.Fatalf("a fresh load must forget the prior in-flight token")
	}

	// A late terminal for the superseded token must not paint the reloading mod.
	o.ingest("tok-1", doneParams())
	if !mod.Loading || mod.Subjects != nil {
		t.Fatalf("a superseded terminal must not project, got loading=%v subjects=%d",
			mod.Loading, len(mod.Subjects))
	}
}

func TestApplyTerminalFailedSurfacesError(t *testing.T) {
	o := newOrchestrator(nil)
	mod := &modSt{ShortName: "nginx", Loading: true}
	o.applyLoad(moduleSurveyLoadedMsg{mod: mod, token: "tok-1", accepted: true})
	o.ingest("tok-1", protocol.JobUpdateParams{
		State: "failed",
		Error: &protocol.ErrorDetail{Code: "X", Message: "boom"},
	})

	if mod.Loading || mod.Err == "" {
		t.Fatalf("failed must clear the spinner and surface the error, got loading=%v err=%q",
			mod.Loading, mod.Err)
	}
	if _, ok := o.known["tok-1"]; ok {
		t.Fatalf("failed must clear the known token")
	}
}

func TestApplyLoadInlineProjects(t *testing.T) {
	o := newOrchestrator(nil)
	mod := &modSt{ShortName: "nginx", Loading: true}
	// Sync is the degenerate case: an inline (accepted=false) result projects
	// directly, with no token ever entering the parking area.
	sr := surface.SurveyResponse{Total: 2, StatusSummary: "2 domains",
		Subjects: []surface.Subject{{ID: "a", Name: "alpha"}, {ID: "b", Name: "beta"}}}
	o.applyLoad(surveyLoadedMsg(mod, sr, nil))

	assertPainted(t, mod)
	if len(o.known) != 0 || len(o.parked) != 0 {
		t.Fatalf("an inline result must not register or park anything")
	}
}

func TestParkedBoundedByCap(t *testing.T) {
	o := newOrchestrator(nil)
	// Park more distinct, never-claimed tokens than the cap; the parking area
	// must stay bounded by dropping the oldest.
	for i := 0; i < parkedCap+10; i++ {
		o.ingest(string(rune('a'+i%26))+itoa(i), doneParams())
	}
	if len(o.order) > parkedCap || len(o.parked) > parkedCap {
		t.Fatalf("parking area must stay bounded by cap=%d, got order=%d parked=%d",
			parkedCap, len(o.order), len(o.parked))
	}
}

// itoa is a tiny int→string helper to keep the eviction test dependency-free.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// ── Cell fill ──────────────────────────────────────────────────────────────
// These cover the face-side (mod,from) correlation: the same park-then-replay
// gap as the survey path, plus dedupe-by-`from` (columns sharing a source
// collapse to one call for the whole column) and the pending/error cell states.

// fakeFace is a minimal SurfaceInvoker that records each FillCells call and
// answers inline with a fixed row-keyed map, so a fillCellsCmd batch can be
// driven end-to-end without a broker.
type fakeFace struct {
	calls []string // capName, in call order
	rows  map[string]map[string]surface.Column
}

func (f *fakeFace) LoadSurvey(string, map[string]interface{}) (surfacecore.SurveyLoad, error) {
	return surfacecore.SurveyLoad{}, nil
}
func (f *fakeFace) LoadFocus(string, string) (surface.FocusResponse, error) {
	return surface.FocusResponse{}, nil
}
func (f *fakeFace) FillCells(capName string, _ map[string]interface{}) (surfacecore.CellLoad, error) {
	f.calls = append(f.calls, capName)
	return surfacecore.CellLoad{Token: "ct-" + capName, Rows: f.rows}, nil
}
func (f *fakeFace) InvokeAction(string, map[string]interface{}) error { return nil }
func (f *fakeFace) Inventory() (sdksystem.InventoryResponse, error) {
	return sdksystem.InventoryResponse{}, nil
}
func (f *fakeFace) Updates() <-chan surfacecore.JobUpdate { return nil }

// cellMod builds a two-row survey whose two columns share one `from` — the
// compound-source shape (one call/row fills both).
func cellMod() *modSt {
	sc := &manifest.SurfaceConfig{}
	sc.Survey.Columns = []manifest.SurfaceColumnConfig{
		{ID: "modules", Label: "modules", From: "odoo.database.overview"},
		{ID: "users", Label: "users", From: "odoo.database.overview"},
	}
	return &modSt{
		ShortName:     "odoo",
		SurfaceConfig: sc,
		Subjects: []map[string]interface{}{
			{"id": "db1", "name": "db1", "columns": map[string]interface{}{}},
			{"id": "db2", "name": "db2", "columns": map[string]interface{}{}},
		},
	}
}

// doneCellParams is a terminal "done" job_update carrying the row-keyed map a
// `from` cap returns for the whole column — the cell-fill peer of doneParams.
func doneCellParams() protocol.JobUpdateParams {
	return protocol.JobUpdateParams{State: "done", Output: json.RawMessage(
		`{"rows":{"db1":{"columns":{"modules":{"value":"1/2","color":"ok"},"users":{"value":"3/4"}}},` +
			`"db2":{"columns":{"modules":{"value":"5/6"},"users":{"value":"7/8"}}}}}`)}
}

// drainBatch runs a tea.Batch cmd one level deep and collects the cellLoadedMsgs
// its sub-commands produce — enough to drive fillCellsCmd to completion.
func drainBatch(cmd tea.Cmd) []cellLoadedMsg {
	var out []cellLoadedMsg
	if cmd == nil {
		return out
	}
	switch mm := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range mm {
			if c == nil {
				continue
			}
			if cl, ok := c().(cellLoadedMsg); ok {
				out = append(out, cl)
			}
		}
	case cellLoadedMsg:
		out = append(out, mm)
	}
	return out
}

func TestFillCellsDedupesByFrom(t *testing.T) {
	ff := &fakeFace{rows: map[string]map[string]surface.Column{
		"db1": {"modules": {Value: "1/2", Color: "ok"}, "users": {Value: "3/4"}},
		"db2": {"modules": {Value: "5/6"}, "users": {Value: "7/8"}},
	}}
	o := newOrchestrator(ff)
	mod := cellMod()

	cmd := o.fillCellsCmd(mod)
	if !mod.hasPendingCells() {
		t.Fatalf("fillCellsCmd must mark cells pending synchronously")
	}
	for _, m := range drainBatch(cmd) {
		o.applyCellLoad(m)
	}

	// One distinct `from` for the whole column = one call, regardless of row count
	// (columns collapse and rows fold into the single row-keyed map).
	if len(ff.calls) != 1 {
		t.Fatalf("columns sharing a from must collapse to one whole-column call, got %d calls", len(ff.calls))
	}
	if mod.hasPendingCells() {
		t.Fatalf("every cell on every row must be filled after the batch applies")
	}
	colMap := mod.Subjects[0]["columns"].(map[string]interface{})
	if val, color := cellValue(colMap, "modules"); val != "1/2" || color != "ok" {
		t.Fatalf("inline fill must paint row 0 value+color, got %q/%q", val, color)
	}
	colMap2 := mod.Subjects[1]["columns"].(map[string]interface{})
	if val, _ := cellValue(colMap2, "modules"); val != "5/6" {
		t.Fatalf("one call must paint every row, got row 1 modules %q", val)
	}
}

func TestApplyCellAsyncRegisterThenDone(t *testing.T) {
	o := newOrchestrator(nil)
	mod := cellMod()
	o.applyCellLoad(cellLoadedMsg{mod: mod,
		capName: "odoo.database.overview", token: "ct-1", accepted: true})
	if _, ok := o.cellKnown["ct-1"]; !ok {
		t.Fatalf("async accept must register cellKnown token→target")
	}
	o.ingest("ct-1", doneCellParams())

	colMap := mod.Subjects[0]["columns"].(map[string]interface{})
	if val, _ := cellValue(colMap, "modules"); val != "1/2" {
		t.Fatalf("pushed done must paint row 0, got %q", val)
	}
	colMap2 := mod.Subjects[1]["columns"].(map[string]interface{})
	if val, _ := cellValue(colMap2, "modules"); val != "5/6" {
		t.Fatalf("pushed done must paint every row, got row 1 %q", val)
	}
	if _, ok := o.cellKnown["ct-1"]; ok {
		t.Fatalf("terminal done must clear the cell token")
	}
}

func TestApplyCellDoneBeforeRegister(t *testing.T) {
	o := newOrchestrator(nil)
	mod := cellMod()
	// The fast-fill race: the terminal done arrives before the accept. It must be
	// parked, then replayed across the rows the moment registerCell binds the token.
	o.ingest("ct-1", doneCellParams())
	o.applyCellLoad(cellLoadedMsg{mod: mod,
		capName: "odoo.database.overview", token: "ct-1", accepted: true})

	colMap := mod.Subjects[0]["columns"].(map[string]interface{})
	if val, _ := cellValue(colMap, "modules"); val != "1/2" {
		t.Fatalf("a parked cell done must replay across the rows, got %q", val)
	}
	if len(o.parked) != 0 || len(o.order) != 0 {
		t.Fatalf("registerCell must drain and clear the parking area, got %v", o.parked)
	}
}

func TestApplyCellFailedMarksError(t *testing.T) {
	o := newOrchestrator(nil)
	mod := cellMod()
	o.applyCellLoad(cellLoadedMsg{mod: mod,
		capName: "odoo.database.overview", token: "ct-1", accepted: true})
	o.ingest("ct-1", protocol.JobUpdateParams{State: "failed",
		Error: &protocol.ErrorDetail{Code: "X", Message: "boom"}})

	colMap := mod.Subjects[0]["columns"].(map[string]interface{})
	if _, errored := cellState(colMap, "modules"); !errored {
		t.Fatalf("a failed cell must mark every shared column errored")
	}
	colMap2 := mod.Subjects[1]["columns"].(map[string]interface{})
	if _, errored := cellState(colMap2, "modules"); !errored {
		t.Fatalf("a failed whole-column fill must mark every row errored")
	}
	if _, ok := o.cellKnown["ct-1"]; ok {
		t.Fatalf("a failed terminal must clear the cell token")
	}
}

func TestFillCellsForgottenOnReload(t *testing.T) {
	o := newOrchestrator(nil)
	mod := cellMod()
	o.applyCellLoad(cellLoadedMsg{mod: mod,
		capName: "odoo.database.overview", token: "ct-1", accepted: true})

	// A fresh load supersedes in-flight cell fills: forget drops the cell token so
	// a late terminal cannot paint the reloading rows. (The Cmd is not run.)
	o.beginLoad(mod)
	if _, ok := o.cellKnown["ct-1"]; ok {
		t.Fatalf("a fresh load must forget in-flight cell tokens")
	}
}
