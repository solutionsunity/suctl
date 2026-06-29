// SPDX-License-Identifier: Apache-2.0

// Package repl — page_survey_keys.go: SURVEY-level keyboard handling.
package repl

import (
	tea "github.com/charmbracelet/bubbletea"
)

func (p surveyPage) handleKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	k := m.String()

	// Quit shortcuts (ctrl+c, alt+q) are intercepted centrally in root.
	if k == "alt+r" {
		p.filter = ""
		for i := range p.activeFacets {
			p.activeFacets[i] = false
		}
		return p, p.reload()
	}
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
		} else {
			return p, pop()
		}
	case "up":
		switch cur.kind {
		case rowKindFacet:
			cur.kind = rowKindFilter
		case rowKindSubject:
			if cur.subjRow > 0 {
				cur.subjRow--
				cur.fieldIdx = 0
				p.selectedKey = p.subjectKeyAt(cur.subjRow)
			} else {
				cur.kind = rowKindFacet
			}
		case rowKindExit:
			vis := visibleSubjectsAll(p.mod.Subjects, p.activeFacetValues(), p.filter)
			if len(vis) > 0 {
				cur.kind = rowKindSubject
				cur.subjRow = len(vis) - 1
				p.selectedKey = p.subjectKeyAt(cur.subjRow)
			} else {
				cur.kind = rowKindFacet
			}
			cur.fieldIdx = 0
		}
	case "down":
		switch cur.kind {
		case rowKindFilter:
			if p.mod.SurfaceConfig != nil && len(p.mod.SurfaceConfig.Survey.Facets) > 0 {
				cur.kind = rowKindFacet
			} else {
				cur.kind = rowKindSubject
				cur.subjRow = 0
				p.selectedKey = p.subjectKeyAt(0)
			}
		case rowKindFacet:
			cur.kind = rowKindSubject
			cur.subjRow = 0
			cur.fieldIdx = 0
			p.selectedKey = p.subjectKeyAt(0)
		case rowKindSubject:
			vis := visibleSubjectsAll(p.mod.Subjects, p.activeFacetValues(), p.filter)
			if cur.subjRow < len(vis)-1 {
				cur.subjRow++
				cur.fieldIdx = 0
				p.selectedKey = p.subjectKeyAt(cur.subjRow)
			} else {
				cur.kind = rowKindExit
				cur.fieldIdx = 0
			}
		}
	case "pgup":
		vis := visibleSubjectsAll(p.mod.Subjects, p.activeFacetValues(), p.filter)
		if len(vis) > 0 {
			cur.kind = rowKindSubject
			cur.fieldIdx = 0
			step := p.bodyLimit()
			cur.subjRow -= step
			if cur.subjRow < 0 {
				cur.subjRow = 0
			}
			p.selectedKey = p.subjectKeyAt(cur.subjRow)
		}
	case "pgdown":
		vis := visibleSubjectsAll(p.mod.Subjects, p.activeFacetValues(), p.filter)
		if len(vis) > 0 {
			cur.kind = rowKindSubject
			cur.fieldIdx = 0
			step := p.bodyLimit()
			cur.subjRow += step
			if cur.subjRow > len(vis)-1 {
				cur.subjRow = len(vis) - 1
			}
			p.selectedKey = p.subjectKeyAt(cur.subjRow)
		}
	case "home":
		vis := visibleSubjectsAll(p.mod.Subjects, p.activeFacetValues(), p.filter)
		if len(vis) > 0 {
			cur.kind = rowKindSubject
			cur.subjRow = 0
			cur.fieldIdx = 0
			p.selectedKey = p.subjectKeyAt(0)
		}
	case "end":
		vis := visibleSubjectsAll(p.mod.Subjects, p.activeFacetValues(), p.filter)
		if len(vis) > 0 {
			cur.kind = rowKindSubject
			cur.subjRow = len(vis) - 1
			cur.fieldIdx = 0
			p.selectedKey = p.subjectKeyAt(cur.subjRow)
		}
	case "left", "shift+tab":
		// Wrap-around: stepping past 0 lands on the last index of that axis
		// so left/right (and tab cycle) is infinite — matches the user's
		// expectation that fields form a ring, not a bounded list.
		switch cur.kind {
		case rowKindFacet:
			if p.mod.SurfaceConfig != nil {
				n := len(p.mod.SurfaceConfig.Survey.Facets)
				if n > 0 {
					cur.facetIdx = (cur.facetIdx - 1 + n) % n
				}
			}
		case rowKindSubject:
			n := p.maxFieldIdx(cur.subjRow) + 1
			cur.fieldIdx = (cur.fieldIdx - 1 + n) % n
		case rowKindExit:
			n := p.exitMaxFieldIdx() + 1
			cur.fieldIdx = (cur.fieldIdx - 1 + n) % n
		}
	case "right", "tab":
		switch cur.kind {
		case rowKindFacet:
			if p.mod.SurfaceConfig != nil {
				n := len(p.mod.SurfaceConfig.Survey.Facets)
				if n > 0 {
					cur.facetIdx = (cur.facetIdx + 1) % n
				}
			}
		case rowKindSubject:
			n := p.maxFieldIdx(cur.subjRow) + 1
			cur.fieldIdx = (cur.fieldIdx + 1) % n
		case rowKindExit:
			n := p.exitMaxFieldIdx() + 1
			cur.fieldIdx = (cur.fieldIdx + 1) % n
		}
	case "enter":
		switch cur.kind {
		case rowKindFilter:
			cur.kind = rowKindSubject
			cur.subjRow = 0
			cur.fieldIdx = 0
			p.selectedKey = p.subjectKeyAt(0)
		case rowKindFacet:
			if cur.facetIdx < len(p.activeFacets) {
				p.activeFacets[cur.facetIdx] = !p.activeFacets[cur.facetIdx]
				// Facet toggle re-filters locally — no network call needed (D68).
				return p, nil
			}
		case rowKindSubject:
			vis := visibleSubjectsAll(p.mod.Subjects, p.activeFacetValues(), p.filter)
			if cur.subjRow >= len(vis) {
				break
			}
			subjIdx := vis[cur.subjRow]
			subj := p.mod.Subjects[subjIdx]
			if cur.fieldIdx == 0 {
				p.selectedKey = subjectID(subj)
				return p, push(newFocusPage(p.ctx, p.mod, subj))
			}
			nInline := 0
			if subjIdx < len(p.mod.InlineActions) {
				nInline = len(p.mod.InlineActions[subjIdx])
			}
			actFieldIdx := cur.fieldIdx - 1
			if actFieldIdx < nInline {
				act := p.mod.InlineActions[subjIdx][actFieldIdx]
				return p, triggerAction(p.ctx, p.mod, subj, act)
			}
			// Past the inline actions: the field is a drill. Build a scoped
			// child mod from the drill config and push its survey.
			drillIdx := actFieldIdx - nInline
			if drillIdx >= 0 && drillIdx < p.drillCount() {
				drill := &p.mod.SurfaceConfig.Drills[drillIdx]
				child := childModSt(p.mod, drill, subjectID(subj))
				return p, push(newSurveyPage(p.ctx, child))
			}
		case rowKindExit:
			switch cur.fieldIdx {
			case 0:
				return p, pop()
			case 1:
				return p, p.reload()
			default:
				actIdx := cur.fieldIdx - 2
				nSurveyActions := 0
				if p.mod != nil {
					nSurveyActions = len(p.mod.SurveyActions)
				}
				if actIdx >= 0 && actIdx < nSurveyActions {
					act := p.mod.SurveyActions[actIdx]
					vis := visibleSubjectsAll(p.mod.Subjects, p.activeFacetValues(), p.filter)
					if len(vis) == 0 {
						return p, nil // no-op: nothing visible to act on
					}
					subjectIDs := make([]string, len(vis))
					for i, idx := range vis {
						subjectIDs[i] = subjectID(p.mod.Subjects[idx])
					}
					return p, triggerSurveyAction(p.ctx, p.mod, act, subjectIDs)
				}
				// Jobs toggle is at 2+nSurveyActions.
				if p.mod.ShortName == "jobs" && actIdx == nSurveyActions {
					p.showSystem = !p.showSystem
					return p, p.reload()
				}
			}
		}
	}
	return p, nil
}

// exitMaxFieldIdx returns the highest valid fieldIdx for the exit row.
// Layout: 0=home, 1=refresh, 2..2+N-1=survey actions, 2+N=jobs toggle.
func (p surveyPage) exitMaxFieldIdx() int {
	n := 1 // baseline: 0=home, 1=refresh
	if p.mod != nil {
		n += len(p.mod.SurveyActions)
		if p.mod.ShortName == "jobs" {
			n++ // system toggle appended after survey actions
		}
	}
	return n
}

// subjectKeyAt returns the stable id for the visible subject at the given row.
func (p surveyPage) subjectKeyAt(visRow int) string {
	vis := visibleSubjectsAll(p.mod.Subjects, p.activeFacetValues(), p.filter)
	if visRow >= len(vis) {
		return ""
	}
	return subjectID(p.mod.Subjects[vis[visRow]])
}

// maxFieldIdx returns the highest valid fieldIdx for a subject row.
// Cursor steps land only on interactive elements: the subject name (0), each
// inline action (1..nInline), then each drill (nInline+1..nInline+nDrills).
// Data columns are informational and are intentionally skipped so the ring
// contains no dead stops.
func (p surveyPage) maxFieldIdx(subjRow int) int {
	vis := visibleSubjectsAll(p.mod.Subjects, p.activeFacetValues(), p.filter)
	if subjRow >= len(vis) {
		return 0
	}
	subjIdx := vis[subjRow]
	nInline := 0
	if subjIdx < len(p.mod.InlineActions) {
		nInline = len(p.mod.InlineActions[subjIdx])
	}
	return nInline + p.drillCount()
}

// drillCount returns the number of drills declared on this surface — the same
// for every row, since drills are static surface config.
func (p surveyPage) drillCount() int {
	if p.mod.SurfaceConfig == nil {
		return 0
	}
	return len(p.mod.SurfaceConfig.Drills)
}
