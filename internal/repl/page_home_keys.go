// SPDX-License-Identifier: Apache-2.0

// Package repl — page_home_keys.go: HOME-level keyboard handling.
package repl

import (
	tea "github.com/charmbracelet/bubbletea"
)

func (p homePage) handleKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	k := m.String()

	// Quit shortcuts (ctrl+c, alt+q) are intercepted centrally in root.
	if k == "alt+f" {
		if p.cursor.kind == rowKindFilter {
			p.cursor.kind = rowKindFacet
		} else {
			p.cursor.kind = rowKindFilter
		}
		return p, nil
	}
	if isTypable(k) {
		p.filter += string(m.Runes)
		p.cursor.kind = rowKindFilter
		return p, nil
	}

	cur := &p.cursor
	switch k {
	case "backspace":
		if len(p.filter) > 0 {
			p.filter = p.filter[:len(p.filter)-1]
		}
	case "esc":
		if p.filter != "" {
			p.filter = ""
		}
	case "up":
		switch cur.kind {
		case rowKindFacet:
			cur.kind = rowKindFilter
		case rowKindSubject:
			if cur.subjRow > 0 {
				cur.subjRow--
				cur.fieldIdx = 0
			} else {
				cur.kind = rowKindFacet
			}
		case rowKindExit:
			vis := p.visibleMods()
			if len(vis) > 0 {
				cur.kind = rowKindSubject
				cur.subjRow = len(vis) - 1
			} else {
				cur.kind = rowKindFacet
			}
			cur.fieldIdx = 0
		}
	case "down":
		switch cur.kind {
		case rowKindFilter:
			cur.kind = rowKindFacet
			cur.facetIdx = 0
		case rowKindFacet:
			cur.kind = rowKindSubject
			cur.subjRow = 0
			cur.fieldIdx = 0
		case rowKindSubject:
			vis := p.visibleMods()
			if cur.subjRow < len(vis)-1 {
				cur.subjRow++
				cur.fieldIdx = 0
			} else {
				cur.kind = rowKindExit
				cur.fieldIdx = 0
			}
		}
	case "pgup":
		vis := p.visibleMods()
		if len(vis) > 0 {
			cur.kind = rowKindSubject
			cur.fieldIdx = 0
			cur.subjRow -= p.maxVisibleMods()
			if cur.subjRow < 0 {
				cur.subjRow = 0
			}
		}
	case "pgdown":
		vis := p.visibleMods()
		if len(vis) > 0 {
			cur.kind = rowKindSubject
			cur.fieldIdx = 0
			cur.subjRow += p.maxVisibleMods()
			if cur.subjRow > len(vis)-1 {
				cur.subjRow = len(vis) - 1
			}
		}
	case "home":
		vis := p.visibleMods()
		if len(vis) > 0 {
			cur.kind = rowKindSubject
			cur.subjRow = 0
			cur.fieldIdx = 0
		}
	case "end":
		vis := p.visibleMods()
		if len(vis) > 0 {
			cur.kind = rowKindSubject
			cur.subjRow = len(vis) - 1
			cur.fieldIdx = 0
		}
	case "left", "shift+tab":
		// Wrap-around: stepping past 0 lands on the last index of that axis
		// so left/right (and tab cycle) is infinite — matches the user's
		// expectation that fields form a ring, not a bounded list.
		switch cur.kind {
		case rowKindFacet:
			if n := len(homeFacets); n > 0 {
				cur.facetIdx = (cur.facetIdx - 1 + n) % n
			}
		case rowKindExit:
			cur.fieldIdx = (cur.fieldIdx - 1 + 5) % 5 // 0=inventory 1=jobs 2=msgs 3=refresh 4=quit
		}
	case "right", "tab":
		switch cur.kind {
		case rowKindFacet:
			if n := len(homeFacets); n > 0 {
				cur.facetIdx = (cur.facetIdx + 1) % n
			}
		case rowKindExit:
			cur.fieldIdx = (cur.fieldIdx + 1) % 5 // 0=inventory 1=jobs 2=msgs 3=refresh 4=quit
		}
	case "enter":
		switch cur.kind {
		case rowKindFilter:
			// Enter on filter row is a no-op — typing is always live.
		case rowKindFacet:
			// Enter on facet row toggles the highlighted facet.
			if cur.facetIdx < len(p.facets) {
				p.facets[cur.facetIdx] = !p.facets[cur.facetIdx]
			}
		case rowKindSubject:
			vis := p.visibleMods()
			if cur.subjRow < len(vis) {
				mod := p.ctx.UserMods()[vis[cur.subjRow]]
				return p, push(newSurveyPage(p.ctx, mod))
			}
		case rowKindExit:
			switch cur.fieldIdx {
			case 0:
				return p, push(newSurveyPage(p.ctx, p.ctx.InventoryMod()))
			case 1:
				return p, push(newSurveyPage(p.ctx, p.ctx.JobsMod))
			case 2:
				return p, push(newSurveyPage(p.ctx, p.ctx.MessagesMod))
			case 3:
				// Refresh: reset filter and all facets, then reload every survey.
				p.filter = ""
				p.facets = make([]bool, len(homeFacets))
				return p, p.ctx.beginLoadAll()
			case 4:
				return p, tea.Quit
			}
		}
	}
	return p, nil
}
