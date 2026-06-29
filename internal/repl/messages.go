// SPDX-License-Identifier: Apache-2.0

package repl

import (
	tea "github.com/charmbracelet/bubbletea"
)

// pushMsg tells the root model to push a new page onto the navigation stack.
type pushMsg struct{ page tea.Model }

// popMsg tells the root model to pop the current page off the stack.
type popMsg struct{}

// replaceMsg tells the root model to replace the current top page in place.
// Used by runningPage to morph into resultPage without growing the stack.
type replaceMsg struct{ page tea.Model }

// replace returns a Cmd that replaces the current top page with the given one.
func replace(page tea.Model) tea.Cmd {
	return func() tea.Msg { return replaceMsg{page: page} }
}

// becameActiveMsg is sent by root to the new top page after a pop.
// Pages that hold live data (overview list, domain focus) handle this to
// re-read reality and refresh their display without requiring a full push/pop cycle.
type becameActiveMsg struct{}

// push returns a Cmd that pushes the given page.
func push(page tea.Model) tea.Cmd {
	return func() tea.Msg { return pushMsg{page: page} }
}

// pop returns a Cmd that pops the current page.
func pop() tea.Cmd {
	return func() tea.Msg { return popMsg{} }
}

// stopFnMsg carries the module shutdown function from the boot page to root
// so root can return it to the caller after the Bubble Tea program exits.
type stopFnMsg struct{ fn func() }

// bootFailMsg signals a fatal boot failure (CORE BIST) from the boot page
// to root, which quits the program; RunWithBoot then reports the error and
// exits after Bubble Tea has restored the terminal. It carries the shutdown
// function directly — tea.Batch gives no ordering guarantee, so root may see
// this message before any stopFnMsg.
type bootFailMsg struct {
	err    error
	stopFn func()
}

// faceReadyMsg hands the REPL orchestrator from the boot page to root once boot
// succeeds. root stores it and starts the inbound job_update listener so pushed
// deliveries flow into the Update loop and are correlated centrally, regardless
// of which page is on top. It is the same orchestrator instance the pages hold
// via AppCtx, so outbound loads and inbound correlation share one authority.
type faceReadyMsg struct{ orch *Orchestrator }
