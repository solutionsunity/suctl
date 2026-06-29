// SPDX-License-Identifier: Apache-2.0

package repl

import (
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/solutionsunity/suctl/internal/theme"
)

// BootResult carries the output of module startup back to the boot page.
// It decouples the repl package from the startup package so there is no
// heavy import chain into the UI layer. Face is the in-process orchestrator
// (the core broker) the REPL invokes directly — the REPL is part of the core
// process, so there is no socket to dial.
//
// A non-nil Err is a fatal boot failure (CORE BIST): the TUI quits and
// RunWithBoot reports the error after the terminal is restored. StopFn may
// still be set alongside Err when modules were already activated.
type BootResult struct {
	Warns  []string
	StopFn func()           // call to gracefully shut down all active modules
	Face   SurfaceInvoker // in-process face onto the surface orchestrator
	Err    error            // fatal boot failure; nil on success
}

// bootDoneMsg is sent by the async boot command when startup completes.
type bootDoneMsg struct{ result BootResult }

// bootPage is the Bubble Tea model shown while modules are activating.
// It animates a spinner and, when the async boot finishes, seamlessly
// transitions to the REPL home page.
type bootPage struct {
	spinner spinner.Model
	warns   []string // pre-boot warns (probe, scan, missing)
	bootFn  func() BootResult
}

func newBootPage(warns []string, bootFn func() BootResult) bootPage {
	s := spinner.New()
	s.Spinner = spinner.Points
	s.Style = theme.Accent
	return bootPage{spinner: s, warns: warns, bootFn: bootFn}
}

func (p bootPage) Init() tea.Cmd {
	return tea.Batch(
		p.spinner.Tick,
		// Run startup asynchronously so the spinner can animate.
		func() tea.Msg {
			return bootDoneMsg{result: p.bootFn()}
		},
	)
}

func (p bootPage) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Quit shortcuts (ctrl+c, alt+q) are intercepted centrally in root.
	switch m := msg.(type) {
	case bootDoneMsg:
		// Fatal boot failure — hand the error (and any shutdown function) to
		// root, which quits so RunWithBoot can report it on a sane terminal.
		if m.result.Err != nil {
			return p, func() tea.Msg {
				return bootFailMsg{err: m.result.Err, stopFn: m.result.StopFn}
			}
		}
		// Merge any startup warnings into the pre-boot warns list and hand the
		// surface door to newAppCtx, which builds the orchestrator and fetches
		// the initial inventory snapshot *through it* — boot makes no surface
		// transport call itself, keeping the orchestrator the single door.
		warns := append(p.warns, m.result.Warns...)
		ctx := newAppCtx(warns, m.result.Face)
		return p, tea.Batch(
			func() tea.Msg { return stopFnMsg{fn: m.result.StopFn} },
			func() tea.Msg { return faceReadyMsg{orch: ctx.orch} },
			push(newHomePage(ctx)),
		)

	case spinner.TickMsg:
		var cmd tea.Cmd
		p.spinner, cmd = p.spinner.Update(m)
		return p, cmd
	}
	return p, nil
}

func (p bootPage) View() string {
	var b strings.Builder
	b.WriteString("\n\n")
	b.WriteString("  " + theme.Title.Render("suctl") + "\n\n")
	b.WriteString("  " + p.spinner.View() + "  " + theme.Dim.Render("Starting modules...") + "\n")
	if len(p.warns) > 0 {
		b.WriteString("\n")
		for _, w := range p.warns {
			b.WriteString("  " + theme.Warn.Render("⚠  "+w) + "\n")
		}
	}
	return b.String()
}

// MinSize is the content-derived minimum terminal size for this page.
// Boot needs only enough room for the title and the spinner status; one
// extra row is reserved per warn line so the operator sees them all.
func (p bootPage) MinSize() (int, int) {
	w := 30
	h := 5 + len(p.warns)
	if len(p.warns) > 0 {
		h++ // blank separator before warn list
	}
	return w, h
}
