// SPDX-License-Identifier: Apache-2.0

// Package repl — actions.go is the single entry point for invoking an
// Action against a subject. Callers (page_survey_keys, page_focus_keys)
// hand it a *modSt + subject + Action; it routes destructive actions
// through CONFIRM and non-destructive ones straight to RUNNING.
package repl

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

// triggerAction returns the Cmd that pushes the next page for invoking
// act against subj on mod. Destructive actions push a confirmPage;
// non-destructive actions push a runningPage directly. ctx is threaded
// through pendingAction so the result page can trigger a rescan for
// system actions before the originating page reloads its survey.
func triggerAction(ctx *AppCtx, mod *modSt, subj map[string]interface{}, act Action) tea.Cmd {
	pa := pendingAction{
		Action:      act,
		SubjectID:   subjectID(subj),
		SubjectName: subjectLabel(subj),
		ShortName:   mod.ShortName,
		Ctx:         ctx,
	}
	if act.Destructive {
		return push(newConfirmPage(pa))
	}
	return push(newRunningPage(pa))
}

// triggerSurveyAction returns the Cmd for a survey-level bulk action.
// Survey actions always confirm (they are mass operations) regardless of
// the Destructive flag — the flag only controls button danger styling.
// The capability receives {"scope": <parent-scope>, "subjects": [ids…]};
// the module decides execution strategy (serial, parallel, batched…).
// subjectIDs is the currently visible set after facet + text filtering.
func triggerSurveyAction(ctx *AppCtx, mod *modSt, act Action, subjectIDs []string) tea.Cmd {
	scope := mod.Scope
	// Build a readable scope label for the confirm prompt.
	subject := "subjects"
	if mod.SurfaceConfig != nil && mod.SurfaceConfig.Subject != "" {
		subject = mod.SurfaceConfig.Subject
	}
	scopeLabel := fmt.Sprintf("%d %s", len(subjectIDs), subject)
	if scope != "" {
		scopeLabel += " in " + scope
	}
	pa := pendingAction{
		Action:      act,
		SubjectID:   "", // no single subject — scope+subjects[] via ExtraArgs
		SubjectName: scopeLabel,
		ShortName:   mod.ShortName,
		Ctx:         ctx,
		ExtraArgs: map[string]interface{}{
			"scope":    scope,
			"subjects": subjectIDs,
		},
	}
	return push(newConfirmPage(pa))
}
