// SPDX-License-Identifier: Apache-2.0

// Package module implements module discovery and the modules store — the
// single source of truth for every module's config, identity, and supervision
// facets. Config, identity, and supervision are three facets of one per-module
// row, not separate truths.
//
// The package is split across several files:
//   - module.go       — State, constants, ConflictError, and store-level helpers
//   - store.go        — Record (the unified per-module row) and Store
//   - route.go        — capability resolution (Route) and the requires gate
//   - scan.go         — directory scanning and discovery
//   - requirements.go — requirement and capability evaluation
//   - moduleconf.go   — operator-editable config file parsing
package module

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/solutionsunity/suctl/sdk/paths"
)

// SystemModulePath is the first path scanned — all modules shipped with suctl
// and installed by `suctl install`. Resolved per-OS by sdk/paths.
var SystemModulePath = paths.SystemModulePath

// DefaultThirdPartyPath is the default path for third-party (non-suctl-shipped)
// module installations. Resolved per-OS by sdk/paths.
var DefaultThirdPartyPath = paths.ThirdPartyModulePath

// ActivationDir is where suctl writes per-module activation flag files.
// Resolved per-OS by sdk/paths (the single source of truth for all suctl paths).
var ActivationDir = paths.ModuleStateDir

// State represents the lifecycle state of a module.
type State string

const (
	// StateReady — requirements met, not yet activated by operator.
	StateReady State = "ready"
	// StateActive — activated, process running, capabilities registered.
	StateActive State = "active"
	// StateUnavailable — cannot be activated; Reason always set.
	StateUnavailable State = "unavailable"
	// StateMissing — was activated; directory or manifest.json no longer on disk.
	StateMissing State = "missing"
	// StateFailed — was active; runtime health checks failed and the configured
	// restart attempts were exhausted. Distinct from StateUnavailable,
	// which is a pre-activation verdict. Reason always set.
	StateFailed State = "failed"
)

// ConflictError describes a short-name collision found during Scan.
type ConflictError struct {
	ShortName string
	PathA     string
	PathB     string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf(
		"module conflict: %q found in two paths — remove one:\n  %s\n  %s",
		e.ShortName, e.PathA, e.PathB,
	)
}

// RequirementFailure identifies a single unmet requirement.
type RequirementFailure struct {
	Type  string
	Value string
}

// SortedKeys returns the store's record keys in lexicographic order.
// Deterministic iteration is required by phase 3b activation, REPL listings,
// and any diagnostic that compares output across runs.
func SortedKeys(s *Store) []string {
	return s.Names()
}

// IsOutOfSync reports whether runtime state (record state + live wire)
// diverges from the activation flags on disk — used by the REPL to show
// "restart required". A record is running when it is active and holds a wire.
func IsOutOfSync(s *Store, activationDir string) bool {
	for name, r := range s.snapshot() {
		if r.IsInert() {
			continue
		}
		isRunning := r.State() == StateActive && r.GetMux() != nil
		path := filepath.Join(activationDir, name+".flag")
		_, err := os.Stat(path)
		if isRunning != (err == nil) {
			return true
		}
	}
	return false
}

