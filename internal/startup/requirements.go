// SPDX-License-Identifier: Apache-2.0

package startup

import (
	"fmt"
	"log/slog"

	"github.com/solutionsunity/suctl/internal/hooks"
	"github.com/solutionsunity/suctl/sdk/manifest"
	"github.com/solutionsunity/suctl/internal/module"
	"github.com/solutionsunity/suctl/sdk/paths"
)

// runRequirementMissingHooks fires the on-requirement-missing hook for each
// failing requirement, one at a time: after exit 0, re-check that one
// requirement; if still missing, mark unavailable and stop. If all requirements
// eventually pass, reset state to StateReady.
//
// The entry must be StateUnavailable when called. On success (all
// requirements pass), it is reset to StateReady. The hook itself fires
// through the lifecycle coordinator, the sole hook caller.
func (rt *Runtime) runRequirementMissingHooks(
	shortName string,
	entry *module.Record,
	hookDecl *manifest.HookDecl,
	runner *hooks.Runner,
	surface map[string]bool,
) {
	for {
		missing := module.FirstMissingRequirement(entry, surface, paths.ModuleConfDir)
		if missing == nil {
			// All requirements are now satisfied — reset state.
			entry.SetStatus(module.StateReady, "")
			slog.Info("module requirements resolved after on-requirement-missing hook",
				"module", shortName)
			return
		}
		// Fire the hook for this specific missing requirement.
		err := rt.lc().fireRequirementMissing(runner, hookDecl, missing.Type, missing.Value)
		if err != nil {
			// Hook failed or timed out — mark unavailable, stop.
			slog.Warn("on-requirement-missing hook failed",
				"module", shortName,
				"req_type", missing.Type,
				"req_value", missing.Value,
				"error", err)
			entry.SetStatus(module.StateUnavailable, fmt.Sprintf("requires %s %q (on-requirement-missing hook failed: %v)",
				missing.Type, missing.Value, err))
			return
		}
		// Hook exited 0 — re-check this specific requirement by calling
		// FirstMissingRequirement again. If the same requirement is still
		// missing, mark unavailable and stop.
		recheck := module.FirstMissingRequirement(entry, surface, paths.ModuleConfDir)
		if recheck != nil && recheck.Type == missing.Type && recheck.Value == missing.Value {
			slog.Warn("on-requirement-missing hook exited 0 but requirement still missing",
				"module", shortName,
				"req_type", missing.Type,
				"req_value", missing.Value)
			entry.SetStatus(module.StateUnavailable, fmt.Sprintf("requires %s %q which is not available", missing.Type, missing.Value))
			return
		}
		// Requirement resolved — loop to check the next one.
	}
}
