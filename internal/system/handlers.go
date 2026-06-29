// SPDX-License-Identifier: Apache-2.0

package system

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/solutionsunity/suctl/internal/activation"
	"github.com/solutionsunity/suctl/internal/module"
	"github.com/solutionsunity/suctl/sdk/manifest"
	"github.com/solutionsunity/suctl/sdk/protocol"
	"github.com/solutionsunity/suctl/sdk/surface"
	sdksystem "github.com/solutionsunity/suctl/sdk/system"
)

// invokeArgs extracts the capability args from a broker-routed request.
// When called through the broker the envelope is {name, args} — the actual
// args sit one level deeper under params["args"]. Fall back to params itself
// for direct (test) calls where no wrapping envelope is present.
//
// The envelope is detected by the presence of the "args" key, not its type: an
// invoke whose args is not an object (an empty struct in-process, or a non-object
// JSON value over the wire) still yields the inner args (an empty map here),
// never the envelope itself. This keeps the envelope's "name" (the capability
// name) from ever leaking through to a handler as a subject.
func invokeArgs(req *protocol.Request) map[string]interface{} {
	params, _ := req.Params.(map[string]interface{})
	if raw, ok := params["args"]; ok {
		args, _ := raw.(map[string]interface{})
		return args
	}
	return params
}

func (h *handlers) replSurvey(req *protocol.Request, _ module.CallerIdentity) *protocol.Response {
	return okResponse(BuildSurveyResponse(h.store))
}

// BuildSurveyResponse projects the modules store onto the surface.SurveyResponse
// DTO. Single source of truth for the system survey — shared by the broker
// handler and CLI diagnostic commands. The virtual system record is the control
// plane itself, not an operable module, so it is excluded from the listing.
// Each subject row is tagged with its state as a facet value; core filters
// locally (D68).
func BuildSurveyResponse(s *module.Store) surface.SurveyResponse {
	var ready, unavail, missing, failed, total int
	subjects := make([]surface.Subject, 0)
	for _, name := range s.Names() {
		if name == ShortName {
			continue
		}
		entry, ok := s.Get(name)
		if !ok {
			continue
		}
		total++
		state := entry.State()
		switch state {
		case module.StateReady:
			ready++
		case module.StateUnavailable:
			unavail++
		case module.StateMissing:
			missing++
		case module.StateFailed:
			failed++
		}

		caps := 0
		version := "0.0.0"
		if entry.Manifest != nil {
			caps = len(entry.Manifest.Capabilities)
			version = entry.Manifest.Version
		}

		subjects = append(subjects, surface.Subject{
			ID:   name,
			Name: name,
			Columns: map[string]surface.Column{
				"status":  surface.Col(displayState(state), statusColor(string(state))),
				"version": surface.Col(version, nil),
				"caps":    surface.Col(strconv.Itoa(caps), "blue"),
			},
			Facets: []string{string(state)},
		})
	}

	return surface.SurveyResponse{
		Total:         total,
		StatusSummary: buildSummary(ready, unavail, missing, failed),
		Subjects:      subjects,
	}
}

func (h *handlers) replFocus(req *protocol.Request, _ module.CallerIdentity) *protocol.Response {
	params := invokeArgs(req)
	shortName, _ := params["subject"].(string)
	if shortName == "" {
		return errorResponse(protocol.CodeInvalidParams, "subject is required")
	}

	resp, err := BuildFocusResponse(shortName, h.store)
	if err != nil {
		return errorResponse("NOT_FOUND", err.Error())
	}
	return okResponse(resp)
}

// BuildFocusResponse projects one module record onto the surface.FocusResponse
// DTO. Single source of truth for the focus view of the modules store.
func BuildFocusResponse(shortName string, s *module.Store) (surface.FocusResponse, error) {
	entry, ok := s.Get(shortName)
	if !ok {
		return surface.FocusResponse{}, fmt.Errorf("module %q not found", shortName)
	}

	caps := 0
	version := "0.0.0"
	moduleFull := "suctl-mod-" + shortName
	description := "no description"
	if entry.Manifest != nil {
		caps = len(entry.Manifest.Capabilities)
		version = entry.Manifest.Version
		description = entry.Manifest.Description
	}

	identityFields := []surface.Field{
		{Label: "module", Value: moduleFull},
		{Label: "version", Value: version},
		{Label: "status", Value: displayState(entry.State()), Color: statusColor(string(entry.State()))},
	}
	if reason := entry.Reason(); reason != "" {
		identityFields = append(identityFields, surface.Field{
			Label: "reason", Value: reason, Color: "alert", FullWidth: true,
		})
	}
	identityFields = append(identityFields,
		surface.Field{Label: "capabilities", Value: strconv.Itoa(caps), Color: "blue"},
		surface.Field{Label: "directory", Value: entry.Dir, FullWidth: true},
	)

	sections := []surface.Section{
		{Title: "identity", Fields: identityFields},
		{Title: "description", Fields: []surface.Field{
			{Label: "description", Value: description, FullWidth: true},
		}},
	}

	if reqSec := buildRequirementsSection(entry, s); reqSec != nil {
		sections = append(sections, *reqSec)
	}

	return surface.FocusResponse{
		ID:       shortName,
		Name:     shortName,
		Sections: sections,
		Actions:  focusActions(shortName, entry),
	}, nil
}

// moduleInventory returns the typed wire view of the modules store.
func (h *handlers) moduleInventory(req *protocol.Request, _ module.CallerIdentity) *protocol.Response {
	return okResponse(BuildInventoryResponse(h.store))
}

// BuildInventoryResponse projects the modules store onto the
// sdksystem.InventoryResponse DTO. Single source of truth for the wire
// inventory — shared by the broker handler and CLI diagnostic commands. The
// virtual system record is the control plane itself and is excluded.
func BuildInventoryResponse(s *module.Store) sdksystem.InventoryResponse {
	names := s.Names()

	resp := sdksystem.InventoryResponse{Entries: make([]sdksystem.InventoryEntry, 0, len(names))}
	for _, name := range names {
		if name == ShortName {
			continue
		}
		entry, ok := s.Get(name)
		if !ok {
			continue
		}

		desc := ""
		if entry.Manifest != nil {
			desc = entry.Manifest.Description
		}

		var rc json.RawMessage
		if len(entry.Surfaces) > 0 {
			rc, _ = json.Marshal(manifest.SurfaceFile{Surfaces: entry.Surfaces})
		}

		state := entry.State()
		resp.Entries = append(resp.Entries, sdksystem.InventoryEntry{
			ShortName:     name,
			Description:   desc,
			State:         string(state),
			Reason:        entry.Reason(),
			SurfaceConfig: rc,
		})
		switch state {
		case module.StateActive:
			resp.ActiveCount++
		case module.StateReady:
			resp.ReadyCount++
		}
	}
	return resp
}

func (h *handlers) moduleActivate(req *protocol.Request, _ module.CallerIdentity) *protocol.Response {
	params := invokeArgs(req)
	shortName, _ := params["subject"].(string)
	if shortName == "" {
		return errorResponse(protocol.CodeInvalidParams, "subject is required")
	}
	confirm, _ := params[protocol.ConfirmParam].(bool)

	entry, ok := h.store.Get(shortName)
	if !ok {
		return errorResponse("NOT_FOUND", fmt.Sprintf("module %q not found", shortName))
	}
	if entry.IsInert() {
		return errorResponse("NOT_INSTALLABLE", fmt.Sprintf("module %q is not fully installed (status: %s)", shortName, entry.State()))
	}
	if entry.State() == module.StateActive {
		return okResponse(map[string]string{
			"result":  "already_active",
			"message": fmt.Sprintf("module %q is already active", shortName),
		})
	}

	// When activation requires additional providers to become active,
	// the operator must see and confirm the full set first. Without confirm
	// the request is held and the cascade list returned as structured detail.
	providers := module.RequiredInactiveProviders(shortName, h.store)
	if len(providers) > 0 && !confirm {
		return confirmationRequiredResponse(shortName, providers)
	}

	// Confirmed (or no cascade needed). Write activation flags depth-first so
	// each provider's flag is in place before its dependents', then drive each
	// module's lifecycle directly in that same order — providers before the
	// target — so a dependent activates only after its providers are up. No
	// whole-store rescan poll: intent is persisted, then applied to exactly the
	// modules that changed.
	for _, prov := range providers {
		if err := activation.Activate(h.stateDir, prov); err != nil {
			return errorResponse(protocol.CodeInternalError, err.Error())
		}
	}
	if err := activation.Activate(h.stateDir, shortName); err != nil {
		return errorResponse(protocol.CodeInternalError, err.Error())
	}

	if h.activateFn != nil {
		for _, prov := range providers {
			h.activateFn(prov)
		}
		h.activateFn(shortName)
	}

	return okResponse(map[string]string{
		"result":  "ok",
		"message": fmt.Sprintf("module %q activated", shortName),
	})
}

// confirmationRequiredResponse builds the CONFIRMATION_REQUIRED error
// returned when activating target also implies activating providers.
// The detail payload schema is owned by protocol.CascadeDetail —
// every caller of system.module.activate reads it via protocol.AsCascade,
// so sender and receiver share one type.
func confirmationRequiredResponse(target string, providers []string) *protocol.Response {
	msg := fmt.Sprintf(
		"activating %q also requires activating: %s",
		target, strings.Join(providers, ", "),
	)
	return &protocol.Response{
		V:      protocol.Version,
		Status: "error",
		Error:  protocol.NewCascadeError(msg, protocol.CascadeDetail{Target: target, Providers: providers}),
	}
}

func (h *handlers) moduleDeactivate(req *protocol.Request, _ module.CallerIdentity) *protocol.Response {
	params := invokeArgs(req)
	shortName, _ := params["subject"].(string)
	if shortName == "" {
		return errorResponse(protocol.CodeInvalidParams, "subject is required")
	}

	// Existence check, mirroring moduleActivate: deactivating an unknown module
	// is a NOT_FOUND error, not a silent ok — without it a bogus subject would
	// run a no-op flag removal and report success.
	if _, ok := h.store.Get(shortName); !ok {
		return errorResponse("NOT_FOUND", fmt.Sprintf("module %q not found", shortName))
	}

	// Ops-gate: refuse a busy module before dropping its activation flag, so
	// disk intent never diverges from a module that keeps running because the
	// lifecycle deactivation is itself refused.
	if h.busyFn != nil {
		if token, busy := h.busyFn(shortName); busy {
			return errorResponse(protocol.CodeInternalError,
				fmt.Sprintf("module %q is busy (job %s holds its footprint); deactivation refused", shortName, token))
		}
	}

	if err := activation.Deactivate(h.stateDir, shortName); err != nil {
		return errorResponse(protocol.CodeInternalError, err.Error())
	}

	// Intent persisted; drive the one module's lifecycle directly so the next
	// survey reads the new state without a whole-store rescan poll.
	if h.deactivateFn != nil {
		h.deactivateFn(shortName)
	}

	return okResponse(map[string]string{
		"result":  "ok",
		"message": fmt.Sprintf("module %q deactivated", shortName),
	})
}

func buildSummary(ready, unavail, missing, failed int) string {
	var parts []string
	if ready > 0 {
		parts = append(parts, fmt.Sprintf("%d ready", ready))
	}
	if unavail > 0 {
		parts = append(parts, fmt.Sprintf("%d unavailable", unavail))
	}
	if missing > 0 {
		parts = append(parts, fmt.Sprintf("%d missing", missing))
	}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", failed))
	}
	return strings.Join(parts, " · ")
}

func displayState(s module.State) string {
	return string(s)
}

func statusColor(status string) interface{} {
	switch status {
	case "active":
		return "ok"
	case "ready":
		return "blue"
	case "unavailable":
		return "alert"
	case "missing":
		return "ghost"
	case "failed":
		return "err"
	default:
		return nil
	}
}

func focusActions(shortName string, entry *module.Record) []surface.Action {
	var actions []surface.Action
	switch entry.State() {
	case module.StateReady:
		actions = append(actions, surface.Action{
			Capability: "system.module.activate",
			Label:      "activate",
		})
	case module.StateActive:
		actions = append(actions, surface.Action{
			Capability: "system.module.deactivate",
			Label:      "deactivate",
		})
	case module.StateFailed:
		// Recovery: deactivating clears the failed verdict back to ready
		// so the operator can re-activate the module.
		actions = append(actions, surface.Action{
			Capability: "system.module.deactivate",
			Label:      "deactivate",
		})
	}
	return actions
}

// buildRequirementsSection constructs the "requirements" focus section for a
// module, listing each declared binary, path, socket, and capability with a
// ✓/✗ glyph indicating whether the requirement is currently met.
// Returns nil when the module declares no requirements.
//
// All probing is delegated to module.CheckAllRequirements so the focus view,
// diag dump, and FirstMissingRequirement share a single source of truth.
func buildRequirementsSection(entry *module.Record, s *module.Store) *surface.Section {
	checks := module.CheckAllRequirements(entry, module.ReadySurface(s), s, "")
	if len(checks) == 0 {
		return nil
	}
	fields := make([]surface.Field, 0, len(checks))
	for _, c := range checks {
		fields = append(fields, surface.Field{
			Label: c.Type,
			Value: requirementGlyph(c.Met) + "  " + decorateRequirement(c, s),
			Color: requirementColor(c.Met),
		})
	}
	return &surface.Section{Title: "requirements", Fields: fields}
}

// requirementGlyph returns the ✓/✗ glyph for a check result.
// Single source of truth for both the live focus view and diag dump.
func requirementGlyph(met bool) string {
	if met {
		return "✓"
	}
	return "✗"
}

// requirementColor returns the theme colour token matching met state.
// interface{} return matches surface.Field.Color; diag callers wrap to string.
func requirementColor(met bool) interface{} {
	if met {
		return "ok"
	}
	return "alert"
}

// decorateRequirement renders the value portion of a requirement field.
// For capability checks the provider module and its current state are
// appended in parentheses when known; other types render their value as-is.
func decorateRequirement(c module.RequirementCheck, s *module.Store) string {
	if c.Type != "capability" || c.Provider == "" || s == nil {
		return c.Value
	}
	prov, ok := s.Get(c.Provider)
	if !ok {
		return c.Value
	}
	return c.Value + "  (" + c.Provider + " · " + string(prov.State()) + ")"
}


