// SPDX-License-Identifier: Apache-2.0

// Package repl — page_result.go is the RESULT page: the outcome of an
// invoked Action (success or error). Esc/Enter pops back to whichever
// page originated the action; that page handles becameActiveMsg to
// reload its live state.
package repl

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/solutionsunity/suctl/internal/theme"
)

type resultPage struct {
	result actionResult
}

func newResultPage(r actionResult) resultPage {
	return resultPage{result: r}
}

func (p resultPage) Init() tea.Cmd {
	// System activate/deactivate drive the affected module's lifecycle directly
	// (synchronously) in the capability handler, so by the time we render here
	// the runtime state is already up to date. No async rescan trigger needed.
	return nil
}

// Ctx returns the shared AppCtx so root can centrally update
// terminal dimensions on WindowSizeMsg.
func (p resultPage) Ctx() *AppCtx { return p.result.Action.Ctx }

func (p resultPage) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Quit shortcuts (ctrl+c, alt+q) are intercepted centrally in root.
	switch m := msg.(type) {
	case tea.KeyMsg:
		switch m.String() {
		case "esc", "enter", "q":
			return p, pop()
		}
	}
	return p, nil
}

func (p resultPage) View() string {
	ctx := p.result.Action.Ctx
	w := ctx.Width
	inner := innerWidth(w)
	// Body lines use a 2-char left pad ("  ") inside the box; wrap budget
	// reserves that pad so the right border is never breached.
	wrapW := inner - 2
	if wrapW < 1 {
		wrapW = 1
	}
	var b strings.Builder
	tb := titleBar{
		Middle: p.result.Action.ShortName + " " + GlyphCaret + " " + p.result.Action.SubjectName,
		Token:  "ok",
	}
	b.WriteString(viewTitleLine(ctx.Inventory, ctx.Hostname, ctx.IP, w, tb))
	b.WriteString(boxTop(inner))
	if p.result.Ok {
		b.WriteString(bRow("  "+theme.Success.Render("✓ "+p.result.Action.Action.Label+" succeeded"), inner))
		for _, line := range wrapText(p.result.Action.SubjectName, wrapW) {
			b.WriteString(bRow("  "+theme.Dim.Render(line), inner))
		}
	} else {
		b.WriteString(bRow("  "+theme.Error.Render("✗ "+p.result.Action.Action.Label+" failed"), inner))
		// Module-supplied error text may be a single long line or several
		// concatenated tool outputs; wrap each paragraph independently so
		// long stderr dumps never bleed past the box border.
		for _, line := range wrapText(p.result.Message, wrapW) {
			b.WriteString(bRow("  "+theme.Body.Render(line), inner))
		}
	}
	b.WriteString(boxMid(inner))
	b.WriteString(bRow("  "+theme.Dim.Render("esc / enter back"), inner))
	b.WriteString(boxBot(inner))
	return b.String()
}

// MinSize is the content-derived minimum terminal size for this page.
// Width is driven only by the headline and footer — body content (subject
// name on success, error message on failure) is word-wrapped at render time
// and so does not constrain the minimum terminal width.
func (p resultPage) MinSize() (int, int) {
	w := lipgloss.Width("esc / enter back") + 6
	if p.result.Ok {
		head := lipgloss.Width("✓ "+p.result.Action.Action.Label+" succeeded") + 6
		if head > w {
			w = head
		}
	} else {
		head := lipgloss.Width("✗ "+p.result.Action.Action.Label+" failed") + 6
		if head > w {
			w = head
		}
	}
	// title(1) + boxTop(1) + head(1) + body(1) + boxMid(1) + footer(1) + boxBot(1) = 7
	return w, 7
}
