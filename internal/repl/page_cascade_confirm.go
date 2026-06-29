// SPDX-License-Identifier: Apache-2.0

// Package repl — page_cascade_confirm.go is the page shown when the
// system module returns CONFIRMATION_REQUIRED for an activation request.
// It lists the target module and the ready-but-inactive providers that
// activation will also start and asks the operator to approve the
// full set before anything is written. On Yes the originating action is
// re-invoked with {"confirm": true}; on No the request is dropped.
package repl

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/solutionsunity/suctl/sdk/protocol"
	"github.com/solutionsunity/suctl/internal/theme"
)

// cascadeConfirmHintRow is the bottom hint line for the cascade-confirm
// dialog. Defined once so View and MinSize cannot drift.
func cascadeConfirmHintRow() string {
	return viewHintRow(
		[2]string{"←→", "choose"},
		[2]string{"⏎", "confirm"},
		[2]string{"y/n", "shortcut"},
		[2]string{"esc", "cancel"},
	)
}

type cascadeConfirmPage struct {
	pending pendingAction
	cascade protocol.CascadeDetail
	yes     bool // cursor on Yes (false = No, the default)
}

func newCascadeConfirmPage(pa pendingAction, c protocol.CascadeDetail) cascadeConfirmPage {
	return cascadeConfirmPage{pending: pa, cascade: c, yes: false}
}

func (p cascadeConfirmPage) Init() tea.Cmd { return nil }

// Ctx returns the shared AppCtx so root can centrally update
// terminal dimensions on WindowSizeMsg.
func (p cascadeConfirmPage) Ctx() *AppCtx { return p.pending.Ctx }

func (p cascadeConfirmPage) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Quit shortcuts (ctrl+c, alt+q) are intercepted centrally in root.
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "esc", "n", "N":
			return p, pop()
		case "left", "right", "tab":
			p.yes = !p.yes
		case "y", "Y":
			return p, replace(newRunningPage(p.withConfirm()))
		case "enter":
			if p.yes {
				return p, replace(newRunningPage(p.withConfirm()))
			}
			return p, pop()
		}
	}
	return p, nil
}

// withConfirm clones pending with ExtraArgs[ConfirmParam]=true so the
// re-invocation passes the operator's approval to the handler. The key
// name is owned by sdk/protocol so sender and receiver cannot drift.
func (p cascadeConfirmPage) withConfirm() pendingAction {
	pa := p.pending
	args := make(map[string]interface{}, len(pa.ExtraArgs)+1)
	for k, v := range pa.ExtraArgs {
		args[k] = v
	}
	args[protocol.ConfirmParam] = true
	pa.ExtraArgs = args
	return pa
}

func (p cascadeConfirmPage) View() string {
	var b strings.Builder
	b.WriteString("\n\n")
	// Heading tinted with the Act moment colour (amber).
	b.WriteString("  " + theme.MomentTitle(theme.MomentAct).Render("Confirm "+p.pending.Action.Label) + "\n\n")

	target := p.cascade.Target
	if target == "" {
		target = p.pending.SubjectName
	}
	b.WriteString("  " + theme.Body.Render("activating ") + theme.Warn.Render(target) +
		theme.Body.Render(" also requires activating:") + "\n\n")

	for _, prov := range p.cascade.Providers {
		b.WriteString("    " + theme.Warn.Render("• "+prov) + "\n")
	}
	b.WriteString("\n  " + theme.Dim.Render("activate all listed modules?") + "\n\n")

	// Cascade activation is not destructive (the change is reversible by
	// deactivating), so both Yes and No use the safe colour ramp.
	if p.yes {
		b.WriteString("  " + theme.FieldFocus.Render("  Yes  ") + "   " + theme.BtnIdleSafe.Render("  No  ") + "\n\n")
	} else {
		b.WriteString("  " + theme.BtnIdleSafe.Render("  Yes  ") + "   " + theme.FieldFocus.Render("  No  ") + "\n\n")
	}
	b.WriteString("  " + cascadeConfirmHintRow())
	return b.String()
}

// MinSize is the content-derived minimum terminal size for this page.
// Width fits the header sentence plus the widest provider name; height
// accounts for fixed chrome plus one row per listed provider.
func (p cascadeConfirmPage) MinSize() (int, int) {
	target := p.cascade.Target
	if target == "" {
		target = p.pending.SubjectName
	}
	w := lipgloss.Width("activating "+target+" also requires activating:") + 4
	for _, prov := range p.cascade.Providers {
		pw := lipgloss.Width("• "+prov) + 6
		if pw > w {
			w = pw
		}
	}
	hint := lipgloss.Width(cascadeConfirmHintRow()) + 4
	if hint > w {
		w = hint
	}
	// 2 blanks + title + blank + header + blank + providers + blank + prompt + blank + buttons + blank + footer
	h := 11 + len(p.cascade.Providers)
	return w, h
}
