// SPDX-License-Identifier: Apache-2.0

package module

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/solutionsunity/suctl/sdk/manifest"
)

// RequirementCheck is the result of evaluating one declared requirement.
// Pure: produced by CheckAllRequirements, consumed by FirstMissingRequirement
// (state mutation) and by REPL/diag rendering (display). Single source of
// truth for what counts as "met" for binaries, paths, sockets, capabilities,
// and required config keys.
type RequirementCheck struct {
	// Type is one of "capability", "binary", "path", "socket", "config".
	Type string
	// Value is the requirement value (capability name, binary name, path, …).
	Value string
	// Met is true when the requirement is satisfied at evaluation time.
	Met bool
	// Provider is the module short name that declares Value as a capability.
	// Populated only when Type == "capability" and an idx was supplied;
	// empty otherwise.
	Provider string
}

// PendingSurface builds the complete set of declared capability names across
// all discovered modules, regardless of their state. This is the raw declared
// surface used for diagnostics and display — not for requirement evaluation.
func PendingSurface(s *Store) map[string]bool {
	return pendingSurface(s.snapshot())
}

func pendingSurface(recs map[string]*Record) map[string]bool {
	surface := make(map[string]bool)
	for _, r := range recs {
		if r.Manifest == nil {
			continue
		}
		for _, cap := range r.Manifest.Capabilities {
			surface[cap.Name] = true
		}
	}
	return surface
}

// ReadySurface builds the set of capability names provided only by modules
// that are not StateUnavailable or StateMissing. This is used in the second
// phase of EvaluateRequirements so that a capability is only considered
// available when its provider itself satisfies all system requirements.
func ReadySurface(s *Store) map[string]bool {
	return readySurface(s.snapshot())
}

func readySurface(recs map[string]*Record) map[string]bool {
	surface := make(map[string]bool)
	for _, r := range recs {
		if r.IsInert() || r.Manifest == nil {
			continue
		}
		for _, cap := range r.Manifest.Capabilities {
			surface[cap.Name] = true
		}
	}
	return surface
}

// FindCapabilityProvider returns the short name of the module that declares
// cap in its manifest, searching all modules regardless of state.
// Returns "" when no module in the store declares the capability.
func FindCapabilityProvider(cap string, s *Store) string {
	return findProvider(cap, s.snapshot())
}

func findProvider(cap string, recs map[string]*Record) string {
	for shortName, r := range recs {
		if r.Manifest == nil {
			continue
		}
		for _, c := range r.Manifest.Capabilities {
			if c.Name == cap {
				return shortName
			}
		}
	}
	return ""
}

// RequiredInactiveProviders returns the transitive list of provider short
// names that target depends on (via requires.capabilities) and that are
// currently StateReady but not yet StateActive. Order is depth-first so a
// provider precedes its dependents. The target itself is excluded; only
// other modules whose activation is implied by activating target appear.
// Providers that are not StateReady (e.g. unavailable, missing) are
// omitted — EvaluateRequirements has already marked the target unavailable
// in that case and activation will not be offered.
func RequiredInactiveProviders(target string, s *Store) []string {
	recs := s.snapshot()
	visited := map[string]bool{target: true}
	var out []string
	collectInactiveProviders(target, recs, visited, &out)
	return out
}

func collectInactiveProviders(name string, recs map[string]*Record, visited map[string]bool, out *[]string) {
	r, ok := recs[name]
	if !ok || r == nil || r.Manifest == nil {
		return
	}
	for _, cap := range r.Manifest.Requires.Capabilities {
		prov := findProvider(cap, recs)
		if prov == "" || visited[prov] {
			continue
		}
		provRec := recs[prov]
		if provRec == nil || provRec.State() != StateReady {
			continue
		}
		visited[prov] = true
		collectInactiveProviders(prov, recs, visited, out)
		*out = append(*out, prov)
	}
}

// Footprint returns the flat set of module short names that a job rooted at
// target can transitively reach via requires.capabilities — the static
// reservation set the gate holds while a job runs. It always includes
// target itself, is cycle-tolerant (each module appears once; a cycle back to
// an already-seen module terminates), and includes providers regardless of
// their current state: the footprint is the manifest dependency closure, not a
// runtime-availability view. The requires-gate keeps that declared set faithful to what the
// job may actually touch. A target absent from idx, or without a manifest, or
// whose required capabilities have no provider, yields just {target}.
//
// Unlike RequiredInactiveProviders (which is activation-ordering and filters to
// StateReady providers), Footprint is state-agnostic and unordered — it answers
// "which modules must be reserved", not "which must be activated first".
func Footprint(target string, s *Store) map[string]bool {
	recs := s.snapshot()
	out := map[string]bool{target: true}
	collectFootprint(target, recs, out)
	return out
}

func collectFootprint(name string, recs map[string]*Record, out map[string]bool) {
	r, ok := recs[name]
	if !ok || r == nil || r.Manifest == nil {
		return
	}
	for _, cap := range r.Manifest.Requires.Capabilities {
		prov := findProvider(cap, recs)
		if prov == "" || out[prov] {
			continue
		}
		out[prov] = true
		collectFootprint(prov, recs, out)
	}
}

// EvaluateRequirements runs a two-phase requirement check across all modules.
//
// Phase 1 — system requirements (binary, path, socket): marks modules
// StateUnavailable when a concrete system resource is absent.
//
// Phase 2 — capability requirements: rebuilds ReadySurface from the survivors
// and marks any module whose required capability is absent, iterating to a
// fixpoint so unavailability cascades through requirement chains of any depth:
// if certbot is unavailable (binary missing) then nginx — which requires
// certbot.cert.provision — becomes unavailable, and anything requiring an
// nginx capability follows on the next pass. Each reason names the provider
// and its state.
func EvaluateRequirements(s *Store) {
	recs := s.snapshot()

	// Phase 1: binary, path, socket checks.
	// Records already StateUnavailable (e.g. a conflict, or the platform gate in
	// Scan) keep their original, more-fundamental reason rather than being
	// re-marked with an incidental missing-binary one.
	for _, r := range recs {
		if r.State() == StateActive || r.State() == StateUnavailable || r.Manifest == nil {
			continue
		}
		evaluateSystemReqs(r)
	}

	// Phase 2: capability checks against the shrinking ready surface, repeated
	// until no module changes state.
	for {
		changed := false
		surface := readySurface(recs)
		for _, r := range recs {
			if r.State() == StateActive || r.State() == StateUnavailable || r.Manifest == nil {
				continue
			}
			for _, cap := range r.Manifest.Requires.Capabilities {
				if !surface[cap] {
					provider := findProvider(cap, recs)
					if provider != "" {
						r.SetStatus(StateUnavailable, fmt.Sprintf(
							"requires capability %q — provider %q is %s",
							cap, provider, recs[provider].State(),
						))
					} else {
						r.SetStatus(StateUnavailable, fmt.Sprintf(
							"requires capability %q which is not provided by any installed module",
							cap,
						))
					}
					changed = true
					break
				}
			}
		}
		if !changed {
			break
		}
	}
}

// evaluateSystemReqs checks binary, path, and socket requirements for a
// single record and marks it StateUnavailable on the first failure.
// Uses the shared checkSystemResources primitive so the same probes back
// the display path in CheckAllRequirements.
func evaluateSystemReqs(r *Record) {
	for _, c := range checkSystemResources(r.Manifest.Requires) {
		if !c.Met {
			r.SetStatus(StateUnavailable, systemReqReason(c))
			return
		}
	}
}

// systemReqReason renders the operator-facing reason for a failed
// binary/path/socket check. Wording matches the legacy evaluateSystemReqs
// messages so the focus view and tests remain stable.
func systemReqReason(c RequirementCheck) string {
	switch c.Type {
	case "binary":
		return fmt.Sprintf("requires binary %q which was not found in PATH", c.Value)
	case "path":
		return fmt.Sprintf("requires path %q which does not exist", c.Value)
	case "socket":
		return fmt.Sprintf("requires socket %q which does not exist", c.Value)
	}
	return ""
}

// EvaluateConfigRequirements checks each module's requires.config entries
// against its config file in confDir. For any required key not present in the
// file, the module is marked StateUnavailable.
func EvaluateConfigRequirements(s *Store, confDir string) {
	for shortName, r := range s.snapshot() {
		if r.State() == StateActive || r.IsInert() {
			continue
		}
		if r.Manifest == nil {
			continue
		}
		conf := ReadModuleConf(confDir, shortName)
		for _, ce := range r.Manifest.Requires.Config {
			if !ce.Required {
				continue
			}
			if _, ok := conf[ce.Key]; !ok {
				r.SetStatus(StateUnavailable, fmt.Sprintf("requires config key %q which is not set in %s/%s.conf",
					ce.Key, confDir, shortName))
				break
			}
		}
	}
}

// FirstMissingRequirement returns the first unsatisfied requirement for the
// record, or nil when all requirements are met. Walks the same ordered check
// list produced by CheckAllRequirements so display order and recheck order are
// guaranteed to agree.
func FirstMissingRequirement(r *Record, surface map[string]bool, confDir string) *RequirementFailure {
	for _, c := range CheckAllRequirements(r, surface, nil, confDir) {
		if !c.Met {
			return &RequirementFailure{Type: c.Type, Value: c.Value}
		}
	}
	return nil
}

// checkSystemResources runs the binary/path/socket probes for req and
// returns one RequirementCheck per declared entry. Pure — no state mutation.
func checkSystemResources(req manifest.Requires) []RequirementCheck {
	out := make([]RequirementCheck, 0, len(req.Binaries)+len(req.Paths)+len(req.Sockets))
	for _, bin := range req.Binaries {
		_, err := exec.LookPath(bin)
		out = append(out, RequirementCheck{Type: "binary", Value: bin, Met: err == nil})
	}
	for _, p := range req.Paths {
		_, err := os.Stat(p)
		out = append(out, RequirementCheck{Type: "path", Value: p, Met: err == nil})
	}
	for _, sock := range req.Sockets {
		_, err := os.Stat(sock)
		out = append(out, RequirementCheck{Type: "socket", Value: sock, Met: err == nil})
	}
	return out
}

// CheckAllRequirements is the canonical pure evaluation of an entry's
// declared requirements. It returns one RequirementCheck per declared
// requirement, in the order capability → binary → path → socket → config.
// Does not mutate entry state.
//
// Arguments:
//   - surface: the capability set to compare requires.capabilities against.
//     Pass ReadySurface(idx) for cascade-aware display, or PendingSurface(idx)
//     for on-requirement-missing rechecks.
//   - s: used only to populate RequirementCheck.Provider for capability
//     checks. Pass nil when the provider name is not needed.
//   - confDir: directory holding per-module .conf files. Empty disables
//     config-key checks (caller is not in a context where config matters).
func CheckAllRequirements(r *Record, surface map[string]bool, s *Store, confDir string) []RequirementCheck {
	if r == nil || r.Manifest == nil {
		return nil
	}
	var recs map[string]*Record
	if s != nil {
		recs = s.snapshot()
	}
	req := r.Manifest.Requires
	out := make([]RequirementCheck, 0,
		len(req.Capabilities)+len(req.Binaries)+len(req.Paths)+len(req.Sockets)+len(req.Config))

	for _, cap := range req.Capabilities {
		c := RequirementCheck{Type: "capability", Value: cap, Met: surface[cap]}
		if recs != nil {
			c.Provider = findProvider(cap, recs)
		}
		out = append(out, c)
	}
	out = append(out, checkSystemResources(req)...)

	if confDir != "" {
		conf := ReadModuleConf(confDir, manifest.ShortNameFromDir(r.Dir))
		for _, ce := range req.Config {
			if !ce.Required {
				continue
			}
			_, ok := conf[ce.Key]
			out = append(out, RequirementCheck{Type: "config", Value: ce.Key, Met: ok})
		}
	}
	return out
}
