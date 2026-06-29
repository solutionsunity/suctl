// SPDX-License-Identifier: Apache-2.0

package module

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/solutionsunity/suctl/sdk/manifest"
)

// Scan walks the provided paths in order, loads every manifest.json it finds,
// and returns a Store. The scan order is:
//
//  1. SystemModulePath
//  2. DefaultThirdPartyPath
//  3. Each path in extraPaths (operator-declared in config.ModulePaths)
//
// If the same short name appears in two paths, the module is marked
// StateUnavailable in the store and a warning is appended — scan continues.
// Scan never returns a non-nil error for conflicts.
//
// Directories that do not contain a manifest.json are silently skipped.
// Directories with an invalid manifest.json are skipped with a warning
// recorded in the returned warnings slice.
func Scan(extraPaths []string) (*Store, []string, error) {
	return scanPaths(buildScanOrder(extraPaths))
}

// scanPaths is the unexported implementation used by both Scan and tests.
// It scans exactly the paths given — no system or default paths are prepended.
// Tests call this directly with explicit temp directories to avoid picking up
// any modules installed at SystemModulePath on the test host.
func scanPaths(paths []string) (*Store, []string, error) {
	store := NewStore()
	var warnings []string

	for _, root := range paths {
		entries, err := os.ReadDir(root)
		if err != nil {
			// SystemModulePath MUST exist; suctl will warn loudly if it is absent.
			if root == SystemModulePath {
				warnings = append(warnings, fmt.Sprintf("CRITICAL: system module path %q is missing; suctl core functions may be unavailable", root))
			}
			// Other paths do not exist or are not readable — not an error, just skip.
			continue
		}
		for _, de := range entries {
			if !de.IsDir() {
				continue
			}
			dir := filepath.Join(root, de.Name())
			m, err := manifest.LoadFromDir(dir)
			if err != nil {
				// Invalid/missing manifest — skip, warn.
				warnings = append(warnings, fmt.Sprintf("skip %s: %v", dir, err))
				continue
			}
			short := manifest.ShortNameFromDir(dir)
			if existing, dup := store.Get(short); dup {
				// Conflict handling is non-fatal.
				// Mark the already-indexed record unavailable (it may have been
				// StateReady) and record a warning. The duplicate is not added.
				ce := &ConflictError{ShortName: short, PathA: existing.Dir, PathB: dir}
				existing.SetStatus(StateUnavailable, ce.Error())
				warnings = append(warnings, ce.Error())
				continue
			}
			// Platform gate: a module whose platform list does not include the
			// current OS is indexed as StateUnavailable (visible, with a reason)
			// rather than silently skipped, so the inventory honestly reflects
			// every installed module regardless of host OS.
			state, reason := StateReady, ""
			if !platformSupported(m.Platform) {
				state = StateUnavailable
				reason = fmt.Sprintf("not supported on this platform (%s); module supports: %s",
					runtime.GOOS, strings.Join(m.Platform, ", "))
			} else if !entrypointResolvable(m.Entrypoint, dir) {
				// Entrypoint gate: a module whose launch program (Entrypoint[0])
				// is neither a file in its own directory nor a command on PATH can
				// never spawn. Index it visible-but-unavailable for the same honesty
				// reason as the platform gate, rather than advertising StateReady
				// only to fail at activation time.
				state = StateUnavailable
				reason = fmt.Sprintf("entrypoint %q not built or installed (not in module dir, not on PATH)",
					m.Entrypoint.Parts[0])
			}
			// Load surface.json if present; empty is the normal case for util modules.
			surfaces, surfErr := manifest.LoadSurfaceFromDir(dir)
			if surfErr != nil {
				warnings = append(warnings, fmt.Sprintf("skip surface for %s: %v", dir, surfErr))
			}
			store.Put(short, &Record{
				Manifest: m,
				Surfaces: surfaces,
				Dir:      dir,
				state:    state,
				reason:   reason,
			})
		}
	}
	return store, warnings, nil
}

// MarkMissing compares the set of previously activated module names against
// the discovered store. Any name that was activated but is no longer present is
// inserted as a StateMissing record (Manifest nil). Returns a warning string
// for each missing module so callers can surface them to the operator.
func MarkMissing(s *Store, activatedNames []string) []string {
	var warns []string
	for _, name := range activatedNames {
		if _, found := s.Get(name); !found {
			s.Put(name, &Record{
				state:  StateMissing,
				reason: "module directory or manifest.json no longer present on disk",
			})
			warns = append(warns, fmt.Sprintf("module %q was activated but is now missing from disk", name))
		}
	}
	return warns
}

// platformSupported reports whether the current OS (runtime.GOOS) is listed
// in the module's declared platform array. Silently returns false for an
// empty list — Validate() catches that separately.
func platformSupported(platforms []string) bool {
	current := runtime.GOOS
	for _, p := range platforms {
		if p == current {
			return true
		}
	}
	return false
}

// entrypointResolvable reports whether a module's launch program can actually
// be executed from dir, mirroring the supervisor's launch resolution: the
// program is Entrypoint[0] (the rest are arguments). It resolves iff it is an
// absolute path that exists, a file inside the module directory (a compiled
// binary or script launcher), or a command found on PATH (an interpreter such
// as "python3"/"node"). Manifest validation guarantees at least one part.
func entrypointResolvable(e manifest.Entrypoint, dir string) bool {
	prog := e.Parts[0]
	if filepath.IsAbs(prog) {
		_, err := os.Stat(prog)
		return err == nil
	}
	if _, err := os.Stat(filepath.Join(dir, prog)); err == nil {
		return true
	}
	_, err := exec.LookPath(prog)
	return err == nil
}

// buildScanOrder returns the ordered list of root paths to scan.
// System path first, default third-party second, operator extras after.
// Blank strings and duplicates are filtered out.
func buildScanOrder(extraPaths []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, p := range append([]string{SystemModulePath, DefaultThirdPartyPath}, extraPaths...) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}
