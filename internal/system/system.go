// SPDX-License-Identifier: Apache-2.0

// Package system implements the suctl control-plane capabilities.
// These capabilities are registered directly with the broker as in-process
// handlers.
package system

import (
	"encoding/json"

	"github.com/solutionsunity/suctl/internal/messages"
	"github.com/solutionsunity/suctl/internal/module"
	"github.com/solutionsunity/suctl/sdk/manifest"
	"github.com/solutionsunity/suctl/sdk/protocol"
	sdksystem "github.com/solutionsunity/suctl/sdk/system"
)

// ShortName is the reserved short name for the system module.
const ShortName = "system"

// Register installs all system capabilities as in-process handlers on the
// modules store under the virtual system record. The store is also the
// index the handlers project for inventory/survey/focus.
// activateFn / deactivateFn drive a single module's lifecycle directly after the
// handler persists intent to disk — there is no whole-store rescan poll. Pass nil
// to skip the lifecycle step (e.g. in tests where the lifecycle is not running).
// msgs is the messages store; the system.jobs.* handlers derive jobs from it
// (with caller-identity filtering) and the system.messages.* handlers list its
// records. Pass nil in tests that do not exercise those capabilities.
// busyFn reports whether a module is currently busy (a running job's footprint
// covers it); moduleDeactivate consults it to refuse deactivating a busy module
// before any activation flag is dropped. Pass nil to skip the busy-check.
func Register(store *module.Store, stateDir string, msgs *messages.Store, activateFn, deactivateFn func(string), busyFn func(string) (string, bool)) {
	h := &handlers{store: store, stateDir: stateDir, msgs: msgs, activateFn: activateFn, deactivateFn: deactivateFn, busyFn: busyFn}
	m := Manifest()
	store.RegisterHandler(ShortName, m, "system.module.survey", h.replSurvey)
	store.RegisterHandler(ShortName, m, "system.module.focus", h.replFocus)
	store.RegisterHandler(ShortName, m, "system.jobs.survey", h.replJobsSurvey)
	store.RegisterHandler(ShortName, m, "system.jobs.focus", h.replJobsFocus)
	store.RegisterHandler(ShortName, m, "system.messages.survey", h.replMessagesSurvey)
	store.RegisterHandler(ShortName, m, "system.messages.focus", h.replMessagesFocus)
	store.RegisterHandler(ShortName, m, "system.module.inventory", h.moduleInventory)
	store.RegisterHandler(ShortName, m, "system.module.activate", h.moduleActivate)
	store.RegisterHandler(ShortName, m, "system.module.deactivate", h.moduleDeactivate)
}

// Manifest returns the virtual manifest for the system module.
func Manifest() *manifest.Manifest {
	return &manifest.Manifest{
		Version:     "0.1.0",
		Protocol:    "1",
		Platform:    []string{"linux"},
		Author:      "suctl",
		License:     "Apache-2.0",
		Description: "suctl control-plane — discover, activate, and deactivate modules.",
		Capabilities: []manifest.Capability{
			{
				Name:        "system.module.survey",
				Description: "List all discovered modules with activation status for the survey.",
				Params: []manifest.Param{
					{Name: "filter", Type: "string", Required: false, Description: "Filter by status: all, active, ready, unavailable, missing"},
				},
			},
			{
				Name:        "system.module.focus",
				Description: "Return full detail for a selected module.",
				Params: []manifest.Param{
					{Name: "subject", Type: "string", Required: true, Description: "Module short name (opaque id from survey)"},
				},
			},
			{
				Name:        "system.module.inventory",
				Description: "Return all discovered modules with state, socket path, and surface config — the canonical wire view of the module index for any face (REPL, gRPC, HTTP).",
			},
			{
				Name:        "system.jobs.survey",
				Description: "List jobs visible to the caller. The system caller (empty caller, in-process face) sees all jobs; a module caller sees only its own jobs. Each row is tagged with its state as a facet value; core filters locally.",
				Params:      []manifest.Param{},
			},
			{
				Name:        "system.jobs.focus",
				Description: "Return full detail for a single job by its job token. Caller must have visibility to the job.",
				Params: []manifest.Param{
					{Name: "subject", Type: "string", Required: true, Description: "Job token (opaque id from jobs survey)"},
				},
			},
			{
				Name:        "system.messages.survey",
				Description: "List recorded message exchanges visible to the caller (system → all; module → own jobs').",
			},
			{
				Name:        "system.messages.focus",
				Description: "Return full detail for a selected message exchange. Caller must have visibility to the exchange.",
				Params: []manifest.Param{
					{Name: "subject", Type: "string", Required: true, Description: "Message id (opaque id from messages survey)"},
				},
			},
			{
				Name:        "system.module.activate",
				Description: "Activate a module by short name.",
				Params: []manifest.Param{
					{Name: "name", Type: "string", Required: true, Description: "Module short name"},
				},
			},
			{
				Name:        "system.module.deactivate",
				Description: "Deactivate a module by short name.",
				Params: []manifest.Param{
					{Name: "name", Type: "string", Required: true, Description: "Module short name"},
				},
			},
		},
	}
}

// SurfaceConfig returns the control-plane "module" surface. The definition is
// owned by core's surface.json (sdk/system) — the single source of truth shared
// by every face and by core BIST — so this is a thin typed accessor, not a
// second definition.
func SurfaceConfig() *manifest.SurfaceConfig {
	return sdksystem.Surface(sdksystem.SubjectModule)
}

// JobsSurfaceConfig returns the control-plane "job" surface from core's
// surface.json (sdk/system). See SurfaceConfig.
func JobsSurfaceConfig() *manifest.SurfaceConfig {
	return sdksystem.Surface(sdksystem.SubjectJob)
}

// MessagesSurfaceConfig returns the control-plane "message" surface from core's
// surface.json (sdk/system). See SurfaceConfig.
func MessagesSurfaceConfig() *manifest.SurfaceConfig {
	return sdksystem.Surface(sdksystem.SubjectMessage)
}

// AllSurfaces returns every control-plane surface declared in core's
// surface.json (module, job, message). Core BIST validates the REPL contract of
// each one at startup, so a surface's survey/columns/facets/focus is self-tested
// rather than only the module surface.
func AllSurfaces() []manifest.SurfaceConfig {
	return sdksystem.Surfaces()
}

type handlers struct {
	store        *module.Store
	stateDir     string
	msgs         *messages.Store             // optional — drives system.jobs.* and system.messages.*
	activateFn   func(string)                // optional — hot-loads one module after its flag is written
	deactivateFn func(string)                // optional — hot-unloads one module after its flag is dropped
	busyFn       func(string) (string, bool) // optional — gate busy-check for deactivate
}

func okResponse(v interface{}) *protocol.Response {
	b, _ := json.Marshal(v)
	return &protocol.Response{
		V:      protocol.Version,
		Status: "ok",
		Result: b,
	}
}

func errorResponse(code, message string) *protocol.Response {
	return &protocol.Response{
		V:      protocol.Version,
		Status: "error",
		Error: &protocol.ErrorDetail{
			Code:    code,
			Message: message,
		},
	}
}
