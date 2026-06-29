// SPDX-License-Identifier: Apache-2.0

// Package repl — page_home.go is the HOME page: an aggregator survey
// where each row is one user module (inventory is reachable via the
// [ inventory ] exit button).
package repl

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/solutionsunity/suctl/internal/theme"
)

// homeHintPrimary is the first footer line on the HOME page — essential
// navigation keys only. This is the only line used to drive MinSize() width
// so the terminal is not forced wide just to fit the Alt+ shortcuts.
func homeHintPrimary() string {
	return viewHintRow(
		[2]string{"↑↓", "move"},
		[2]string{"⏎", "enter"},
		[2]string{"←→/tab", "fields"},
		[2]string{"type", "filter"},
	)
}

// homeHintAlt is the second footer line — Alt+ power-user shortcuts. It is
// rendered below the primary line but is NOT used for MinSize so it flows
// naturally on wider terminals without constraining the minimum.
func homeHintAlt() string {
	return viewHintRow(
		[2]string{"Alt+f", "filter"},
		[2]string{"Alt+q", "quit"},
	)
}

type homePage struct {
	ctx          *AppCtx
	cursor       rowCursor
	filter       string
	facets       []bool
	scrollOffset int
}

func newHomePage(ctx *AppCtx) homePage {
	return homePage{
		ctx:    ctx,
		cursor: rowCursor{kind: rowKindSubject},
		facets: make([]bool, len(homeFacets)),
	}
}

func (p homePage) Init() tea.Cmd { return p.ctx.beginLoadAll() }

func (p homePage) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// WindowSizeMsg and spinner.TickMsg are handled centrally by root —
	// root writes ctx dimensions via Ctx() and fans out modSt spinners via
	// ctx.tickSpinners before delegating here. scrollOffset is clamped via
	// SyncScrolled after every delegation, so no inline syncScroll needed.
	switch m := msg.(type) {
	case becameActiveMsg:
		// A pushed page may have activated/deactivated a module while we
		// were away. Rebuild Mods first so newly-active modules join the
		// home list (and deactivated ones leave) before we kick off loads.
		p.ctx.refreshInventory()
		return p, p.ctx.beginLoadAll()
	case tea.KeyMsg:
		return p.handleKey(m)
	}
	return p, nil
}

// SyncScrolled re-clamps scrollOffset after a root-driven delegation so
// the visible window remains valid across resize and key events. The
// pointer-receiver syncScroll mutates the local copy in place.
func (p homePage) SyncScrolled() tea.Model {
	p.syncScroll()
	return p
}

func (p homePage) View() string {
	w := p.ctx.Width
	inner := innerWidth(p.ctx.Width)
	p.syncScroll()
	// Home page shows the system-wide active/ready counts in the title bar —
	// that is its primary purpose (aggregated overview). No module-specific context.
	return viewTitleLine(p.ctx.Inventory, p.ctx.Hostname, p.ctx.IP, w, titleBar{}) + p.viewBody(inner)
}

// maxVisibleMods returns the number of module rows (each 2 lines tall) that
// fit in the visible window. Reserves one extra row above the chrome so the
// title line cannot be pushed off-screen by alt-screen scroll on exact fits.
//
// Layout: title + boxTop + filter + facet + boxMid + body(2*N) + boxMid +
//         exit + footer1 + footer2 + boxBot = 11 + 2*N.
func (p homePage) maxVisibleMods() int {
	limit := p.ctx.Height - 11 - 1
	n := limit / 2
	if n < 1 {
		n = 1
	}
	return n
}

// syncScroll ensures the scrollOffset is adjusted so that the selected
// module row is visible, and that the visible window is always filled
// from the top — when the terminal grows the offset is pulled down so
// the body never leaves dead space below the last module.
// Modules are 2 lines each; offset is in module units.
func (p *homePage) syncScroll() {
	limit := p.maxVisibleMods()
	total := len(p.visibleMods())
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
func (p homePage) Ctx() *AppCtx { return p.ctx }

// MinSize is the content-derived minimum terminal size for this page.
// Width is driven by homeHintPrimary only — the secondary Alt+ hint line
// is displayed but excluded from the width floor so narrow terminals are
// not penalised for the power-user shortcuts.
// Height counts every chrome row plus one minimum body row.
func (p homePage) MinSize() (int, int) {
	w := lipgloss.Width(homeHintPrimary()) + 2 // + box borders
	// title(1) + box top(1) + filter(1) + facet(1) + mid(1) + body(1) +
	// mid(1) + exit(1) + footer1(1) + footer2(1) + bot(1) = 11
	return w, 11
}

func (p homePage) viewBody(inner int) string {
	var b strings.Builder
	b.WriteString(boxTop(inner))

	onFilter := p.cursor.kind == rowKindFilter
	b.WriteString(viewFilterRow(p.filter, onFilter, inner, ""))

	onFacet := p.cursor.kind == rowKindFacet
	labels := make([]string, len(homeFacets))
	for i, hf := range homeFacets {
		labels[i] = hf.Label
	}
	// p.facets carries the toggled-on state; viewFacetRow renders the ●
	// prefix and green colour so active facets are always visible.
	b.WriteString(viewFacetRow(labels, p.cursor.facetIdx, p.facets, onFacet, inner))
	b.WriteString(boxMid(inner))

	maxVis := p.maxVisibleMods()

	vis := p.visibleMods()
	if len(vis) == 0 {
		msg := "  (no active modules — open [ inventory ] to activate one)"
		if p.filter != "" {
			msg = "  no active modules match: " + p.filter
		}
		b.WriteString(bRow(theme.Dim.Render(msg), inner))
	} else {
		var body strings.Builder
		userMods := p.ctx.UserMods()
		for rowPos, mi := range vis {
			if rowPos < p.scrollOffset || rowPos >= p.scrollOffset+maxVis {
				continue
			}
			m := userMods[mi]
			sel := p.cursor.kind == rowKindSubject && p.cursor.subjRow == rowPos

			health, healthW := homeHealthGlyph(m)
			nameW := inner - 2 - healthW - 2
			if nameW < 8 {
				nameW = 8
			}
			namePart := theme.Body.Render(fmt.Sprintf(" %-*s", nameW, truncate(m.ShortName, nameW)))
			line1 := namePart + "  " + health
			summary := ""
			if !m.Loading {
				if m.StatusSummary != "" {
					// Module-provided warning/operational summary → amber.
					summary = "   " + theme.Warn.Render(truncate(m.StatusSummary, inner-4))
				} else if s := fallbackStatusSummary(m); s != "" {
					// Bare subject-count fallback → dim informational.
					summary = "   " + theme.Dim.Render(truncate(s, inner-4))
				}
			}
			if sel {
				body.WriteString(bRowSel(line1, inner))
				body.WriteString(bRowSel(summary, inner))
			} else {
				body.WriteString(bRow(line1, inner))
				body.WriteString(bRow(summary, inner))
			}
		}
		// Each mod renders two box rows; scrollbar works in rendered-line units.
		b.WriteString(applyScrollbar(body.String(), p.scrollOffset*2, len(vis)*2))
	}
	b.WriteString(boxMid(inner))

	// Exit row — color governs affordance (blue = selectable, amber bg =
	// focused). No brackets: each button reserves a 1-char internal pad on
	// each side so the focused amber background fills exactly the same
	// footprint the idle text occupies; widths are stable across states.
	exitSel := p.cursor.kind == rowKindExit
	invBtn := theme.BtnIdleSafe.Render(" inventory ")
	jobsBtn := theme.BtnIdleSafe.Render(" jobs ")
	msgsBtn := theme.BtnIdleSafe.Render(" messages ")
	refreshBtn := theme.BtnIdleSafe.Render(" refresh ")
	quitBtn := theme.BtnIdleSafe.Render(" quit ")
	if exitSel {
		switch p.cursor.fieldIdx {
		case 0:
			invBtn = theme.FieldFocus.Render(" inventory ")
		case 1:
			jobsBtn = theme.FieldFocus.Render(" jobs ")
		case 2:
			msgsBtn = theme.FieldFocus.Render(" messages ")
		case 3:
			refreshBtn = theme.FieldFocus.Render(" refresh ")
		case 4:
			quitBtn = theme.FieldFocus.Render(" quit ")
		}
	}
	b.WriteString(bRow(" "+invBtn+" "+jobsBtn+" "+msgsBtn+" "+refreshBtn+" "+quitBtn, inner))
	b.WriteString(bRow(homeHintPrimary(), inner))
	b.WriteString(bRow(homeHintAlt(), inner))
	b.WriteString(boxBot(inner))
	return b.String()
}

// homeHealthGlyph returns (rendered glyph, visible width) for a mod row.
func homeHealthGlyph(m *modSt) (string, int) {
	if m.Loading {
		return m.Spinner.View() + " loading…", 11
	}
	if m.Err != "" {
		return theme.Error.Render("✗ error"), 7
	}
	if m.StatusSummary != "" {
		return theme.Warn.Render("● warn"), 6
	}
	return theme.Status("ok").Render("● ok"), 4
}

// visibleMods returns indices into ctx.UserMods() passing the active filters.
// Text filter and facets combine with AND semantics.
func (p homePage) visibleMods() []int {
	q := strings.ToLower(p.filter)
	// Build a set of active facet values (only "has-warnings" exists for HOME).
	hasWarningsActive := len(p.facets) > 0 && p.facets[0] &&
		len(homeFacets) > 0 && homeFacets[0].Value == "has-warnings"
	var out []int
	for i, m := range p.ctx.UserMods() {
		if q != "" && !strings.Contains(strings.ToLower(m.ShortName), q) {
			continue
		}
		if hasWarningsActive && m.StatusSummary == "" && m.Err == "" {
			continue
		}
		out = append(out, i)
	}
	return out
}
