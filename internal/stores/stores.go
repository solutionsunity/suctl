// SPDX-License-Identifier: Apache-2.0

// Package stores owns core's two stores and their boot-time builder. It is the
// single place that constructs the modules store (discovery over disk) and the
// empty messages store and hands them to the orchestrators. Build is the one
// sanctioned writer that precedes the orchestrators; once core is running, each
// store has exactly one runtime writer — its orchestrator (the supervisor over
// modules, the broker over messages).
package stores

import (
	"github.com/solutionsunity/suctl/internal/messages"
	"github.com/solutionsunity/suctl/internal/module"
)

// Stores is core's pair of stores, constructed together at boot and wired into
// startup in one step so no caller reaches around to build a store directly.
type Stores struct {
	// Modules is the modules store: every module's config/identity/supervision
	// facets, keyed by short name.
	Modules *module.Store
	// Messages is the messages store: the recorded work, keyed by request id.
	Messages *messages.Store
}

// Build runs the boot-time discovery pipeline for the modules store and creates
// the empty messages store.
//
// The modules pipeline is the established boot sequence (phases 1–3a):
//   - Scan walks modulePaths and loads every manifest into the store.
//   - MarkMissing flags previously-activated modules now absent from disk.
//   - EvaluateRequirements cascades system/capability requirement failures.
//   - EvaluateConfigRequirements marks modules missing a required config key.
//
// activatedNames is the persisted activation list (read by the caller).
// confDir holds the per-module .conf files. Warnings accumulated by the pipeline
// are returned for the caller to log; a non-nil error is fatal (scan failure).
func Build(modulePaths, activatedNames []string, confDir string) (*Stores, []string, error) {
	mod, warns, err := module.Scan(modulePaths)
	if err != nil {
		return nil, warns, err
	}
	warns = append(warns, module.MarkMissing(mod, activatedNames)...)
	module.EvaluateRequirements(mod)
	module.EvaluateConfigRequirements(mod, confDir)
	return &Stores{Modules: mod, Messages: messages.New()}, warns, nil
}
