// SPDX-License-Identifier: Apache-2.0

// Package repl — orchestrator.go is the REPL-side surface orchestrator: the
// inbound mirror of surface.Orchestrator (internal/surface). The surface
// orchestrator is the core side of the conversation — it mints identity
// (id + job_token), fires the capability, and decodes the module output.
// This orchestrator is the face side: it fires loads outbound through that
// same door (the SurfaceInvoker) and correlates the pushed job_update stream
// inbound back onto the originating mod.
//
// The one asymmetry between the two is identity: the surface orchestrator
// mints the token, so it correlates for free; the face learns the token a beat
// late (on the async accept). The parking area absorbs exactly that one-fact
// gap — updates that arrive before their token is known are held by token and
// replayed the moment a load is accepted. It is order-independent: a terminal
// "done" that beats the accept (the fast-survey race) and an accept that beats
// the done both flow through the same park-then-replay path.
//
// Like its peer it owns no truth and no cache: known/parked hold only ephemeral
// in-flight correlation, discarded on terminal or supersede, and every load is
// still a fresh read. It is a stream transformer, not a store. All inbound
// methods run on the single Bubble Tea Update goroutine; the outbound Cmd
// producers run their I/O in tea.Cmd goroutines and fold results back as
// messages, so the maps are never touched concurrently.
package repl

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	surfacecore "github.com/solutionsunity/suctl/internal/surface"
	"github.com/solutionsunity/suctl/sdk/protocol"
	sdksystem "github.com/solutionsunity/suctl/sdk/system"
)

// parkedCap bounds the number of distinct tokens held in the parking area.
// Parked entries are tiny and short-lived (claimed on the next accept); the cap
// only guards against unbounded growth from superseded tokens whose terminal
// update arrives after a fresh load orphaned them. Oldest token is evicted; the
// next fresh load reconciles truth regardless. Matches the surface stream depth.
const parkedCap = 64

// cellTarget is the face-side correlation a cell fill keeps: the mod and the
// capName (the column `from`) the fill was fired for, so the completion can be
// painted onto every column sharing it across every row — and cleared together on
// error. One `from` is fired once for the whole column, so no row id is kept.
type cellTarget struct {
	mod     *modSt
	capName string
}

// Orchestrator is the face's single door onto the surface, in both directions.
// It holds the SurfaceInvoker (the outbound composition authority) and the
// inbound correlation maps. It caches no truth.
type Orchestrator struct {
	face SurfaceInvoker
	// known maps a minted job_token to the mod awaiting its async survey —
	// recorded when a load is accepted. The face's late-learned equivalent of
	// the token the surface orchestrator already holds.
	known map[string]*modSt
	// cellKnown maps a minted job_token to the (mod, from) a cell fill is filling —
	// the face's token→(mod,from) correlation, parallel to known (token→mod).
	// Recorded when a cell fill is accepted async; the pushed completion is routed
	// here and painted onto the matching columns of every row.
	cellKnown map[string]cellTarget
	// parked holds updates that arrived before their token was known, by token,
	// in arrival order. Drained and cleared on register; bounded by parkedCap.
	parked map[string][]protocol.JobUpdateParams
	// order is the FIFO of distinct parked tokens for bounded eviction.
	order []string
}

// newOrchestrator builds the REPL orchestrator over the surface door.
func newOrchestrator(face SurfaceInvoker) *Orchestrator {
	return &Orchestrator{
		face:      face,
		known:     map[string]*modSt{},
		cellKnown: map[string]cellTarget{},
		parked:    map[string][]protocol.JobUpdateParams{},
	}
}

// listen blocks on the surface orchestrator's inbound stream and emits the next
// delivery as a jobUpdateMsg. root re-issues it after each delivery so the
// stream is drained continuously, independent of which page is on top. A closed
// stream ends the listener (returns nil) so shutdown parks no goroutine.
func (o *Orchestrator) listen() tea.Cmd {
	return func() tea.Msg {
		u, ok := <-o.face.Updates()
		if !ok {
			return nil
		}
		return jobUpdateMsg{token: u.Token, params: u.Params}
	}
}

// Inventory fires system.module.inventory through the surface door and returns
// the decoded index. Refreshed after every state-changing action.
func (o *Orchestrator) Inventory() (sdksystem.InventoryResponse, error) {
	return o.face.Inventory()
}

// beginLoad starts a fresh async survey reload for mod: it supersedes any
// in-flight correlation for this mod, resets the spinner/loading state, and
// returns the Cmd that fires the load and ticks the spinner. extra carries
// optional capability args (show_system for jobs, scope for drill children).
func (o *Orchestrator) beginLoad(mod *modSt, extra ...map[string]interface{}) tea.Cmd {
	o.forget(mod)
	sp := newSpinner()
	mod.Spinner = sp
	mod.Loading = true
	mod.Subjects = nil
	return tea.Batch(sp.Tick, o.loadSurveyCmd(mod, extra...))
}

// beginFocus prepares a focusSt for the subject and returns the Cmd that loads
// the focus body and ticks its spinner. Focus is synchronous — no correlation.
func (o *Orchestrator) beginFocus(mod *modSt, subject map[string]interface{}) (*focusSt, tea.Cmd) {
	sp := newSpinner()
	foc := &focusSt{Loading: true, Spinner: sp, Subject: subject}
	return foc, tea.Batch(sp.Tick, o.loadFocusCmd(mod, subject))
}

// loadSurveyCmd asks the surface door to load mod's survey, then adapts the
// neutral response into a moduleSurveyLoadedMsg carrying the mod pointer.
// On async accept it carries the minted token (correlation key) and accepted=true;
// inline it carries the decoded rows. extra keys are merged into args.
func (o *Orchestrator) loadSurveyCmd(mod *modSt, extra ...map[string]interface{}) tea.Cmd {
	return func() tea.Msg {
		args := map[string]interface{}{}
		if len(extra) > 0 {
			for k, v := range extra[0] {
				args[k] = v
			}
		}
		load, err := o.face.LoadSurvey(mod.SurveyCapName, args)
		if err != nil {
			return moduleSurveyLoadedMsg{mod: mod, err: err}
		}
		if load.Accepted {
			return moduleSurveyLoadedMsg{mod: mod, token: load.Token, accepted: true}
		}
		return surveyLoadedMsg(mod, load.Survey, load.Actions)
	}
}

// loadFocusCmd asks the surface door to load mod's focus body for a subject and
// adapts it into a focusSt-shaped message. Synchronous; no correlation.
func (o *Orchestrator) loadFocusCmd(mod *modSt, subject map[string]interface{}) tea.Cmd {
	return func() tea.Msg {
		resp, err := o.face.LoadFocus(mod.FocusCapName, subjectID(subject))
		if err != nil {
			return moduleFocusLoadedMsg{shortName: mod.ShortName, err: err}
		}
		st := newFocusStFromResponse(resp)
		defaultLabels(st.Actions, mod.ShortName)
		subjectName := st.SubjectName
		if subjectName == "" {
			subjectName, _ = subject["name"].(string)
		}
		return moduleFocusLoadedMsg{shortName: mod.ShortName, subjectName: subjectName, sections: st.Sections, actions: st.Actions}
	}
}

// defaultLabels fills any empty action label with the capability name minus the
// module prefix — the face's display fallback when a module omits a label.
func defaultLabels(actions []Action, shortName string) {
	for i := range actions {
		if actions[i].Label == "" {
			actions[i].Label = strings.TrimPrefix(actions[i].CapName, shortName+".")
		}
	}
}

// invokeAction fires the pending action's capability through the surface door
// and returns frameActionDoneMsg (empty err = success). A CONFIRMATION_REQUIRED
// cascade is recovered via protocol.AsCascade and surfaced to the running page.
func (o *Orchestrator) invokeAction(pa pendingAction) tea.Cmd {
	return func() tea.Msg {
		// Survey (bulk) actions carry no subject; scope+subjects[] arrive via
		// ExtraArgs instead. Only inject {subject} when there is a real subject.
		args := map[string]interface{}{}
		if pa.SubjectID != "" {
			args["subject"] = pa.SubjectID
		}
		for k, v := range pa.ExtraArgs {
			args[k] = v
		}
		if err := o.face.InvokeAction(pa.Action.CapName, args); err != nil {
			if cd, ok := protocol.AsCascade(err); ok {
				return frameActionDoneMsg{shortName: pa.ShortName, cascade: cd}
			}
			return frameActionDoneMsg{shortName: pa.ShortName, err: err.Error()}
		}
		return frameActionDoneMsg{shortName: pa.ShortName}
	}
}

// ── Inbound correlation (Update-loop only) ────────────────────────────────

// applyLoad folds a survey-load result into its mod. An async accept registers
// the token→mod binding (replaying any update that already parked under it); an
// inline (sync) result or a load error projects straight onto the mod. This is
// where "sync is the degenerate case of async" lands at the face: the inline
// branch and the pushed-terminal branch (apply, below) paint through the same
// projectSurvey.
func (o *Orchestrator) applyLoad(m moduleSurveyLoadedMsg) tea.Cmd {
	if m.accepted {
		o.register(m.token, m.mod)
		return o.afterSurvey(m.mod)
	}
	projectSurvey(m.mod, m)
	return o.afterSurvey(m.mod)
}

// ingest is the single sink for every pushed job_update. If its token is known
// it applies immediately; otherwise it parks the update until the matching load
// is accepted. This is the order-independent half: a terminal that beats its
// accept is held here, not dropped.
func (o *Orchestrator) ingest(token string, params protocol.JobUpdateParams) tea.Cmd {
	if mod, ok := o.known[token]; ok {
		o.apply(mod, params)
		if isTerminal(params.State) {
			delete(o.known, token)
			return o.afterSurvey(mod)
		}
		return nil
	}
	if tgt, ok := o.cellKnown[token]; ok {
		o.applyCell(tgt, params)
		if isTerminal(params.State) {
			delete(o.cellKnown, token)
		}
		return nil
	}
	o.park(token, params)
	return nil
}

// register binds a token to the mod that accepted its async load, then replays
// every update parked under that token in arrival order. The moment the face
// learns the token, the parking gap closes.
func (o *Orchestrator) register(token string, mod *modSt) {
	o.known[token] = mod
	for _, p := range o.parked[token] {
		o.apply(mod, p)
		if isTerminal(p.State) {
			delete(o.known, token)
		}
	}
	o.unpark(token)
}

// apply folds one pushed update into a mod's render state. A done update carries
// the same {subjects,…} output a sync invoke returns inline — decoded by the one
// shared surface decoder and projected through projectSurvey. A failed update
// surfaces the error; a running update is ignored (the spinner already animates).
func (o *Orchestrator) apply(mod *modSt, params protocol.JobUpdateParams) {
	switch params.State {
	case "done":
		sr, actions := surfacecore.DecodeSurveyOutput(params.Output)
		projectSurvey(mod, surveyLoadedMsg(mod, sr, actions))
	case "failed":
		mod.Loading = false
		if params.Error != nil {
			mod.Err = params.Error.Error()
		} else {
			mod.Err = "job failed"
		}
	}
}

// ── Cell fill (Update-loop only) ──────────────────────────────────────────

// afterSurvey fires the row cell-fill jobs once a survey has resolved cleanly
// (rows present, no spinner, no error). An accept that has not yet completed, a
// load error, or an empty survey returns nil — there is nothing to fill yet.
// This is the single seam where "core fires only the survey, the face requests
// the cells" lands: every clean survey outcome (inline, pushed done, replayed
// parked done) routes through here.
func (o *Orchestrator) afterSurvey(mod *modSt) tea.Cmd {
	if mod.Loading || mod.Err != "" || len(mod.Subjects) == 0 {
		return nil
	}
	return o.fillCellsCmd(mod)
}

// fillCellsCmd marks every `from` column of every row pending (so the survey
// paints spinners at once) and returns the batch that fires one job per
// distinct-from. Columns sharing a `from` collapse to one call, and that one call
// fills the whole column across every row from the returned row-keyed map — so the
// fan-out is O(distinct-froms), independent of row count. No `from` columns ⇒ nil
// (the survey already carries every cell).
func (o *Orchestrator) fillCellsCmd(mod *modSt) tea.Cmd {
	if mod.SurfaceConfig == nil {
		return nil
	}
	var order []string
	seen := map[string]bool{}
	for _, col := range mod.SurfaceConfig.Survey.Columns {
		if col.From == "" || seen[col.From] {
			continue
		}
		seen[col.From] = true
		order = append(order, col.From)
	}
	if len(order) == 0 {
		return nil
	}
	for _, subj := range mod.Subjects {
		colMap, _ := subj["columns"].(map[string]interface{})
		if colMap == nil {
			colMap = map[string]interface{}{}
			subj["columns"] = colMap
		}
		for _, capName := range order {
			for _, colID := range fromColumns(mod, capName) {
				markCellPending(colMap, colID)
			}
		}
	}
	var cmds []tea.Cmd
	for _, capName := range order {
		cmds = append(cmds, o.fillCellCmd(mod, capName))
	}
	return tea.Batch(cmds...)
}

// fillCellCmd fires one column `from` for the whole column through the surface
// door and folds the outcome into a cellLoadedMsg — the cell-fill peer of
// loadSurveyCmd. The survey-level scope (a drill child's parent row) rides as
// {scope} so the module returns the same rows the survey did. The I/O runs in the
// tea.Cmd goroutine; the message is applied back on the Update loop.
func (o *Orchestrator) fillCellCmd(mod *modSt, capName string) tea.Cmd {
	return func() tea.Msg {
		args := map[string]interface{}{}
		if mod.Scope != "" {
			args["scope"] = mod.Scope
		}
		load, err := o.face.FillCells(capName, args)
		if err != nil {
			return cellLoadedMsg{mod: mod, capName: capName, token: load.Token, err: err}
		}
		if load.Accepted {
			return cellLoadedMsg{mod: mod, capName: capName, token: load.Token, accepted: true}
		}
		return cellLoadedMsg{mod: mod, capName: capName, rows: load.Rows}
	}
}

// applyCellLoad folds a cell-fill outcome across every row. An async accept
// registers token→(mod,from) and replays any parked completion (the same
// park-then-replay gap the survey path closes); an inline result paints the
// row-keyed map at once; an error marks the shared columns errored on every row.
// Mirrors applyLoad.
func (o *Orchestrator) applyCellLoad(m cellLoadedMsg) {
	if m.err != nil {
		applyCellError(m.mod, m.capName)
		return
	}
	if m.accepted {
		o.registerCell(m.token, cellTarget{mod: m.mod, capName: m.capName})
		return
	}
	fillCellColumns(m.mod, m.capName, m.rows)
}

// registerCell binds a token to the cell target that accepted its async fill,
// then replays every update parked under that token in arrival order — the
// cell-fill peer of register.
func (o *Orchestrator) registerCell(token string, tgt cellTarget) {
	o.cellKnown[token] = tgt
	for _, p := range o.parked[token] {
		o.applyCell(tgt, p)
		if isTerminal(p.State) {
			delete(o.cellKnown, token)
		}
	}
	o.unpark(token)
}

// applyCell folds one pushed update into a cell target. A done update carries the
// row-keyed map (decoded by the shared cell decoder) painted across every row; a
// failed update marks the shared columns errored on every row; a running update is
// ignored (the spinner already animates). The cell-fill peer of apply.
func (o *Orchestrator) applyCell(tgt cellTarget, params protocol.JobUpdateParams) {
	switch params.State {
	case "done":
		fillCellColumns(tgt.mod, tgt.capName, surfacecore.DecodeCellOutput(params.Output))
	case "failed":
		applyCellError(tgt.mod, tgt.capName)
	}
}

// forget drops any in-flight correlation for mod so a fresh load supersedes it —
// both its survey token and any cell-fill tokens. A late terminal for an old
// token then finds no known mod/target and is parked as an orphan (evicted by
// the cap) rather than painting stale data onto the reload.
func (o *Orchestrator) forget(mod *modSt) {
	for tok, m := range o.known {
		if m == mod {
			delete(o.known, tok)
		}
	}
	for tok, t := range o.cellKnown {
		if t.mod == mod {
			delete(o.cellKnown, tok)
		}
	}
}

// park appends an unclaimed update under its token, evicting the oldest token
// when the cap is exceeded.
func (o *Orchestrator) park(token string, params protocol.JobUpdateParams) {
	if _, ok := o.parked[token]; !ok {
		o.order = append(o.order, token)
		for len(o.order) > parkedCap {
			evict := o.order[0]
			o.order = o.order[1:]
			delete(o.parked, evict)
		}
	}
	o.parked[token] = append(o.parked[token], params)
}

// unpark clears a token's parked updates and removes it from the FIFO.
func (o *Orchestrator) unpark(token string) {
	delete(o.parked, token)
	for i, t := range o.order {
		if t == token {
			o.order = append(o.order[:i], o.order[i+1:]...)
			break
		}
	}
}

// isTerminal reports whether a job_update state ends the job.
func isTerminal(state string) bool { return state == "done" || state == "failed" }

// projectSurvey writes a non-accept survey result (inline sync, pushed done, or
// load error) into the mod. Shared by applyLoad and apply so every survey
// outcome paints through one path. An accept never reaches here — it holds the
// spinner via register until its terminal update arrives.
func projectSurvey(mod *modSt, m moduleSurveyLoadedMsg) {
	mod.Loading = false
	if m.err != nil {
		mod.Err = m.err.Error()
		return
	}
	mod.Subjects = m.subjects
	mod.InlineActions = m.inlineActions
	mod.SurveyActions = m.surveyActions
	mod.Total = m.total
	mod.StatusSummary = m.statusSummary
	mod.Err = ""
}
