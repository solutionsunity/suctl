// SPDX-License-Identifier: Apache-2.0

// Package repl — page_running.go is the RUNNING page: a spinner shown
// while a pendingAction is in flight. On completion it morphs into a
// resultPage in place (replace, not push) so popping the result returns
// to the page that originated the action.
package repl

import (
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/solutionsunity/suctl/internal/theme"
)

type runningPage struct {
	pending pendingAction
	spinner spinner.Model
}

func newRunningPage(pa pendingAction) runningPage {
	sp := newSpinner()
	// Spinner tinted with the Act moment colour (amber) — reinforces the
	// "something is happening" peripheral cue.
	sp.Style = theme.MomentAccent(theme.MomentAct)
	return runningPage{pending: pa, spinner: sp}
}

func (p runningPage) Init() tea.Cmd {
	return tea.Batch(
		p.spinner.Tick,
		p.pending.Ctx.orch.invokeAction(p.pending),
	)
}

// Ctx returns the shared AppCtx so root can centrally update
// terminal dimensions on WindowSizeMsg.
func (p runningPage) Ctx() *AppCtx { return p.pending.Ctx }

func (p runningPage) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Quit shortcuts (ctrl+c, alt+q) are intercepted centrally in root.
	switch m := msg.(type) {
	case spinner.TickMsg:
		var cmd tea.Cmd
		p.spinner, cmd = p.spinner.Update(m)
		return p, cmd

	case frameActionDoneMsg:
		if m.cascade != nil {
			return p, replace(newCascadeConfirmPage(p.pending, *m.cascade))
		}
		res := actionResult{Ok: m.err == "", Message: m.err, Action: p.pending}
		return p, replace(newResultPage(res))
	}
	return p, nil
}

func (p runningPage) View() string {
	ctx := p.pending.Ctx
	w := ctx.Width
	inner := innerWidth(w)
	var b strings.Builder
	tb := titleBar{
		Middle: p.pending.ShortName + " " + GlyphCaret + " " + p.pending.SubjectName,
		Token:  "ok",
	}
	b.WriteString(viewTitleLine(ctx.Inventory, ctx.Hostname, ctx.IP, w, tb))
	b.WriteString(boxTop(inner))
	b.WriteString(bRow("  "+theme.MomentTitle(theme.MomentAct).Render(p.pending.Action.Label), inner))
	b.WriteString(bRow("  "+p.spinner.View()+"  "+theme.Dim.Render("running "+p.pending.Action.Label+" on "+p.pending.SubjectName+"…"), inner))
	b.WriteString(boxBot(inner))
	return b.String()
}

// MinSize is the content-derived minimum terminal size for this page.
// Width must fit the title and the running-status line at full length.
func (p runningPage) MinSize() (int, int) {
	title := lipgloss.Width(p.pending.Action.Label) + 6
	status := lipgloss.Width("running "+p.pending.Action.Label+" on "+p.pending.SubjectName+"…") + 10
	w := title
	if status > w {
		w = status
	}
	// title(1) + boxTop(1) + head(1) + spinner(1) + boxBot(1) = 5
	return w, 5
}
