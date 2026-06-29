// SPDX-License-Identifier: Apache-2.0

// Package repl — page_confirm.go is the CONFIRM page: a yes/no prompt
// shown before invoking a destructive Action. On Yes it morphs into a
// runningPage in place (replace, not push) so popping the result lands
// on the page that originated the action.
package repl

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/solutionsunity/suctl/internal/theme"
)

// confirmHintRow is the bottom hint line for the confirm dialog. Keys
// wear a keycap chip so they read as bindings rather than prose.
func confirmHintRow() string {
	return viewHintRow(
		[2]string{"←→", "choose"},
		[2]string{"⏎", "confirm"},
		[2]string{"y/n", "shortcut"},
		[2]string{"esc", "cancel"},
	)
}

type confirmPage struct {
	pending pendingAction
	yes     bool // cursor on Yes (false = No, the default)
}

func newConfirmPage(pa pendingAction) confirmPage {
	return confirmPage{pending: pa, yes: false}
}

func (p confirmPage) Init() tea.Cmd { return nil }

// Ctx returns the shared AppCtx so root can centrally update
// terminal dimensions on WindowSizeMsg.
func (p confirmPage) Ctx() *AppCtx { return p.pending.Ctx }

func (p confirmPage) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Quit shortcuts (ctrl+c, alt+q) are intercepted centrally in root.
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "esc", "n", "N":
			return p, pop()
		case "left":
			p.yes = false // left key → No (the left button)
		case "right":
			p.yes = true // right key → Yes (the right button)
		case "tab":
			p.yes = !p.yes // tab wraps between the two
		case "y", "Y":
			return p, replace(newRunningPage(p.pending))
		case "enter":
			if p.yes {
				return p, replace(newRunningPage(p.pending))
			}
			return p, pop()
		}
	}
	return p, nil
}

func (p confirmPage) View() string {
	var b strings.Builder
	b.WriteString("\n\n")
	// Heading tinted with the Act moment colour (amber).
	b.WriteString("  " + theme.MomentTitle(theme.MomentAct).Render("Confirm "+p.pending.Action.Label) + "\n\n")
	line := "  " + theme.Body.Render(p.pending.Action.Label) + " " + theme.Warn.Render(p.pending.SubjectName) + "?"
	b.WriteString(line + "\n\n")

	// Confirm dialog buttons — [No] on LEFT, [Yes] on RIGHT.
	// Layout matches key semantics: left=No, right=Yes.
	// No is the safe default (yes=false on open); Yes is danger-red.
	// Both buttons are fixed-width so focus change never shifts layout.
	if p.yes {
		b.WriteString("  " + theme.BtnIdleSafe.Render("  No  ") + "   " + theme.BtnSelectedDanger.Render("  Yes  ") + "\n\n")
	} else {
		b.WriteString("  " + theme.FieldFocus.Render("  No  ") + "   " + theme.BtnIdleDanger.Render("  Yes  ") + "\n\n")
	}
	b.WriteString("  " + confirmHintRow())
	return b.String()
}

// MinSize is the content-derived minimum terminal size for this page.
// Width must fit the title, the action+subject message, and the footer
// hint, each rendered with a 2-column indent.
func (p confirmPage) MinSize() (int, int) {
	hints := lipgloss.Width(confirmHintRow()) + 4
	title := lipgloss.Width("Confirm "+p.pending.Action.Label) + 4
	msg := lipgloss.Width(p.pending.Action.Label+" "+p.pending.SubjectName+"?") + 4
	w := hints
	if title > w {
		w = title
	}
	if msg > w {
		w = msg
	}
	// 2 leading blanks + title + blank + msg + blank + buttons + blank + footer = 9
	return w, 9
}
