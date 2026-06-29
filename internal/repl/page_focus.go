// SPDX-License-Identifier: Apache-2.0

// Package repl — page_focus.go is the FOCUS page: a single subject's
// detail body (KV sections) plus an action row at the bottom. Navigation
// is constrained to the action row; the body is read-only.
package repl

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/solutionsunity/suctl/internal/theme"
)

// focusHintRow is the bottom-of-box hint line on the FOCUS page. Keys
// wear a keycap chip so they read as bindings rather than prose.
func focusHintRow() string {
	return viewHintRow(
		[2]string{"←→/tab", "action"},
		[2]string{"↑↓", "scroll"},
		[2]string{"⏎", "invoke"},
		[2]string{"esc", "back"},
	)
}

type focusPage struct {
	ctx          *AppCtx
	mod          *modSt
	subject      map[string]interface{}
	state        *focusSt
	initCmd      tea.Cmd
	scrollOffset int
}

func newFocusPage(ctx *AppCtx, mod *modSt, subject map[string]interface{}) focusPage {
	st, cmd := ctx.orch.beginFocus(mod, subject)
	return focusPage{ctx: ctx, mod: mod, subject: subject, state: st, initCmd: cmd}
}

func (p focusPage) Init() tea.Cmd {
	// State + load Cmd were prepared in the constructor; Init only needs
	// to hand the spinner-tick / load batch back to the runtime.
	return p.initCmd
}

func (p focusPage) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// WindowSizeMsg is handled centrally by root via Ctx(); spinner.TickMsg
	// is fanned out to modSt spinners by root before delegation. The local
	// focusSt.Spinner is page-private and ticked here.
	switch m := msg.(type) {
	case moduleFocusLoadedMsg:
		applyFocusLoaded(p.state, m)
		return p, nil
	case spinner.TickMsg:
		var tickCmd tea.Cmd
		p.state.Spinner, tickCmd = p.state.Spinner.Update(m)
		return p, tickCmd
	case becameActiveMsg:
		// Returned from a CONFIRM/RUNNING/RESULT pop — re-read focus.
		st, loadCmd := p.ctx.orch.beginFocus(p.mod, p.subject)
		p.state = st
		return p, loadCmd
	case tea.KeyMsg:
		return p.handleKey(m)
	}
	return p, nil
}

// SyncScrolled re-clamps scrollOffset after a root-driven delegation so
// the visible window remains valid across resize and key events.
func (p focusPage) SyncScrolled() tea.Model {
	p.syncScroll()
	return p
}

// applyFocusLoaded writes a moduleFocusLoadedMsg into the focusSt.
func applyFocusLoaded(st *focusSt, m moduleFocusLoadedMsg) {
	st.Loading = false
	if m.err != nil {
		st.Err = m.err.Error()
		return
	}
	st.SubjectName = m.subjectName
	st.Sections = m.sections
	st.Actions = m.actions
	st.Err = ""
	// ActionIdx 0 = virtual back, 1..n = real actions. Clamp only when
	// out of the valid range (> len, not >= len).
	if st.ActionIdx > len(m.actions) {
		st.ActionIdx = 0
	}
}

func (p focusPage) View() string {
	w := p.ctx.Width
	inner := innerWidth(p.ctx.Width)
	p.syncScroll()
	return viewTitleLine(p.ctx.Inventory, p.ctx.Hostname, p.ctx.IP, w, p.titleBarContent()) + p.viewBody(inner)
}

// titleBarContent builds the breadcrumb for the focus title bar:
// "module › subject". The separator is the same GlyphCaret used in the
// in-box breadcrumb row so the two levels (title + box) read consistently.
func (p focusPage) titleBarContent() titleBar {
	subjLabel := p.state.SubjectName
	if subjLabel == "" {
		subjLabel = subjectLabel(p.subject)
	}
	middle := p.mod.ShortName + " " + GlyphCaret + " " + subjLabel
	return titleBar{Middle: middle, Token: "ok"}
}

// bodyLimit is the number of content lines that fit in the visible window.
//
// Layout: title + boxTop + crumb + boxMid + body + boxMid + actions +
//         footer + boxBot = 9 chrome rows. One extra row is reserved so the
// title cannot be pushed off-screen by alt-screen scroll on exact fits.
func (p focusPage) bodyLimit() int {
	limit := p.ctx.Height - 9 - 1
	if limit < 1 {
		limit = 1
	}
	return limit
}

// syncScroll ensures the scrollOffset is adjusted so that the content is
// within view. Since focus doesn't have a cursor for content (it's in the
// action row), scrolling is manually adjusted via up/down keys. When the
// terminal grows enough that total <= limit, offset is pulled to 0 so
// content fills the visible window from the top instead of leaving dead
// space below.
func (p *focusPage) syncScroll() {
	inner := innerWidth(p.ctx.Width)
	total := p.state.totalLines(inner)
	limit := p.bodyLimit()
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
func (p focusPage) Ctx() *AppCtx { return p.ctx }

// MinSize is the content-derived minimum terminal size for this page.
// Width must fit the footer hint and at least one label+value pair.
// Height counts the box chrome plus one body row minimum.
func (p focusPage) MinSize() (int, int) {
	w := lipgloss.Width(focusHintRow()) + 2
	if w < 36 { // label(20) + gap + min value(12) + borders
		w = 36
	}
	// title(1) + top(1) + crumb(1) + mid(1) + body(1) + mid(1) + actions(1) + footer(1) + bot(1) = 9
	return w, 9
}

func (p focusPage) viewBody(inner int) string {
	var b strings.Builder
	b.WriteString(boxTop(inner))

	// Header: breadcrumb on the left, [← survey] hint on the right.
	subjLabel := p.state.SubjectName
	if subjLabel == "" {
		subjLabel = subjectLabel(p.subject)
	}
	// Breadcrumb separator tinted with the Focus moment colour (green).
	crumb := theme.Dim.Render(" "+p.mod.ShortName) +
		theme.MomentAccent(theme.MomentFocus).Render(" › ") +
		theme.Body.Render(subjLabel)
	// Right-hand hint: not a focusable button (esc handles back), so it stays
	// Dim and unbracketed — pure label with directional glyph.
	back := theme.Dim.Render("← survey")
	gap := inner - lipgloss.Width(crumb) - lipgloss.Width(back) - 1
	if gap < 1 {
		gap = 1
	}
	b.WriteString(bRow(crumb+strings.Repeat(" ", gap)+back+" ", inner))
	b.WriteString(boxMid(inner))

	b.WriteString(renderFocusBody(p.state, inner, p.scrollOffset, p.bodyLimit()))
	b.WriteString(boxMid(inner))

	b.WriteString(renderFocusActions(p.state, inner))
	b.WriteString(bRow(focusHintRow(), inner))
	b.WriteString(boxBot(inner))
	return b.String()
}

// totalLines returns the total number of content lines (not box rows) that
// the focus body will render.
func (st *focusSt) totalLines(inner int) int {
	if st.Loading || len(st.Sections) == 0 {
		return 1
	}
	if st.Err != "" {
		wrapW := inner - 3
		if wrapW < 1 {
			wrapW = 1
		}
		return len(wrapText(st.Err, wrapW))
	}
	n := 0
	for si, sec := range st.Sections {
		if si > 0 {
			n++ // spacer
		}
		n++ // title
		n += len(sec.fields)
	}
	return n
}

// renderFocusBody renders the KV sections of a focusSt as box rows.
// offset and limit control vertical scrolling; limit < 0 means no limit.
func renderFocusBody(st *focusSt, inner int, offset, limit int) string {
	var lines []string
	if st.Loading {
		lines = append(lines, " "+st.Spinner.View()+" "+theme.Dim.Render("loading…"))
	} else if st.Err != "" {
		wrapW := inner - 3
		if wrapW < 1 {
			wrapW = 1
		}
		for i, line := range wrapText(st.Err, wrapW) {
			if i == 0 {
				lines = append(lines, " "+theme.Error.Render("✗ "+line))
			} else {
				lines = append(lines, "   "+theme.Error.Render(line))
			}
		}
	} else if len(st.Sections) == 0 {
		lines = append(lines, " "+theme.Dim.Render("(no detail)"))
	} else {
		for si, sec := range st.Sections {
			if si > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, " "+theme.Dim.Render(strings.ToUpper(sec.title)))
			for _, f := range sec.fields {
				val := f.value
				if f.color != "" {
					val = theme.Status(f.color).Render(val)
				}
				line := fmt.Sprintf("   %-20s %s", theme.Dim.Render(f.label), val)
				lines = append(lines, line)
			}
		}
	}

	var b strings.Builder
	for i, line := range lines {
		if i < offset || (limit >= 0 && i >= offset+limit) {
			continue
		}
		b.WriteString(bRow(line, inner))
	}
	// Overlay scrollbar on the body block when content exceeds the window.
	return applyScrollbar(b.String(), offset, len(lines))
}
