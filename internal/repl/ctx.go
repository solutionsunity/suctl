// SPDX-License-Identifier: Apache-2.0

// Package repl — ctx.go defines AppCtx, the shared infrastructure handle
// passed by pointer to every page. All long-lived state (module
// inventory, terminal dimensions, broker client, bus JOBS view) lives here so
// pages mutate one source of truth and pop/push transitions are stateless.
//
// CoreMod is the static module-management surface backed by core protocol
// capabilities (system.module.survey / system.module.focus). It is always
// present — it is not a discovered module but a core face feature.
// Mods contains only active user modules that carry a REPL presence.
//
// State boundary: AppCtx never imports internal/module or internal/system
// for state. It holds the wire-shape sdksystem.InventoryResponse fetched
// via the broker and routes every module call back through that same
// in-process broker invoker — it never addresses a module directly. This
// makes REPL a strict protocol client; gRPC and HTTP faces use
// the same contract.
package repl

import (
	"net"
	"os"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	surfacecore "github.com/solutionsunity/suctl/internal/surface"
	"github.com/solutionsunity/suctl/sdk/manifest"
	"github.com/solutionsunity/suctl/sdk/surface"
	sdksystem "github.com/solutionsunity/suctl/sdk/system"
)

// SurfaceInvoker is the face's single door onto the surface orchestrator. The
// orchestrator owns identity (it mints the originating id and job_token) and is
// the surface's composition authority: it fires a capability, unwraps the
// invoke envelope, and decodes the module output into neutral SDK responses.
// The face adapts those into its own render model and never speaks the wire, so
// any future face (gRPC / HTTP) uses the same contract.
type SurfaceInvoker interface {
	// LoadSurvey fires a survey capability and returns a SurveyLoad: the minted
	// job_token (correlation key), whether the module accepted it async, and the
	// decoded survey plus survey-level (bulk) actions when the answer is inline.
	LoadSurvey(capName string, args map[string]interface{}) (surfacecore.SurveyLoad, error)
	// LoadFocus fires a focus capability for a subject and returns the focus body.
	LoadFocus(capName, subjectID string) (surface.FocusResponse, error)
	// FillCells fires a column's `from` capability for the whole column and returns
	// a CellLoad: the minted job_token (correlation key), whether the module
	// accepted it async, and (when inline) the decoded row-keyed map. args carries
	// the survey-level context (e.g. {scope} for a drill child). One call fills
	// every column sharing that `from` across all rows; the face applies each row's
	// map to the matching columns of that row.
	FillCells(capName string, args map[string]interface{}) (surfacecore.CellLoad, error)
	// InvokeAction fires an action capability; a CONFIRMATION_REQUIRED cascade is
	// preserved in the returned error (recover it via protocol.AsCascade).
	InvokeAction(capName string, args map[string]interface{}) error
	// Inventory fires system.module.inventory and returns the decoded index.
	Inventory() (sdksystem.InventoryResponse, error)
	// Updates is the inbound stream of job_update deliveries the broker pushes
	// for face-originated jobs. The face drains it and adapts each delivery into
	// its render model; it never polls.
	Updates() <-chan surfacecore.JobUpdate
}

// AppCtx is the shared infrastructure handed to every page by pointer.
// Pages read width/height/mods at render time; mutations propagate
// without further plumbing because every page holds the same *AppCtx.
type AppCtx struct {
	// Inventory is the typed wire view of the module index fetched from
	// system.module.inventory. Refresh after every state-changing action.
	Inventory sdksystem.InventoryResponse
	// CoreMod is the static module-management surface wired to the broker.
	// Always present; not part of Mods.
	CoreMod *modSt
	// JobsMod is the static jobs surface — virtual modSt that drives the REPL
	// jobs survey/focus pages via system.jobs.* (identity-scoped).
	JobsMod *modSt
	// MessagesMod is the static messages surface — virtual modSt that drives the
	// REPL raw-message survey/focus pages via system.messages.*.
	MessagesMod *modSt
	// orch is the REPL-side surface orchestrator — the face's single door in both
	// directions. Outbound it fires loads/focus/actions through the surface
	// orchestrator (which mints id+job_token and calls the broker); inbound it
	// correlates the pushed job_update stream back onto the originating mod. The
	// REPL is part of the core process, so there is no socket.
	orch     *Orchestrator
	Warns    []string
	Hostname string
	IP       string
	Width    int
	Height   int

	// Mods holds active user modules that carry a REPL presence (alphabetical).
	Mods []*modSt
}

// newAppCtx builds an AppCtx over the surface door (face). It constructs the
// REPL orchestrator — the single door for all surface transport, in both
// directions — and fetches the initial inventory snapshot *through it* so the
// home page renders with state on the first frame. No transport call is ever
// made outside the orchestrator. An inventory failure degrades gracefully: the
// home page shows an empty list (recorded as a warn) and the operator can rescan.
func newAppCtx(warns []string, face SurfaceInvoker) *AppCtx {
	c := &AppCtx{
		CoreMod:     staticModSt(sdksystem.SubjectModule),
		JobsMod:     staticModSt(sdksystem.SubjectJob),
		MessagesMod: staticModSt(sdksystem.SubjectMessage),
		orch:        newOrchestrator(face),
		Warns:       warns,
		Hostname:    probeHostname(),
		IP:          probeIP(),
	}
	if inv, err := c.orch.Inventory(); err != nil {
		c.Warns = append(c.Warns, "inventory fetch failed: "+err.Error())
	} else {
		c.Inventory = inv
	}
	c.Mods = buildMods(c.Inventory)
	return c
}

// refreshInventory re-fetches the inventory from system.module.inventory
// and rebuilds the user-mod list. Called after any state-changing action
// (activate / deactivate) so the home page sees the new state without
// reaching into core's in-process index. CoreMod is static and never refreshed.
func (c *AppCtx) refreshInventory() {
	if inv, err := c.orch.Inventory(); err == nil {
		c.Inventory = inv
	}
	c.Mods = buildMods(c.Inventory)
}

// UserMods returns all active user modules that carry a REPL presence.
func (c *AppCtx) UserMods() []*modSt { return c.Mods }

// InventoryMod returns the static module-management surface (CoreMod) — the
// REPL-side proxy for the core system.module.survey / system.module.focus
// capabilities. Distinct from the Inventory field, which carries the
// wire-shape module index snapshot.
func (c *AppCtx) InventoryMod() *modSt { return c.CoreMod }

// ModByShortName returns the mod with the given short name, or nil.
// CoreMod and JobsMod are checked first, then user mods.
func (c *AppCtx) ModByShortName(name string) *modSt {
	if c.CoreMod.ShortName == name {
		return c.CoreMod
	}
	if c.JobsMod != nil && c.JobsMod.ShortName == name {
		return c.JobsMod
	}
	if c.MessagesMod != nil && c.MessagesMod.ShortName == name {
		return c.MessagesMod
	}
	for _, m := range c.Mods {
		if m.ShortName == name {
			return m
		}
	}
	return nil
}

// tickSpinners advances every loading spinner across CoreMod and all user
// mods. Pages call this on spinner.TickMsg so animation keeps going
// regardless of which page is on top of the stack. Returns the batched Cmd.
func (c *AppCtx) tickSpinners(msg spinner.TickMsg) tea.Cmd {
	var cmds []tea.Cmd
	all := append([]*modSt{c.CoreMod, c.JobsMod, c.MessagesMod}, c.Mods...)
	for _, m := range all {
		if m != nil && (m.Loading || m.hasPendingCells()) {
			var cmd tea.Cmd
			m.Spinner, cmd = m.Spinner.Update(msg)
			cmds = append(cmds, cmd)
		}
	}
	return tea.Batch(cmds...)
}

// beginLoadAll triggers an async survey reload for CoreMod and every user
// mod. Used by homePage.Init and after a rescan.
func (c *AppCtx) beginLoadAll() tea.Cmd {
	cmds := []tea.Cmd{c.orch.beginLoad(c.CoreMod, nil)}
	if c.JobsMod != nil {
		cmds = append(cmds, c.orch.beginLoad(c.JobsMod, nil))
	}
	if c.MessagesMod != nil {
		cmds = append(cmds, c.orch.beginLoad(c.MessagesMod, nil))
	}
	for _, m := range c.Mods {
		cmds = append(cmds, c.orch.beginLoad(m, nil))
	}
	return tea.Batch(cmds...)
}

// buildMods returns the active user modules that carry a REPL presence,
// in the deterministic order delivered by system.module.inventory. A module
// that declares several surfaces fans out into one home-page row per surface
// (multi-subject), preserving surface order. CoreMod (the module-management
// surface) is not part of this list.
func buildMods(inv sdksystem.InventoryResponse) []*modSt {
	var mods []*modSt
	for _, e := range inv.Entries {
		if e.State != sdksystem.StateActive {
			continue
		}
		if len(e.SurfaceConfig) == 0 {
			continue
		}
		mods = append(mods, modStsFromInventory(e)...)
	}
	return mods
}

// staticModSt builds an always-present core modSt for the given subject from
// the canonical core surface. These mods are wired directly to the in-process
// broker where the system.* capabilities are registered; they are not discovered
// modules. The surface — subject, columns, facets, survey/focus entries, and the
// surface's name/description — is declared once in core's surface.json
// (sdk/system), the single source of truth shared by every face and by core
// BIST. REPL reads it through sdk/system, never through internal/system (CQ21
// protocol boundary): the system capabilities are protocol-first constants, not
// runtime-discovered features. The module surface is the management surface
// (system.module.*); the jobs surface is read-only by design — it declares no
// actions — and caller identity filtering is applied server-side.
func staticModSt(subject string) *modSt {
	rc := sdksystem.Surface(subject)
	if rc == nil {
		return nil
	}
	return &modSt{
		ShortName:     rc.Name,
		Desc:          rc.Desc,
		SurveyCapName: rc.Survey.Entry,
		FocusCapName:  rc.Focus.Entry,
		SurfaceConfig: rc,
		Loading:       true,
		Spinner:       newSpinner(),
	}
}

// homeFacets is the fixed HOME-level facet list.
// "all" is intentionally absent — selecting no facet is equivalent to all.
var homeFacets = []manifest.SurfaceFacetConfig{
	{Label: "has-warnings", Value: "has-warnings"},
}

func probeHostname() string {
	h, _ := os.Hostname()
	if h == "" {
		return "—"
	}
	return h
}

func probeIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "—"
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagLoopback != 0 || ifc.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip != nil && !ip.IsLoopback() {
				if ip4 := ip.To4(); ip4 != nil {
					return ip4.String()
				}
			}
		}
	}
	return "—"
}
