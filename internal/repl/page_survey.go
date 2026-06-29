// SPDX-License-Identifier: Apache-2.0

// Package repl — page_survey.go is the SURVEY page: subject rows for a
// single module (inventory or a user module). It is parameterised by a
// *modSt; the rendering is identical for every module.
package repl

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/solutionsunity/suctl/internal/theme"
)

// surveyHintRow is the bottom-of-box hint line on the SURVEY page. Keys
// wear a keycap chip so they read as bindings rather than prose.
func surveyHintRow() string {
	return viewHintRow(
		[2]string{"↑↓", "row"},
		[2]string{"←→/tab", "fields"},
		[2]string{"⏎", "focus or run"},
		[2]string{"esc", "home"},
		[2]string{"type", "filter"},
	)
}

type surveyPage struct {
	ctx          *AppCtx
	mod          *modSt
	cursor       rowCursor
	filter       string
	activeFacets []bool
	selectedKey  string
	// showSystem is a jobs-page-only toggle (default false).
	// When false, capabilities whose name starts with "system." are hidden.
	showSystem bool
	scrollOffset int
}

func newSurveyPage(ctx *AppCtx, mod *modSt) surveyPage {
	nf := 0
	if mod.SurfaceConfig != nil {
		nf = len(mod.SurfaceConfig.Survey.Facets)
	}
	return surveyPage{
		ctx:          ctx,
		mod:          mod,
		cursor:       rowCursor{kind: rowKindSubject},
		activeFacets: make([]bool, nf),
	}
}

func (p surveyPage) Init() tea.Cmd {
	// Always re-load on entry so the survey reflects current state.
	return p.reload()
}

// reload is the single point that kicks off a survey load for this page.
// For the jobs page it injects show_system so the server applies the filter;
// for a drill child it injects the parent-row scope so the module returns only
// the rows under the selected parent. No facets are sent — modules return all
// rows with facet tags; core filters locally (D68).
func (p surveyPage) reload() tea.Cmd {
	extra := map[string]interface{}{}
	if p.mod.ShortName == "jobs" {
		extra["show_system"] = p.showSystem
	}
	if p.mod.Scope != "" {
		extra["scope"] = p.mod.Scope
	}
	if len(extra) > 0 {
		return p.ctx.orch.beginLoad(p.mod, extra)
	}
	return p.ctx.orch.beginLoad(p.mod)
}

func (p surveyPage) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// WindowSizeMsg and spinner.TickMsg are handled centrally by root —
	// root writes ctx dimensions via Ctx() and fans out modSt spinners via
	// ctx.tickSpinners before delegating here. scrollOffset is clamped via
	// SyncScrolled after every delegation, so no inline syncScroll needed.
	switch m := msg.(type) {
	case spinner.TickMsg:
		// A drill child is not registered in AppCtx.Mods, so ctx.tickSpinners
		// (run centrally by root) never advances its loading spinner. Drive it
		// here so the child survey animates while it loads. Registered mods are
		// ticked centrally and skipped to avoid double-advancing.
		if p.mod.Loading && p.ctx.ModByShortName(p.mod.ShortName) == nil {
			var cmd tea.Cmd
			p.mod.Spinner, cmd = p.mod.Spinner.Update(m)
			return p, cmd
		}
		return p, nil
	case moduleSurveyLoadedMsg:
		// Projection is applied centrally by the orchestrator (root) before this
		// delegation; the survey page only re-resolves its stable selected key
		// against the freshly projected rows of its own mod.
		if m.mod == p.mod && p.selectedKey != "" {
			row := resolveSelectedKey(p.mod.Subjects, p.selectedKey)
			if row >= 0 {
				p.cursor.subjRow = row
			} else {
				p.cursor.subjRow = 0
				p.selectedKey = ""
			}
		}
		return p, nil
	case becameActiveMsg:
		// Re-read survey live on return from focus.
		return p, p.reload()
	case tea.KeyMsg:
		return p.handleKey(m)
	}
	return p, nil
}

// SyncScrolled re-clamps scrollOffset after a root-driven delegation so
// the visible window remains valid across resize and key events. The
// pointer-receiver syncScroll mutates the local copy in place.
func (p surveyPage) SyncScrolled() tea.Model {
	p.syncScroll()
	return p
}

func (p surveyPage) activeFacetValues() []string {
	if p.mod.SurfaceConfig == nil {
		return nil
	}
	return activeFacetValues(p.mod.SurfaceConfig.Survey.Facets, p.activeFacets)
}

func (p surveyPage) View() string {
	w := p.ctx.Width
	inner := innerWidth(p.ctx.Width)
	p.syncScroll()
	return viewTitleLine(p.ctx.Inventory, p.ctx.Hostname, p.ctx.IP, w, p.titleBarContent()) + p.viewBody(inner)
}

// titleBarContent builds the middle-band content for the survey title bar.
// Shows the module name (and loading state). Token is "ok".
func (p surveyPage) titleBarContent() titleBar {
	middle := p.mod.ShortName
	if p.mod.Loading {
		middle += " " + GlyphSep + " loading…"
	}
	return titleBar{Middle: middle, Token: "ok"}
}

// MinSize is the content-derived minimum terminal size for this page.
// Width is the larger of the footer hint width and the loaded column
// layout (name + data cols + inline-action buttons). When the module's
// repl config is not yet loaded only the footer drives the floor.
func (p surveyPage) MinSize() (int, int) {
	w := lipgloss.Width(surveyHintRow()) + 2
	if p.mod != nil && p.mod.SurfaceConfig != nil {
		bodyW := 12 // name column floor (gutter + min name)
		for _, col := range p.mod.SurfaceConfig.Survey.Columns {
			cw := col.Width
			if cw == 0 {
				cw = len([]rune(col.Label))
				if cw < 6 {
					cw = 6
				}
			}
			bodyW += cw + 2
		}
		if len(p.mod.InlineActions) > 0 {
			for _, act := range p.mod.InlineActions[0] {
				bodyW += len([]rune(act.Label)) + 4
			}
		}
		for i := range p.mod.SurfaceConfig.Drills {
			bodyW += drillChipWidth(drillLabel(&p.mod.SurfaceConfig.Drills[i]))
		}
		bodyW += 2 // box borders
		if bodyW > w {
			w = bodyW
		}
	}
	// Chrome row count depends on whether the facet row is rendered.
	h := 11 // title + top + filter + [facet] + mid + header + body(>=1) + mid + summary + exit + footer + bot
	if p.mod != nil && p.mod.SurfaceConfig != nil && len(p.mod.SurfaceConfig.Survey.Facets) > 0 {
		h++ // facet row
	}
	return w, h
}

// surveyChrome returns the number of non-body rows the survey page renders
// outside of body subject rows. Used both for syncScroll and viewBody so the
// body window stays consistent with the actual rendered chrome.
//
// Layout: title + boxTop + filter + [facet] + boxMid + header + body +
//         boxMid + summary + exit + footer + boxBot = 10 (no facet) or 11 (with facet).
func (p surveyPage) chromeRows() int {
	if p.mod != nil && p.mod.SurfaceConfig != nil && len(p.mod.SurfaceConfig.Survey.Facets) > 0 {
		return 11
	}
	return 10
}

// surveyBodyLimit is the number of subject rows that fit in the visible
// window. Reserves one extra row above the chrome so the title line cannot
// be pushed off-screen by alt-screen scroll on exact fits.
func (p surveyPage) bodyLimit() int {
	limit := p.ctx.Height - p.chromeRows() - 1
	if limit < 1 {
		limit = 1
	}
	return limit
}

// syncScroll ensures the scrollOffset is adjusted so that the selected
// subject row is visible, and that the visible window is always filled
// from the top — when the terminal grows the offset is pulled down so
// the body never leaves dead space below the last subject.
func (p *surveyPage) syncScroll() {
	limit := p.bodyLimit()
	total := len(visibleSubjectsAll(p.mod.Subjects, p.activeFacetValues(), p.filter))
	if p.cursor.kind == rowKindSubject {
		if p.cursor.subjRow < p.scrollOffset {
			p.scrollOffset = p.cursor.subjRow
		} else if p.cursor.subjRow >= p.scrollOffset+limit {
			p.scrollOffset = p.cursor.subjRow - limit + 1
		}
	}
	maxOffset := total - limit
	if maxOffset < 0 {
		maxOffset = 0
	}
	if p.scrollOffset > maxOffset {
		p.scrollOffset = maxOffset
	}
	if p.scrollOffset < 0 {
		p.scrollOffset = 0
	}
}

// Ctx returns the shared AppCtx so root can centrally update
// terminal dimensions on WindowSizeMsg.
func (p surveyPage) Ctx() *AppCtx { return p.ctx }

func (p surveyPage) viewBody(inner int) string {
	var b strings.Builder
	b.WriteString(boxTop(inner))

	onFilter := p.cursor.kind == rowKindFilter
	// Badge: shown whenever any filter is active (facets OR text). n is the
	// count after both filters; T is mod.Total — the module-reported total,
	// invariant across all filter changes (D68).
	activeFacets := p.activeFacetValues()
	badge := ""
	if p.filter != "" || len(activeFacets) > 0 {
		vis := visibleSubjectsAll(p.mod.Subjects, activeFacets, p.filter)
		badge = fmt.Sprintf("%d of %d", len(vis), p.mod.Total)
	}
	b.WriteString(viewFilterRow(p.filter, onFilter, inner, badge))

	if p.mod.SurfaceConfig != nil && len(p.mod.SurfaceConfig.Survey.Facets) > 0 {
		onFacet := p.cursor.kind == rowKindFacet
		labels := make([]string, len(p.mod.SurfaceConfig.Survey.Facets))
		for i, fc := range p.mod.SurfaceConfig.Survey.Facets {
			labels[i] = fc.Label
		}
		// p.activeFacets carries the toggled-on state; viewFacetRow renders
		// the ● prefix and green colour so active filters are always visible.
		b.WriteString(viewFacetRow(labels, p.cursor.facetIdx, p.activeFacets, onFacet, inner))
	}
	b.WriteString(boxMid(inner))

	b.WriteString(renderSurveyRows(p.mod, p.cursor, p.filter, activeFacets, inner, p.scrollOffset, p.bodyLimit()))
	b.WriteString(boxMid(inner))

	// Summary row — module's voice (not affected by free-text filter).
	summary := ""
	if !p.mod.Loading {
		if p.mod.StatusSummary != "" {
			summary = "   " + theme.Warn.Render(truncate(p.mod.StatusSummary, inner-4))
		} else if s := fallbackStatusSummary(p.mod); s != "" {
			summary = "   " + theme.Dim.Render(truncate(s, inner-4))
		}
	}
	b.WriteString(bRow(summary, inner))

	// Exit row — color-governed buttons (see page_home for rationale).
	// Layout: [← home] [refresh] [survey action …] [show/hide system (jobs)]
	exitSel := p.cursor.kind == rowKindExit
	homeBtn := theme.BtnIdleSafe.Render(" ← home ")
	refreshBtn := theme.BtnIdleSafe.Render(" refresh ")
	if exitSel {
		if p.cursor.fieldIdx == 0 {
			homeBtn = theme.FieldFocus.Render(" ← home ")
		} else if p.cursor.fieldIdx == 1 {
			refreshBtn = theme.FieldFocus.Render(" refresh ")
		}
	}
	exitRow := " " + homeBtn + " " + refreshBtn
	// Survey action buttons (fieldIdx 2 … 2+N-1).
	nSurveyActions := 0
	if p.mod != nil {
		nSurveyActions = len(p.mod.SurveyActions)
		for i, act := range p.mod.SurveyActions {
			fi := 2 + i
			actSel := exitSel && p.cursor.fieldIdx == fi
			lbl := " " + act.Label + " "
			var btn string
			switch {
			case actSel && act.Destructive:
				btn = theme.BtnSelectedDanger.Render(lbl)
			case actSel:
				btn = theme.FieldFocus.Render(lbl)
			case act.Destructive:
				btn = theme.BtnIdleDanger.Render(lbl)
			default:
				btn = theme.BtnIdleSafe.Render(lbl)
			}
			exitRow += " " + btn
		}
	}
	// Jobs page: add system toggle button (after survey actions).
	if p.mod.ShortName == "jobs" {
		toggleLabel := " show system "
		if p.showSystem {
			toggleLabel = " hide system "
		}
		toggleFi := 2 + nSurveyActions
		toggleBtn := theme.BtnIdleSafe.Render(toggleLabel)
		if exitSel && p.cursor.fieldIdx == toggleFi {
			toggleBtn = theme.FieldFocus.Render(toggleLabel)
		}
		exitRow += " " + toggleBtn
	}
	b.WriteString(bRow(exitRow, inner))
	b.WriteString(bRow(surveyHintRow(), inner))
	b.WriteString(boxBot(inner))
	return b.String()
}

// renderSurveyRows renders the rows (header + subject rows) for the given
// modSt + cursor + filter. Exposed as a standalone function so diag.go
// can call it directly without instantiating a full surveyPage.
// activeFacets is the set of facet values to AND-filter against subject tags
// (pass nil for no facet restriction, e.g. from diag). offset and limit
// control vertical scrolling for subject rows; limit < 0 means no limit.
func renderSurveyRows(mod *modSt, cursor rowCursor, filter string, activeFacets []string, inner int, offset, limit int) string {
	if mod.Loading {
		row := " " + mod.Spinner.View() + " " + theme.Dim.Render("loading…")
		return bRow(row, inner)
	}
	if mod.Err != "" {
		// Wrap module-supplied error text to the box width; continuation
		// lines indent under the glyph so multi-line errors read as one block.
		wrapW := inner - 3
		if wrapW < 1 {
			wrapW = 1
		}
		var eb strings.Builder
		for i, line := range wrapText(mod.Err, wrapW) {
			if i == 0 {
				eb.WriteString(bRow(" "+theme.Error.Render("✗ "+line), inner))
			} else {
				eb.WriteString(bRow("   "+theme.Error.Render(line), inner))
			}
		}
		return eb.String()
	}

	visIdxs := visibleSubjectsAll(mod.Subjects, activeFacets, filter)
	if len(visIdxs) == 0 {
		msg := theme.Dim.Render("  (no subjects)")
		if filter != "" {
			msg = theme.Dim.Render("  no results for: " + filter)
		} else if len(activeFacets) > 0 {
			msg = theme.Dim.Render("  no results for active facets")
		}
		return bRow(msg, inner)
	}

	ecols := computeColumnLayout(mod, inner)
	nameW := ecols.nameW
	dataCols := ecols.cols

	var b strings.Builder
	var body strings.Builder

	// Header row.
	{
		var hdr strings.Builder
		hdr.WriteString(theme.Dim.Render(fmt.Sprintf(" %-*s ", nameW, subjectHeader(mod.SurfaceConfig))))
		for _, ec := range dataCols {
			lbl := truncate(ec.label, ec.width)
			pad := ec.width - len([]rune(lbl))
			if pad < 0 {
				pad = 0
			}
			var cell string
			if ec.align == "right" {
				cell = strings.Repeat(" ", pad) + lbl
			} else {
				cell = lbl + strings.Repeat(" ", pad)
			}
			hdr.WriteString(" " + theme.Dim.Render(cell) + " ")
		}
		// Actions column header — a │ separator makes the interactive zone
		// visually distinct from data columns at a glance. Drills share that
		// zone, so the header shows whenever either is present.
		hasInline := len(mod.InlineActions) > 0 && len(mod.InlineActions[0]) > 0
		hasDrills := mod.SurfaceConfig != nil && len(mod.SurfaceConfig.Drills) > 0
		if hasInline || hasDrills {
			hdr.WriteString(theme.Dim.Render(" │ actions"))
		}
		b.WriteString(bRow(hdr.String(), inner))
	}

	for rowPos, subjIdx := range visIdxs {
		if rowPos < offset || (limit >= 0 && rowPos >= offset+limit) {
			continue
		}
		subj := mod.Subjects[subjIdx]
		name, _ := subj["name"].(string)
		colMap, _ := subj["columns"].(map[string]interface{})
		sel := cursor.kind == rowKindSubject && cursor.subjRow == rowPos

		var row strings.Builder
		fieldSel := sel && cursor.fieldIdx == 0
		nameStr := fmt.Sprintf(" %-*s ", nameW, truncate(name, nameW))
		switch {
		case fieldSel:
			row.WriteString(theme.FieldFocus.Render(nameStr))
		case sel:
			row.WriteString(theme.Selected.Render(nameStr))
		default:
			row.WriteString(theme.Body.Render(nameStr))
		}

		for _, ec := range dataCols {
			var cell string
			switch pending, errored := cellState(colMap, ec.id); {
			case pending:
				cell = renderPendingCell(mod.Spinner.View(), ec.width, ec.align)
			case errored:
				cell = renderCell(GlyphAlert, "err", ec.width, ec.align)
			default:
				val, color := cellValue(colMap, ec.id)
				if val == "" {
					val = "—"
					color = "dim"
				}
				cell = renderCell(truncate(val, ec.width), color, ec.width, ec.align)
			}
			row.WriteString(" " + cell + " ")
		}

		nInline := 0
		if subjIdx < len(mod.InlineActions) {
			nInline = len(mod.InlineActions[subjIdx])
		}
		if nInline > 0 {
			for ai, act := range mod.InlineActions[subjIdx] {
				fi := 1 + ai
				afSel := sel && cursor.fieldIdx == fi
				lbl := act.Label
				// " │" separator before each action (2 chars) + " lbl " (len+2)
				// = len+4 per slot — matches computeColumnLayout budget exactly.
				// The │ aligns with the "│ actions" column header above.
				row.WriteString(theme.Dim.Render(" │"))
				var btn string
				switch {
				case afSel && act.Destructive:
					btn = theme.BtnSelectedDanger.Render(" " + lbl + " ")
				case afSel:
					btn = theme.FieldFocus.Render(" " + lbl + " ")
				case act.Destructive:
					btn = theme.BtnIdleDanger.Render(" " + lbl + " ")
				default:
					btn = theme.BtnIdleSafe.Render(" " + lbl + " ")
				}
				row.WriteString(btn)
			}
		}

		// Drill chips follow inline actions in the same zone. Field indices
		// continue the ring: 0=name, 1..nInline=actions, then drills. A trailing
		// caret marks a drill as navigation (it pushes a scoped child survey).
		if mod.SurfaceConfig != nil {
			for di := range mod.SurfaceConfig.Drills {
				fi := 1 + nInline + di
				dfSel := sel && cursor.fieldIdx == fi
				lbl := drillLabel(&mod.SurfaceConfig.Drills[di])
				row.WriteString(theme.Dim.Render(" │"))
				chip := " " + lbl + " " + GlyphCaret
				if dfSel {
					row.WriteString(theme.FieldFocus.Render(chip))
				} else {
					row.WriteString(theme.BtnIdleSafe.Render(chip))
				}
			}
		}

		if sel {
			body.WriteString(bRowSel(row.String(), inner))
		} else {
			body.WriteString(bRow(row.String(), inner))
		}
	}
	// Overlay scrollbar on body rows when content exceeds the visible window.
	b.WriteString(applyScrollbar(body.String(), offset, len(visIdxs)))
	return b.String()
}
