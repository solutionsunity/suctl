// SPDX-License-Identifier: Apache-2.0

// Package repl — page_focus_keys.go: FOCUS-level keyboard handling.
package repl

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/solutionsunity/suctl/internal/theme"
)

func (p focusPage) handleKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	k := m.String()
	// Quit shortcuts (ctrl+c, alt+q) are intercepted centrally in root.
	if k == "esc" {
		return p, pop()
	}
	if p.state.Loading || p.state.Err != "" {
		return p, nil
	}
	switch k {
	case "up":
		if p.scrollOffset > 0 {
			p.scrollOffset--
		}
	case "down":
		inner := innerWidth(p.ctx.Width)
		total := p.state.totalLines(inner)
		limit := p.bodyLimit()
		if p.scrollOffset < total-limit {
			p.scrollOffset++
		}
	case "pgup":
		p.scrollOffset -= p.bodyLimit()
		if p.scrollOffset < 0 {
			p.scrollOffset = 0
		}
	case "pgdown":
		inner := innerWidth(p.ctx.Width)
		total := p.state.totalLines(inner)
		limit := p.bodyLimit()
		p.scrollOffset += limit
		if p.scrollOffset > total-limit {
			p.scrollOffset = total - limit
		}
		if p.scrollOffset < 0 {
			p.scrollOffset = 0
		}
	case "home":
		p.scrollOffset = 0
	case "end":
		inner := innerWidth(p.ctx.Width)
		total := p.state.totalLines(inner)
		limit := p.bodyLimit()
		if total > limit {
			p.scrollOffset = total - limit
		} else {
			p.scrollOffset = 0
		}
	case "left", "shift+tab":
		// Wrap-around: action row is a ring spanning virtual back (0) and
		// real actions (1..n) so tab cycling never dead-ends.
		n := len(p.state.Actions) + 1 // +1 for virtual back at index 0
		p.state.ActionIdx = (p.state.ActionIdx - 1 + n) % n
	case "right", "tab":
		n := len(p.state.Actions) + 1
		p.state.ActionIdx = (p.state.ActionIdx + 1) % n
	case "enter":
		if p.state.ActionIdx == 0 {
			// Virtual back — same as Esc. Safe default on open.
			return p, pop()
		}
		if len(p.state.Actions) == 0 {
			return p, nil
		}
		// Offset by 1: ActionIdx 1 → Actions[0], etc.
		act := p.state.Actions[p.state.ActionIdx-1]
		return p, triggerAction(p.ctx, p.mod, p.subject, act)
	}
	return p, nil
}

// renderFocusActions renders the action button row for a focusSt as a
// single box row. Exposed so diag.go can call it directly.
//
// Layout: [← back] is always first at ActionIdx=0 (the safe default so an
// accidental Enter returns to survey instead of triggering an action). Real
// module actions follow at ActionIdx 1..n.
func renderFocusActions(st *focusSt, inner int) string {
	if st.Loading || st.Err != "" {
		return bRow("", inner)
	}
	var b strings.Builder
	b.WriteString(" ")

	// Virtual back button — index 0.
	if st.ActionIdx == 0 {
		b.WriteString(theme.FieldFocus.Render(" ← back ") + " ")
	} else {
		b.WriteString(theme.Dim.Render(" ← back ") + " ")
	}

	if len(st.Actions) == 0 {
		return bRow(b.String()+theme.Dim.Render("(no actions)"), inner)
	}
	// Real module actions — displayed at positions 1..n.
	for i, act := range st.Actions {
		lbl := act.Label
		sel := (i + 1) == st.ActionIdx // +1 to offset virtual back at 0
		var btn string
		switch {
		case sel && act.Destructive:
			btn = theme.BtnSelectedDanger.Render(" " + lbl + " ")
		case sel:
			btn = theme.FieldFocus.Render(" " + lbl + " ")
		case act.Destructive:
			btn = theme.BtnIdleDanger.Render(" " + lbl + " ")
		default:
			btn = theme.BtnIdleSafe.Render(" " + lbl + " ")
		}
		b.WriteString(btn + " ")
	}
	return bRow(b.String(), inner)
}
