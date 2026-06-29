// SPDX-License-Identifier: Apache-2.0

// Package repl — ui.go holds pure rendering primitives and the generic
// row-cursor state machine shared by HOME and SURVEY. Nothing in this
// file knows about pages or AppCtx; everything is data → string.
package repl

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"

	"github.com/solutionsunity/suctl/internal/theme"
	"github.com/solutionsunity/suctl/internal/version"
	sdksystem "github.com/solutionsunity/suctl/sdk/system"
)

// ── Cursor model ──────────────────────────────────────────────────────────

// rowKind identifies which kind of row the cursor is on. The matrix of
// ↑↓/←→/Enter semantics is keyed by this kind.
type rowKind int

const (
	rowKindFilter  rowKind = iota // type-filter input row
	rowKindFacet                  // facet toggle row
	rowKindSubject                // body subject row
	rowKindExit                   // exit/utility row
)

// ── Glyph atoms ───────────────────────────────────────────────────────────
// Single source of truth for every Unicode glyph used in the REPL frame.
// No file other than ui.go should embed a raw glyph literal — reference these
// constants instead so a glyph change propagates everywhere at once.

const (
	// GlyphGutter is the selection-gutter marker painted at the left edge of a
	// selected row (1 terminal column wide).
	GlyphGutter = "▎"
	// GlyphCursor is the blinking block that marks the active text-input position.
	GlyphCursor = "█"
	// GlyphCaret is the › arrow used as a row prefix and breadcrumb separator.
	// Also serves as the facet-strip close bracket (same glyph, different role).
	GlyphCaret = "›"
	// GlyphFacetOpen brackets the left edge of the facet chip strip.
	GlyphFacetOpen = "‹"
	// GlyphSep is the mid-dot used as an inline separator between items.
	GlyphSep = "·"
	// GlyphLive is the filled circle indicating a live-read data source.
	GlyphLive = "●"
	// GlyphSelect is the right-pointing triangle used as a row-selection marker.
	GlyphSelect = "▶"
	// GlyphAlert is the ballot-X prefix prepended to alert-class cell values
	// so the status is unmistakable via glyph + bold colour, without relying on
	// background colour rendering (which varies widely across terminals).
	GlyphAlert = "✗"
)

// titleBar carries the optional override for the middle band of the title line.
// When Middle is empty the band falls back to the system-wide active/ready counts
// derived from the inventory DTO. Token drives the colour: "ok" (green), "warn"
// (amber), "alert" (red); an empty Token defaults to "ok".
type titleBar struct {
	Middle string // pre-composed context string; empty → inventory counts fallback
	Token  string // colour token: "ok" | "warn" | "alert" | "" (→ "ok")
}

// rowCursor tracks the cursor within a survey-style row stack
// (used by HOME and SURVEY).
type rowCursor struct {
	kind     rowKind // which kind of row
	subjRow  int     // body row index when kind == rowKindSubject
	fieldIdx int     // horizontal index within a subject/exit row
	facetIdx int     // highlighted facet when kind == rowKindFacet
}

// isTypable returns true when k is a printable character the filter
// should consume (a-z 0-9 . - _ / \ @ space).
func isTypable(k string) bool {
	if len(k) != 1 {
		return false
	}
	c := k[0]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c == '.' || c == '-' || c == '_' ||
		c == '/' || c == '\\' || c == '@' || c == ' '
}

// newSpinner returns the standard repl spinner.
func newSpinner() spinner.Model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = theme.Accent
	return sp
}

// ── Box primitives ────────────────────────────────────────────────────────

func boxTop(inner int) string { return "┌" + strings.Repeat("─", inner) + "┐\n" }
func boxBot(inner int) string { return "└" + strings.Repeat("─", inner) + "┘\n" }
func boxMid(inner int) string { return "├" + strings.Repeat("─", inner) + "┤\n" }

// bRow renders one inner row: "│" + content padded/truncated to inner + "│\n".
// content may include ANSI; visible width is measured with lipgloss so the
// borders never break.
func bRow(content string, inner int) string {
	vis := lipgloss.Width(content)
	if vis > inner {
		content = lipgloss.NewStyle().MaxWidth(inner).Render(content)
		vis = inner
	}
	pad := inner - vis
	return "│" + content + strings.Repeat(" ", pad) + "│\n"
}

// bRowSel renders a selected row with a ▎ gutter marker (1 char) and the
// RowBar background (tier-1 selection — the whole row). The gutter is the
// warm Selected colour so "where am I now?" is a pre-attentive cue (~200ms)
// rather than a serial character search. Width matches bRow exactly.
func bRowSel(content string, inner int) string {
	contentInner := inner - 1
	vis := lipgloss.Width(content)
	if vis > contentInner {
		content = lipgloss.NewStyle().MaxWidth(contentInner).Render(content)
		vis = contentInner
	}
	pad := contentInner - vis
	return "│" + theme.Selected.Render(GlyphGutter) + theme.RowBar.Render(content+strings.Repeat(" ", pad)) + "│\n"
}

// scrollbar glyphs — track is hollow, thumb is filled. Drawn in place of the
// right-edge "│" of body rows when content exceeds the viewport so the
// operator sees both that scrolling is possible and where they are.
const (
	scrollbarTrack = "░"
	scrollbarThumb = "█"
)

// applyScrollbar overlays a vertical scrollbar on the right edge of a block
// of bRow-rendered lines (each ending in "│\n"). Returns the block unchanged
// when total <= visible (no scroll needed).
//
//   offset  — first source row index in the visible window
//   total   — total source rows behind the window
//
// The thumb height is proportional to visible/total; its position is
// proportional to offset/(total-visible). The right-edge "│" is replaced
// with the track or thumb glyph for the matching row.
func applyScrollbar(block string, offset, total int) string {
	if block == "" {
		return block
	}
	lines := strings.Split(strings.TrimRight(block, "\n"), "\n")
	n := len(lines)
	if n <= 0 || total <= n {
		return block
	}
	thumbH := n * n / total
	if thumbH < 1 {
		thumbH = 1
	}
	if thumbH > n {
		thumbH = n
	}
	denom := total - n
	var thumbTop int
	if denom > 0 {
		thumbTop = offset * (n - thumbH) / denom
	}
	if thumbTop+thumbH > n {
		thumbTop = n - thumbH
	}
	if thumbTop < 0 {
		thumbTop = 0
	}
	const edge = "│"
	var out strings.Builder
	for i, line := range lines {
		ch := scrollbarTrack
		if i >= thumbTop && i < thumbTop+thumbH {
			ch = scrollbarThumb
		}
		if strings.HasSuffix(line, edge) {
			line = strings.TrimSuffix(line, edge) + theme.Dim.Render(ch)
		}
		out.WriteString(line)
		out.WriteString("\n")
	}
	return out.String()
}

// ── Common row builders ───────────────────────────────────────────────────

// viewTitleLine renders the single "suctl  <middle>  hostname · ip" header
// above the box. When tb.Middle is non-empty it is used as the centre band;
// otherwise the band falls back to system-wide active/ready counts from the
// inventory DTO. tb.Token drives the colour of the middle band; empty Token
// defaults to "ok".
func viewTitleLine(inv sdksystem.InventoryResponse, hostname, ip string, w int, tb titleBar) string {
	left := theme.Title.Render(" suctl ") + theme.Dim.Render(" "+version.Version)

	var midText, token string
	if tb.Middle != "" {
		midText = " " + tb.Middle + " "
		token = tb.Token
		if token == "" {
			token = "ok"
		}
	} else {
		midText = fmt.Sprintf(" %d active · %d ready ", inv.ActiveCount, inv.ReadyCount)
		token = "ok"
		if inv.ActiveCount == 0 {
			token = "warn"
		}
	}
	mid := theme.Status(token).Render(midText)
	right := theme.Dim.Render(hostname + " · " + ip + " ")

	totalW := lipgloss.Width(left) + lipgloss.Width(mid) + lipgloss.Width(right)
	gap := (w - totalW) / 2
	if gap < 1 {
		gap = 1
	}
	tail := w - totalW - gap
	if tail < 1 {
		tail = 1
	}
	return left + strings.Repeat(" ", gap) + mid + strings.Repeat(" ", tail) + right + "\n"
}

// fallbackStatusSummary returns the effective status summary for a mod:
//  1. mod.StatusSummary if the module supplied one (wire-supplied, language-agnostic).
//  2. "%d %s" using mod.Total and mod.SurfaceConfig.Subject when both are non-zero/non-empty.
//  3. Empty string when Total is 0 or no subject is declared.
//
// No English plural heuristics — module authors control the exact wording
// via StatusSummary or their own subject value.
func fallbackStatusSummary(mod *modSt) string {
	if mod.StatusSummary != "" {
		return mod.StatusSummary
	}
	if mod.Total == 0 {
		return ""
	}
	if mod.SurfaceConfig == nil || mod.SurfaceConfig.Subject == "" {
		return fmt.Sprintf("%d", mod.Total)
	}
	return fmt.Sprintf("%d %s", mod.Total, mod.SurfaceConfig.Subject)
}

// viewFilterRow renders the type-filter input row. When badge is non-empty
// it is rendered right-aligned; otherwise "(filter)" is shown.
func viewFilterRow(filterText string, onRow bool, inner int, badge string) string {
	cursor := ""
	if onRow {
		cursor = theme.Selected.Render(GlyphCursor)
	}
	acc := theme.Accent
	if onRow {
		acc = theme.Selected
	}
	prefix := acc.Render(" " + GlyphCaret + " ")
	body := theme.Body.Render(filterText)
	content := prefix + body + cursor
	contentW := 3 + len([]rune(filterText)) + len([]rune(cursor))
	right := theme.Dim.Render("(filter)")
	if badge != "" {
		right = theme.Dim.Render(badge)
	}
	gap := inner - contentW - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	full := content + strings.Repeat(" ", gap) + right
	if onRow {
		return bRowSel(full, inner)
	}
	return bRow(full, inner)
}

// viewFacetRow renders the facet toggle row. activeFlags[i] is true when
// facet i is currently toggled on; these render green with a ● prefix so
// the active set is always visible, even when the cursor is elsewhere.
// When the cursor lands on an active facet, amber focus takes precedence
// (focused state is the highest-priority cue).
func viewFacetRow(facets []string, activeIdx int, activeFlags []bool, onRow bool, inner int) string {
	if len(facets) == 0 {
		return ""
	}
	var sb strings.Builder
	acc := theme.Dim
	if onRow {
		acc = theme.Selected
	}
	sb.WriteString(acc.Render(" " + GlyphFacetOpen + " "))
	for i, f := range facets {
		if i > 0 {
			sb.WriteString(theme.Dim.Render(" " + GlyphSep + " "))
		}
		isActive := i < len(activeFlags) && activeFlags[i]
		isFocused := onRow && i == activeIdx
		switch {
		case isFocused && isActive:
			// Cursor on an active facet: amber bg + ● prefix.
			sb.WriteString(theme.FieldFocus.Render(GlyphLive + " " + f))
		case isFocused:
			// Cursor on inactive facet: amber bg only.
			sb.WriteString(theme.FieldFocus.Render(f))
		case isActive:
			// Toggled on, cursor elsewhere: green ● label.
			sb.WriteString(theme.Success.Render(GlyphLive + " " + f))
		default:
			sb.WriteString(theme.Dim.Render(f))
		}
	}
	sb.WriteString(acc.Render(" " + GlyphCaret))
	if onRow {
		return bRowSel(sb.String(), inner)
	}
	return bRow(sb.String(), inner)
}

// ── Hint row ──────────────────────────────────────────────────────────────

// kbdHint renders one "<key> <desc>" pair where the key part wears a small
// keycap chip (subtle raised background) and the description renders in dim
// text. This makes the binding visually separable from its label without
// adding any borders or extra glyphs.
func kbdHint(key, desc string) string {
	t := theme.Active()
	cap := lipgloss.NewStyle().
		Background(t.Bg4).
		Foreground(t.Text1).
		Render(" " + key + " ")
	if desc == "" {
		return cap
	}
	return cap + " " + theme.Dim.Render(desc)
}

// viewHintRow joins kbdHint pairs into a single footer-hint line with a
// stable inter-pair gap. Returned string is already styled — callers should
// pass it to bRow without an outer Dim wrap.
func viewHintRow(pairs ...[2]string) string {
	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, kbdHint(p[0], p[1]))
	}
	return " " + strings.Join(parts, "   ")
}

// ── Cell helpers ──────────────────────────────────────────────────────────

// renderCell formats a column cell with padding and a color token.
// For "alert" tokens a GlyphAlert prefix is prepended so the status is
// unmissable via glyph + bold colour — no background colour dependency.
// All tokens are rendered FG-only; raw spaces pad outside the styled run.
func renderCell(val, color string, width int, align string) string {
	if color == "alert" {
		val = GlyphAlert + " " + val
	}
	padding := width - lipgloss.Width(val)
	if padding < 0 {
		padding = 0
	}
	if align == "right" {
		return strings.Repeat(" ", padding) + theme.Status(color).Render(val)
	}
	return theme.Status(color).Render(val) + strings.Repeat(" ", padding)
}

// renderPendingCell pads an already-styled spinner glyph to a column width while
// the cell's `from` job is in flight. Unlike renderCell the glyph is pre-styled
// (it carries the spinner's own colour), so it is padded raw, not re-themed.
func renderPendingCell(view string, width int, align string) string {
	padding := width - lipgloss.Width(view)
	if padding < 0 {
		padding = 0
	}
	if align == "right" {
		return strings.Repeat(" ", padding) + view
	}
	return view + strings.Repeat(" ", padding)
}

// truncate clips a string to n runes, appending "…" when needed.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return string(runes[:n-1]) + "…"
}

// wrapText breaks s into visual lines of at most width display columns each.
// Existing "\n" boundaries are preserved (each paragraph wraps independently);
// inside a paragraph words are broken at spaces. Words wider than width are
// hard-split at the column boundary so no line ever exceeds width. The result
// is plain (unstyled) text — callers apply styling per line if needed.
func wrapText(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		if para == "" {
			out = append(out, "")
			continue
		}
		words := strings.Fields(para)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		line := ""
		for _, word := range words {
			ww := lipgloss.Width(word)
			if ww > width {
				if line != "" {
					out = append(out, line)
					line = ""
				}
				runes := []rune(word)
				for len(runes) > 0 {
					n := width
					if n > len(runes) {
						n = len(runes)
					}
					out = append(out, string(runes[:n]))
					runes = runes[n:]
				}
				continue
			}
			if line == "" {
				line = word
				continue
			}
			if lipgloss.Width(line)+1+ww <= width {
				line += " " + word
			} else {
				out = append(out, line)
				line = word
			}
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// pageSizer is implemented by pages that declare their own minimum content
// size. The root model queries the active page's MinSize() and renders the
// "terminal too small" guard when the live terminal cannot accommodate it,
// so the floor is content-derived rather than a hard constant. Pages that
// do not implement this interface render without any size check.
type pageSizer interface {
	MinSize() (w, h int)
}

// renderSizeGuard returns the operator-facing "terminal too small" message
// for a page whose declared minimum exceeds the live terminal size.
func renderSizeGuard(width, height, minW, minH int) string {
	return theme.Error.Render(fmt.Sprintf(
		"terminal too small — this view requires at least %d×%d (current: %d×%d). resize the terminal or use core system tools for recovery.",
		minW, minH, width, height,
	))
}

// innerWidth returns the inside-of-box width for a given terminal width.
// The root size guard is responsible for ensuring width is at least the
// active page's declared minimum, so no minimum clamp is applied here;
// a tiny defensive floor is kept so degenerate callers do not panic.
func innerWidth(width int) int {
	if width < 3 {
		return 1
	}
	return width - 2
}

// ── Column layout ────────────────────────────────────────────────────────

// effCol is the per-column width/align/label/id used by survey rendering.
type effCol struct {
	id    string
	label string
	width int
	align string
}

// columnLayout is the computed layout for one survey: the name-column width
// plus the list of data columns. The selection-marker char is already
// accounted for.
type columnLayout struct {
	nameW int
	cols  []effCol
}

// drillChipWidth is the display width a drill chip reserves on a survey row:
// " │" separator (2) + " label " (len+2) + caret (1). Single source so the
// width budget in computeColumnLayout and MinSize matches the render exactly.
func drillChipWidth(label string) int {
	return len([]rune(label)) + 5
}

// computeColumnLayout derives the on-screen column widths for the given mod
// + inner width. When a manifest column.Width is 0 the width is computed
// from the label and observed values; the name column absorbs the slack
// after the data columns and inline-action buttons are reserved.
func computeColumnLayout(mod *modSt, inner int) columnLayout {
	var ecols []effCol
	if mod.SurfaceConfig != nil {
		for _, col := range mod.SurfaceConfig.Survey.Columns {
			w := col.Width
			if w == 0 {
				w = len([]rune(col.Label))
				for _, subj := range mod.Subjects {
					colMap, _ := subj["columns"].(map[string]interface{})
					val, color := cellValue(colMap, col.ID)
					vw := len([]rune(val))
					if color == "alert" {
						vw += 2 // GlyphAlert + space prefix adds 2 display columns
					}
					if vw > w {
						w = vw
					}
				}
				if w < 4 {
					w = 4
				}
			}
			ecols = append(ecols, effCol{id: col.ID, label: col.Label, width: w, align: col.Align})
		}
	}

	btnTotal := 0
	if len(mod.InlineActions) > 0 {
		for _, act := range mod.InlineActions[0] {
			btnTotal += len([]rune(act.Label)) + 4
		}
	}
	if mod.SurfaceConfig != nil {
		for i := range mod.SurfaceConfig.Drills {
			btnTotal += drillChipWidth(drillLabel(&mod.SurfaceConfig.Drills[i]))
		}
	}

	dataTotal := 0
	for _, ec := range ecols {
		dataTotal += ec.width + 2
	}

	contentBudget := inner - 1
	nameW := contentBudget - 2 - dataTotal - btnTotal
	if nameW < 10 {
		for nameW < 10 {
			shrunk := false
			for i := range ecols {
				if ecols[i].width > 6 {
					ecols[i].width--
					nameW++
					shrunk = true
					if nameW >= 10 {
						break
					}
				}
			}
			if !shrunk {
				break
			}
		}
		if nameW < 10 {
			nameW = 10
		}
	}
	return columnLayout{nameW: nameW, cols: ecols}
}
