// SPDX-License-Identifier: Apache-2.0

// Package repl — data.go defines the data model: modSt (per-module
// state), focusSt (per-subject focus overlay), Action (unified focus +
// inline action), pendingAction (action ready to fire), and the typed
// helpers that the page code uses to interrogate them.
package repl

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"

	"github.com/solutionsunity/suctl/sdk/manifest"
	"github.com/solutionsunity/suctl/sdk/protocol"
	"github.com/solutionsunity/suctl/sdk/surface"
	sdksystem "github.com/solutionsunity/suctl/sdk/system"
)

// modSt holds runtime state for one REPL module. The struct is always
// dereferenced via *modSt so mutations propagate; never copy it.
type modSt struct {
	ShortName     string
	Desc          string
	SurveyCapName string
	FocusCapName  string
	SurfaceConfig *manifest.SurfaceConfig

	// Scope is the selected parent-row id passed to the survey of a drill
	// child. Empty on home-root mods; set on a child built by
	// childModSt so reload injects it as the survey "scope" argument.
	Scope string

	// Survey data — populated by the orchestrator; shared across HOME and SURVEY.
	// In-flight async correlation is owned by the orchestrator (token→mod), not
	// smeared onto the mod, so there is no per-mod pending token here.
	Loading       bool
	Spinner       spinner.Model
	Subjects      []map[string]interface{}
	InlineActions [][]Action
	// SurveyActions holds the survey-level bulk actions returned by the module
	// on the last load. The module returns the enabled subset of those declared
	// in surface.json survey.actions; core renders them in the exit row.
	SurveyActions []Action
	Total         int
	StatusSummary string
	Err           string
}

// focusSt is the per-subject focus overlay. It is held by focusPage
// (not by modSt) so popping focus discards it cleanly.
type focusSt struct {
	Loading     bool
	Spinner     spinner.Model
	Subject     map[string]interface{}
	SubjectName string
	Sections    []focusSection
	Actions     []Action
	ActionIdx   int
	Err         string
}

// Action is the unified action descriptor used both for inline actions
// (per subject row in SURVEY) and for focus actions (action row in FOCUS).
type Action struct {
	CapName     string
	Label       string
	Destructive bool
}

// pendingAction is an Action bound to a concrete subject and client,
// ready to be invoked. It flows through CONFIRM → RUNNING → RESULT.
// Ctx is carried so the result page can reach shared state when the
// originating page reloads. ExtraArgs carries optional invocation
// arguments beyond the subject id — e.g. {"confirm": true} for a
// cascade re-invocation.
type pendingAction struct {
	Action      Action
	SubjectID   string
	SubjectName string
	ShortName   string
	Ctx         *AppCtx
	ExtraArgs   map[string]interface{}
}

// actionResult holds the outcome of a completed action (RESULT level).
type actionResult struct {
	Ok      bool
	Message string
	Action  pendingAction
}

// focusField is one KV field inside a focus section.
type focusField struct {
	label     string
	value     string
	color     string
	fullWidth bool
}

// focusSection is one titled group of KV fields in the focus body.
type focusSection struct {
	title  string
	fields []focusField
}

// ── Conversion helpers — SDK DTOs to internal REPL types ───────────────────

// modStsFromInventory converts a wire-shape InventoryEntry into one live modSt
// per declared surface (multi-subject). The first surface keeps the module's
// bare short name as its row id; each subsequent surface is suffixed with its
// subject ("name:subject") so row ids stay unique while the wire identity
// remains the single module. A module with no parseable surface yields no rows.
func modStsFromInventory(e sdksystem.InventoryEntry) []*modSt {
	surfaces, _ := manifest.ParseSurface(e.SurfaceConfig)
	if len(surfaces) == 0 {
		return nil
	}
	out := make([]*modSt, 0, len(surfaces))
	for i := range surfaces {
		s := surfaces[i]
		tabID := e.ShortName
		if i > 0 {
			tabID = e.ShortName + ":" + s.Subject
		}
		out = append(out, &modSt{
			ShortName:     tabID,
			Desc:          e.Description,
			SurveyCapName: s.Survey.Entry,
			FocusCapName:  s.Focus.Entry,
			SurfaceConfig: &s,
			Loading:       true,
			Spinner:       newSpinner(),
		})
	}
	return out
}

// childModSt builds a drill-target modSt from a parent surface's drill config,
// scoped to the selected parent row id. It is owned by the pushed
// surveyPage — never registered in AppCtx.Mods — so popping the drill discards
// it. The scope is injected into the survey load (see surveyPage.reload) so the
// module returns only rows under the selected parent. ShortName embeds parent +
// subject + scope so survey-load messages route to this page uniquely and
// cannot collide with a registered module's bare short name.
func childModSt(parent *modSt, drill *manifest.SurfaceConfig, scope string) *modSt {
	return &modSt{
		ShortName:     parent.ShortName + "/" + drill.Subject + ":" + scope,
		Desc:          parent.Desc,
		SurveyCapName: drill.Survey.Entry,
		FocusCapName:  drill.Focus.Entry,
		SurfaceConfig: drill,
		Scope:         scope,
		Loading:       true,
		Spinner:       newSpinner(),
	}
}

// drillLabel returns the chip text for a drill — its explicit Label, or its
// Subject when no label is declared.
func drillLabel(d *manifest.SurfaceConfig) string {
	if d.Label != "" {
		return d.Label
	}
	return d.Subject
}

// newFocusStFromResponse converts a wire-shape FocusResponse into a focusSt.
func newFocusStFromResponse(resp surface.FocusResponse) *focusSt {
	st := &focusSt{
		SubjectName: resp.Name,
		Sections:    make([]focusSection, len(resp.Sections)),
		Actions:     make([]Action, len(resp.Actions)),
	}
	for i, s := range resp.Sections {
		st.Sections[i] = focusSection{
			title:  s.Title,
			fields: make([]focusField, len(s.Fields)),
		}
		for j, f := range s.Fields {
			color := ""
			if f.Color != nil {
				color = fmt.Sprintf("%v", f.Color)
			}
			st.Sections[i].fields[j] = focusField{
				label:     f.Label,
				value:     fmt.Sprintf("%v", f.Value),
				color:     color,
				fullWidth: f.FullWidth,
			}
		}
	}
	for i, a := range resp.Actions {
		st.Actions[i] = Action{
			CapName:     a.Capability,
			Label:       a.Label,
			Destructive: a.Destructive,
		}
	}
	return st
}

// SubjectsToMaps converts a slice of SDK subjects into the raw map form
// used by the internal REPL state.
func SubjectsToMaps(subjects []surface.Subject) []map[string]interface{} {
	out := make([]map[string]interface{}, len(subjects))
	for i, s := range subjects {
		m := map[string]interface{}{
			"id":   s.ID,
			"name": s.Name,
		}
		cols := make(map[string]interface{}, len(s.Columns))
		for k, v := range s.Columns {
			cols[k] = map[string]interface{}{
				"value": v.Value,
				"color": v.Color,
			}
		}
		m["columns"] = cols
		if len(s.Facets) > 0 {
			m["facets"] = s.Facets
		}
		out[i] = m
	}
	return out
}

// ActionsToInternal converts a slice of SDK actions into the internal REPL type.
func ActionsToInternal(actions []surface.Action) []Action {
	out := make([]Action, len(actions))
	for i, a := range actions {
		out[i] = Action{
			CapName:     a.Capability,
			Label:       a.Label,
			Destructive: a.Destructive,
		}
	}
	return out
}

type moduleSurveyLoadedMsg struct {
	// mod is the originating mod, carried by pointer so the orchestrator can
	// correlate and project without a registry lookup — this is what lets a
	// drill child (never registered in AppCtx.Mods) route home too.
	mod           *modSt
	subjects      []map[string]interface{}
	inlineActions [][]Action
	surveyActions []Action
	err           error
	total         int
	statusSummary string
	// token is the job_token the surface orchestrator minted for this load;
	// accepted is true when the module took the survey async, in which case the
	// result is not here yet — the orchestrator registers token→mod and holds the
	// spinner until the pushed completion correlates back. Both zero for an
	// inline (sync) load.
	token    string
	accepted bool
}

// surveyLoadedMsg adapts a decoded neutral survey response (whether it arrived
// inline on a sync invoke or on a pushed terminal job_update) into the face's
// render-model message. It is the single conversion shared by the orchestrator's
// loadSurveyCmd and apply paths, so sync and async paint through one projection.
func surveyLoadedMsg(mod *modSt, sr surface.SurveyResponse, surveyActions []surface.Action) moduleSurveyLoadedMsg {
	subjects := SubjectsToMaps(sr.Subjects)
	inlines := make([][]Action, len(sr.Subjects))
	for i, s := range sr.Subjects {
		acts := ActionsToInternal(s.InlineActions)
		defaultLabels(acts, mod.ShortName)
		inlines[i] = acts
	}
	surveyActs := ActionsToInternal(surveyActions)
	defaultLabels(surveyActs, mod.ShortName)
	return moduleSurveyLoadedMsg{
		mod:           mod,
		subjects:      subjects,
		inlineActions: inlines,
		surveyActions: surveyActs,
		total:         sr.Total,
		statusSummary: sr.StatusSummary,
	}
}

type moduleFocusLoadedMsg struct {
	shortName   string
	subjectName string
	sections    []focusSection
	actions     []Action
	err         error
}

// frameActionDoneMsg is sent when an action invocation completes.
// Empty err means success; non-empty is the error text to display.
// cascade is set when the handler returned CONFIRMATION_REQUIRED so the
// running page can route to the cascade confirm page instead of result.
// The payload type lives in sdk/protocol so sender and receiver
// share one definition.
type frameActionDoneMsg struct {
	shortName string
	err       string
	cascade   *protocol.CascadeDetail
}

// jobUpdateMsg carries one neutral job_update delivery from the surface
// orchestrator into the Update loop. The broker pushes it for a face-originated
// job; root re-arms the listener after each delivery so the stream stays live
// across page navigation. The face adapts params into its render model.
type jobUpdateMsg struct {
	token  string
	params protocol.JobUpdateParams
}

// cellLoadedMsg carries the outcome of one column's `from` capability fired for
// the whole column: the originating mod + capName (the distinct `from` the
// orchestrator fired once for all rows), and exactly one of — an inline row-keyed
// map, an async accept (token, accepted), or an error. The orchestrator folds it
// onto every row's shared columns; an async accept registers token→(mod,from) and
// replays any parked completion, mirroring the survey-load path.
type cellLoadedMsg struct {
	mod      *modSt
	capName  string
	rows     map[string]map[string]surface.Column
	err      error
	token    string
	accepted bool
}

// ── Subject helpers ──────────────────────────────────────────────────────

// subjectID returns the opaque id field from a subject map.
func subjectID(subj map[string]interface{}) string {
	id, _ := subj["id"].(string)
	if id == "" {
		id, _ = subj["name"].(string)
	}
	return id
}

// subjectLabel returns a human-readable label for a subject map.
func subjectLabel(subj map[string]interface{}) string {
	for _, key := range []string{"name", "id", "domain", "service", "database"} {
		if v, ok := subj[key]; ok {
			return fmt.Sprintf("%v", v)
		}
	}
	keys := make([]string, 0, len(subj))
	for k := range subj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		return fmt.Sprintf("%v", subj[keys[0]])
	}
	return "(unnamed)"
}

// subjectHeader returns the subject label for the column header.
func subjectHeader(rc *manifest.SurfaceConfig) string {
	if rc != nil && rc.Subject != "" {
		return rc.Subject
	}
	return "name"
}

// cellValue extracts the value and color token from a subject's column data map.
func cellValue(colMap map[string]interface{}, colID string) (string, string) {
	if colMap == nil {
		return "", ""
	}
	cd, _ := colMap[colID].(map[string]interface{})
	val, _ := cd["value"].(string)
	color, _ := cd["color"].(string)
	return val, color
}

// cellPendingKey / cellErrKey are the sentinel keys a column-data map carries
// while its `from` job is in flight or has failed. A pending cell renders the
// survey spinner; an errored cell renders the alert glyph; a cell with neither
// (and no value) renders absent ("—"). They are face-local render state, never
// part of the module contract — the correlation truth lives in the orchestrator,
// not smeared onto the row.
const (
	cellPendingKey = "_pending"
	cellErrKey     = "_err"
)

// markCellPending replaces a column's cell with the pending sentinel so the
// survey paints a spinner until the `from` job resolves.
func markCellPending(colMap map[string]interface{}, colID string) {
	colMap[colID] = map[string]interface{}{cellPendingKey: true}
}

// cellState reports whether a column's cell is pending (job in flight) or
// errored (typed-error terminal). Both false means it is a normal value/absent
// cell read via cellValue.
func cellState(colMap map[string]interface{}, colID string) (pending, errored bool) {
	cd, _ := colMap[colID].(map[string]interface{})
	if cd == nil {
		return false, false
	}
	pending, _ = cd[cellPendingKey].(bool)
	errored, _ = cd[cellErrKey].(bool)
	return pending, errored
}

// columnToMap converts a decoded surface.Column into the {value,color} render
// map a subject's columns map carries. Value/color are stringified (the render
// path asserts string) and omitted when nil so an absent value renders "—".
func columnToMap(c surface.Column) map[string]interface{} {
	m := map[string]interface{}{}
	if c.Value != nil {
		m["value"] = fmt.Sprintf("%v", c.Value)
	}
	if c.Color != nil {
		m["color"] = fmt.Sprintf("%v", c.Color)
	}
	return m
}

// fromColumns returns the ids of every survey column whose `from` is capName —
// the columns one cell-fill call fills together. Used to mark them pending, to
// paint the returned column-map, and to clear them on error or a dropped value.
func fromColumns(mod *modSt, capName string) []string {
	if mod.SurfaceConfig == nil {
		return nil
	}
	var ids []string
	for _, col := range mod.SurfaceConfig.Survey.Columns {
		if col.From == capName {
			ids = append(ids, col.ID)
		}
	}
	return ids
}

// fillCellColumns paints a resolved row-keyed map across every row. For each row
// every column sharing capName is filled from that row's map when present; a
// column the module omitted (or a row absent from the map entirely) is cleared to
// absent ("—") so a dropped value never leaves a forever-spinner.
func fillCellColumns(mod *modSt, capName string, rows map[string]map[string]surface.Column) {
	ids := fromColumns(mod, capName)
	if len(ids) == 0 {
		return
	}
	for _, subj := range mod.Subjects {
		colMap, _ := subj["columns"].(map[string]interface{})
		if colMap == nil {
			colMap = map[string]interface{}{}
			subj["columns"] = colMap
		}
		cols := rows[subjectID(subj)]
		for _, colID := range ids {
			if c, ok := cols[colID]; ok {
				colMap[colID] = columnToMap(c)
			} else {
				delete(colMap, colID)
			}
		}
	}
}

// applyCellError marks every column sharing capName on every row as errored so
// each paints the alert glyph. The whole column fails together — one `from`
// fired once for all rows — but other columns on those rows are untouched.
func applyCellError(mod *modSt, capName string) {
	ids := fromColumns(mod, capName)
	for _, subj := range mod.Subjects {
		colMap, _ := subj["columns"].(map[string]interface{})
		if colMap == nil {
			continue
		}
		for _, colID := range ids {
			colMap[colID] = map[string]interface{}{cellErrKey: true}
		}
	}
}

// hasPendingCells reports whether any cell on any row is still in flight. The
// spinner fan-out uses it to keep animating after the survey itself has resolved
// (Loading=false) while cells are still filling.
func (m *modSt) hasPendingCells() bool {
	for _, subj := range m.Subjects {
		colMap, _ := subj["columns"].(map[string]interface{})
		for colID := range colMap {
			if pending, _ := cellState(colMap, colID); pending {
				return true
			}
		}
	}
	return false
}

// visibleSubjectsAll applies facet filter (AND semantics) then text filter,
// returning indices into subjects that pass both. A subject passes the facet
// filter only if it carries ALL active facet values in its "facets" tag list.
// Empty activeFacets means no facet restriction; empty textFilter means no
// text restriction. T (the total shown in the badge) always comes from
// modSt.Total — this function only computes n (the visible count).
func visibleSubjectsAll(subjects []map[string]interface{}, activeFacets []string, textFilter string) []int {
	q := strings.ToLower(textFilter)
	var out []int
	for i, subj := range subjects {
		// Facet filter: subject must carry ALL active facet values (AND).
		if len(activeFacets) > 0 {
			tags, _ := subj["facets"].([]string)
			tagSet := make(map[string]bool, len(tags))
			for _, t := range tags {
				tagSet[t] = true
			}
			pass := true
			for _, af := range activeFacets {
				if !tagSet[af] {
					pass = false
					break
				}
			}
			if !pass {
				continue
			}
		}
		// Text filter.
		if q != "" {
			name, _ := subj["name"].(string)
			if strings.Contains(strings.ToLower(name), q) {
				out = append(out, i)
				continue
			}
			colMap, _ := subj["columns"].(map[string]interface{})
			matched := false
			for colID := range colMap {
				val, _ := cellValue(colMap, colID)
				if strings.Contains(strings.ToLower(val), q) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		out = append(out, i)
	}
	return out
}

// resolveSelectedKey re-resolves a stable subject id to a row index after refresh.
func resolveSelectedKey(subjects []map[string]interface{}, key string) int {
	if key == "" {
		return -1
	}
	for i, s := range subjects {
		id, _ := s["id"].(string)
		if id == key {
			return i
		}
	}
	return -1
}

// activeFacetValues returns the values to pass to the survey capability
// (AND-combined). An empty result means "no filter — return all".
func activeFacetValues(facets []manifest.SurfaceFacetConfig, active []bool) []string {
	var vals []string
	for i, f := range facets {
		if i < len(active) && active[i] {
			vals = append(vals, f.Value)
		}
	}
	return vals
}


