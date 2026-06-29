// SPDX-License-Identifier: Apache-2.0

// Package repl implements the Bubble Tea interactive REPL for suctl.
package repl

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/solutionsunity/suctl/internal/logging"
)

// RunWithBoot launches the boot spinner, runs bootFn asynchronously to activate
// modules, then seamlessly transitions into the interactive REPL. It blocks
// until the operator quits and returns the module shutdown function so the
// caller can invoke it for signal-handler driven cleanup.
//
// warns contains pre-boot warnings (probe, scan, missing) that are shown on
// the boot page and forwarded to the home page after activation completes.
// The module inventory is fetched through the broker after bootFn returns —
// REPL never touches core's in-process module index.
func RunWithBoot(warns []string, bootFn func() BootResult) func() {
	slog.Info("REPL starting")
	boot := newBootPage(warns, bootFn)
	r := root{pages: []tea.Model{boot}}
	// Disable stdout tee before Bubble Tea takes over the terminal.
	// Background goroutines (health monitor, supervisor) would otherwise write
	// raw log lines directly to stdout and corrupt the TUI display.
	// Per CQ15: logging writes to file only once the TUI is active.
	logging.DisableStdout()
	p := tea.NewProgram(r, tea.WithAltScreen())
	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "suctl:", err)
		os.Exit(1)
	}
	if fr, ok := finalModel.(root); ok {
		// A fatal boot failure (bootFailMsg) quit the program; now that Bubble
		// Tea has restored the terminal, shut down anything already activated,
		// report, and exit non-zero.
		if fr.bootErr != nil {
			if fr.stopFn != nil {
				fr.stopFn()
			}
			fmt.Fprintln(os.Stderr, "suctl:", fr.bootErr)
			os.Exit(1)
		}
		return fr.stopFn
	}
	return nil
}

// ctxHolder is implemented by pages that hold the shared *AppCtx so the
// root model can write terminal dimensions directly on WindowSizeMsg —
// centralising what used to be duplicated in every page's Update and
// covering pages (confirm, running, result, cascade-confirm) that never
// handled the message and so rendered with stale ctx.Width / ctx.Height.
type ctxHolder interface {
	Ctx() *AppCtx
}

// scrollSyncer is implemented by pages that maintain a scroll offset and
// must re-clamp it after any state-changing message (resize, key, load).
// Centralising this in root removes the typed re-assertion ceremony that
// every scrolling page's Update used to repeat verbatim.
type scrollSyncer interface {
	SyncScrolled() tea.Model
}

// root is the top-level Bubble Tea model.  It owns the page stack — each
// level of navigation is a tea.Model pushed onto the stack.  Navigation
// messages (pushMsg / popMsg) travel up via Cmds.
//
// root caches the most recent tea.WindowSizeMsg so that pages pushed or
// replaced after the initial frame still learn the terminal size — bubbletea
// only emits WindowSizeMsg on real resize events, not on every model swap.
type root struct {
	pages   []tea.Model
	stopFn  func()        // set via stopFnMsg when modules finish booting
	orch    *Orchestrator // set via faceReadyMsg; the surface correlation authority
	bootErr error         // set via bootFailMsg; checked by RunWithBoot after exit
	size    tea.WindowSizeMsg
}

func (r root) Init() tea.Cmd {
	if len(r.pages) == 0 {
		return nil
	}
	return r.pages[len(r.pages)-1].Init()
}

func (r root) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case stopFnMsg:
		r.stopFn = m.fn
		return r, nil

	case faceReadyMsg:
		// Boot succeeded; the REPL orchestrator is live. Store it and start the
		// inbound listener — its drain Cmd re-arms via jobUpdateMsg below.
		r.orch = m.orch
		return r, r.orch.listen()

	case jobUpdateMsg:
		// A pushed job_update arrived. The orchestrator correlates it by
		// job_token — applying it when the load is already known, parking it until
		// the accept arrives when not — and folds a terminal update into the
		// originating mod's render state. Page-independent, like the spinner
		// fan-out below, so a completion lands whether or not its mod's page is on
		// top. Re-arm the listener so the stream stays live across navigation.
		if r.orch == nil {
			return r, nil
		}
		cmd := r.orch.ingest(m.token, m.params)
		return r, tea.Batch(cmd, r.orch.listen())

	case moduleSurveyLoadedMsg:
		// A survey load resolved (inline result, async accept, or error). The
		// orchestrator folds it centrally onto the originating mod — projecting an
		// inline/terminal result, or registering an async accept and replaying any
		// update that already parked under its token. Then delegate so the active
		// page can run its own post-projection work (survey selected-key
		// re-resolution). Page-independent: a background load completes under any
		// page.
		var loadCmd tea.Cmd
		if r.orch != nil {
			loadCmd = r.orch.applyLoad(m)
		}
		top, cmd := r.delegate(msg)
		r.pages[len(r.pages)-1] = top
		return r, tea.Batch(loadCmd, cmd)

	case cellLoadedMsg:
		// A column's cell fill resolved (inline row-keyed map, async accept, or
		// error). The orchestrator folds it across every row's shared columns
		// centrally — painting an inline/terminal map, registering an async accept
		// (replaying any parked completion), or marking the columns errored.
		// Page-independent, like the survey-load fan-out above: cells fill under
		// any page.
		if r.orch != nil {
			r.orch.applyCellLoad(m)
		}
		return r, nil

	case bootFailMsg:
		if m.stopFn != nil {
			r.stopFn = m.stopFn
		}
		r.bootErr = m.err
		return r, tea.Quit

	case tea.WindowSizeMsg:
		// Cache for re-delivery to pages pushed after this point, and
		// propagate to the active page's AppCtx so every page (whether or
		// not it handles WindowSizeMsg itself) renders with current
		// dimensions. Force a full repaint — some terminals leave the
		// newly-revealed region uncleared on vertical growth, which
		// manifests as the body appearing to stop at the previous height.
		// tea.ClearScreen is benign on shrink.
		r.size = m
		r.writeSizeToActiveCtx(m)
		top, cmd := r.delegate(msg)
		r.pages[len(r.pages)-1] = top
		return r, tea.Batch(cmd, tea.ClearScreen)

	case spinner.TickMsg:
		// Centralised fan-out: the active page's AppCtx tickSpinners
		// advances every modSt loading spinner so background animations
		// (survey loads under a focus/running page) never freeze. The
		// page-local Update still runs below for pages that own their own
		// spinner (boot, running, focus).
		var ctxCmd tea.Cmd
		if h, ok := r.pages[len(r.pages)-1].(ctxHolder); ok {
			if c := h.Ctx(); c != nil {
				ctxCmd = c.tickSpinners(m)
			}
		}
		top, cmd := r.delegate(msg)
		r.pages[len(r.pages)-1] = top
		return r, tea.Batch(ctxCmd, cmd)

	case pushMsg:
		r.pages = append(r.pages, m.page)
		// New page must learn the current terminal size before its first
		// render — write directly into its Ctx (if any) then deliver the
		// cached WindowSizeMsg so its own size-dependent state initialises.
		r.writeSizeToActiveCtx(r.size)
		initCmd := r.pages[len(r.pages)-1].Init()
		if r.size.Width > 0 && r.size.Height > 0 {
			top, sizeCmd := r.delegate(r.size)
			r.pages[len(r.pages)-1] = top
			return r, tea.Batch(initCmd, sizeCmd)
		}
		return r, initCmd

	case popMsg:
		if len(r.pages) > 1 {
			r.pages = r.pages[:len(r.pages)-1]
			r.writeSizeToActiveCtx(r.size)
			// Notify the new top page that it is active again so it can
			// re-read live state (overview re-loads domain list, focus re-reads domain).
			top, cmd := r.delegate(becameActiveMsg{})
			r.pages[len(r.pages)-1] = top
			if r.size.Width > 0 && r.size.Height > 0 {
				top, sizeCmd := r.delegate(r.size)
				r.pages[len(r.pages)-1] = top
				return r, tea.Batch(cmd, sizeCmd)
			}
			return r, cmd
		}
		return r, nil

	case replaceMsg:
		if len(r.pages) > 0 {
			r.pages[len(r.pages)-1] = m.page
			r.writeSizeToActiveCtx(r.size)
			initCmd := r.pages[len(r.pages)-1].Init()
			if r.size.Width > 0 && r.size.Height > 0 {
				top, sizeCmd := r.delegate(r.size)
				r.pages[len(r.pages)-1] = top
				return r, tea.Batch(initCmd, sizeCmd)
			}
			return r, initCmd
		}
		return r, nil

	case tea.KeyMsg:
		// Universal quit shortcuts — centralised so the operator's expectation
		// holds on every page (boot, running, result included). Per-page
		// handlers no longer need to repeat these.
		if k := m.String(); k == "ctrl+c" || k == "alt+q" {
			return r, tea.Quit
		}
	}

	if len(r.pages) == 0 {
		return r, tea.Quit
	}

	top, cmd := r.delegate(msg)
	r.pages[len(r.pages)-1] = top
	return r, cmd
}

// delegate forwards msg to the active page's Update and then runs the
// page's scroll-sync hook (if any) — centralising the post-Update offset
// clamp that every scrolling page used to do inline.
func (r *root) delegate(msg tea.Msg) (tea.Model, tea.Cmd) {
	top, cmd := r.pages[len(r.pages)-1].Update(msg)
	if ss, ok := top.(scrollSyncer); ok {
		top = ss.SyncScrolled()
	}
	return top, cmd
}

// writeSizeToActiveCtx propagates terminal dimensions into the active
// page's AppCtx (when the page exposes one). A no-op for boot, which has
// no ctx yet, and for any future ctx-less page.
func (r *root) writeSizeToActiveCtx(m tea.WindowSizeMsg) {
	if m.Width <= 0 || m.Height <= 0 || len(r.pages) == 0 {
		return
	}
	if h, ok := r.pages[len(r.pages)-1].(ctxHolder); ok {
		if c := h.Ctx(); c != nil {
			c.Width = m.Width
			c.Height = m.Height
		}
	}
}

func (r root) View() string {
	if len(r.pages) == 0 {
		return ""
	}
	top := r.pages[len(r.pages)-1]
	w, h := r.size.Width, r.size.Height
	// Pre-WindowSizeMsg: render a single-space placeholder until bubbletea
	// delivers a real terminal size. Calling into the page here would render
	// at (0,0) and likely panic on negative-width arithmetic.
	if w <= 0 || h <= 0 {
		return " "
	}
	// Per-page minimum: if the active page declares a MinSize and the live
	// terminal is below it, show the guard message instead of the page body.
	if ps, ok := top.(pageSizer); ok {
		minW, minH := ps.MinSize()
		if w < minW || h < minH {
			return renderSizeGuard(w, h, minW, minH)
		}
	}
	return top.View()
}
