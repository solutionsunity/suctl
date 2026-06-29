// SPDX-License-Identifier: Apache-2.0

package module

import (
	"github.com/solutionsunity/suctl/internal/wire"
)

// Route is the resolved dispatch target for a capability — the config-facet
// routing truth derived from the store, replacing the old broker Surface.
// Exactly one of Handler / Mux is meaningful: an in-process handler for a
// virtual module, or a spawned module's bidirectional broker wire.
type Route struct {
	// Module is the short name of the providing module.
	Module string
	// Mux is the target module's broker wire (nil for in-process handlers).
	Mux *wire.Mux
	// Handler is the in-process handler (nil for wire-routed modules).
	Handler InProcessHandler
	// Async is the capability's declared async mode.
	Async bool
}

// Resolve finds the dispatch target for capName. An in-process handler wins;
// otherwise the first active module that declares the capability provides it.
// Returns ok=false when no virtual handler and no active module provides it.
func (s *Store) Resolve(capName string) (Route, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for name, r := range s.records {
		if h, ok := r.Handlers[capName]; ok {
			return Route{Module: name, Handler: h, Async: capAsync(r, capName)}, true
		}
	}
	for name, r := range s.records {
		if r.State() != StateActive || r.Manifest == nil {
			continue
		}
		for _, c := range r.Manifest.Capabilities {
			if c.Name == capName {
				return Route{
					Module: name,
					Mux:    r.GetMux(),
					Async:  c.Async,
				}, true
			}
		}
	}
	return Route{}, false
}

// CapabilityAsync reports the declared async mode of capName on moduleName and
// whether the (module, capability) pair is present in the store. It reads the
// declared manifest flag — the same fact route resolution uses — never a runtime
// signal, so the answer matches the broker's actual dispatch contract.
func (s *Store) CapabilityAsync(moduleName, capName string) (async bool, found bool) {
	r, ok := s.Get(moduleName)
	if !ok || r.Manifest == nil {
		return false, false
	}
	for _, c := range r.Manifest.Capabilities {
		if c.Name == capName {
			return c.Async, true
		}
	}
	return false, false
}

// capAsync returns the declared async mode of capName on record r.
func capAsync(r *Record, capName string) bool {
	if r.Manifest == nil {
		return false
	}
	for _, c := range r.Manifest.Capabilities {
		if c.Name == capName {
			return c.Async
		}
	}
	return false
}

// Allows reports whether moduleName declared capName in requires.capabilities —
// the cross-module call gate, derived from the config facet (no separate
// registry).
func (s *Store) Allows(moduleName, capName string) bool {
	r, ok := s.Get(moduleName)
	if !ok || r.Manifest == nil {
		return false
	}
	for _, c := range r.Manifest.Requires.Capabilities {
		if c == capName {
			return true
		}
	}
	return false
}
