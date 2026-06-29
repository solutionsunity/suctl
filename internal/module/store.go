// SPDX-License-Identifier: Apache-2.0

package module

import (
	"sort"
	"sync"

	"github.com/solutionsunity/suctl/internal/health"
	"github.com/solutionsunity/suctl/internal/supervisor"
	"github.com/solutionsunity/suctl/internal/wire"
	"github.com/solutionsunity/suctl/sdk/manifest"
	"github.com/solutionsunity/suctl/sdk/protocol"
)

// CallerIdentity names the module on whose wire a request arrived. An
// empty Module is the originating face (REPL); a non-empty Module is the
// core-managed module that holds that inherited wire.
type CallerIdentity struct {
	Module string
}

// InProcessHandler implements a capability inside core (virtual modules such as
// system). It receives the request and the caller identity of the wire it
// arrived on and returns the response.
type InProcessHandler func(req *protocol.Request, caller CallerIdentity) *protocol.Response

// Record is the core's unified per-module row — the single truth for one module
// across three facets:
//
//   - config:      the discovered manifest, surface config, directory, and
//                  lifecycle state. Manifest/Surfaces/Dir/Handlers are immutable
//                  after scan/registration; state and reason are mutated by
//                  requirement evaluation and the lifecycle.
//   - identity:    the broker wire Mux — core's end of the module's broker
//                  socketpair, rebound on every (re)launch; possession of the
//                  child end is identity. It carries both directions, so it is
//                  also the transport for core->module calls (handshake, health,
//                  hooks, broker forwards).
//   - supervision: supervisor and monitor — the live handles, set at activation
//                  and cleared at teardown.
//
// The mutable facets (state, reason, supervisor, monitor, mux) are guarded by
// one RWMutex: the lifecycle coordinator, health-monitor goroutines, and the
// broker all touch the same record concurrently. Access them only through the
// accessors below.
type Record struct {
	// ---- config facet (immutable after scan/registration) ----
	Manifest *manifest.Manifest
	// Surfaces is the module's parsed surface.json surfaces. A module
	// may expose several co-equal surfaces (multi-subject); empty for util modules.
	Surfaces []manifest.SurfaceConfig
	Dir      string

	// Handlers holds in-process capability handlers for a virtual module
	// (system) whose capabilities execute inside core rather than over a wire.
	Handlers map[string]InProcessHandler

	// mu guards every mutable facet below.
	mu     sync.RWMutex
	state  State
	reason string

	// ---- supervision facet ----
	supervisor *supervisor.Supervisor
	monitor    *health.Monitor

	// ---- identity facet ----
	// mux is core's end of the module's bidirectional broker wire — the
	// supervisor rebinds it (SetMux) on every relaunch while the health monitor
	// and broker read it concurrently (GetMux).
	mux *wire.Mux
}

// NewRecord constructs a Record in the given lifecycle state. External packages
// (and their tests) build records through this constructor; the module package
// itself may use literals.
func NewRecord(st State, m *manifest.Manifest) *Record {
	return &Record{state: st, Manifest: m}
}

// State returns the module's lifecycle state.
func (r *Record) State() State {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state
}

// Reason returns the operator-facing explanation for the current state
// (always set for unavailable/missing/failed; empty otherwise).
func (r *Record) Reason() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.reason
}

// SetStatus atomically sets the lifecycle state and its reason.
func (r *Record) SetStatus(st State, reason string) {
	r.mu.Lock()
	r.state = st
	r.reason = reason
	r.mu.Unlock()
}

// Supervisor returns the live supervisor handle, or nil when the module's
// process is not supervised.
func (r *Record) Supervisor() *supervisor.Supervisor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.supervisor
}

// SetSupervisor binds the live supervisor handle (set at activation).
func (r *Record) SetSupervisor(s *supervisor.Supervisor) {
	r.mu.Lock()
	r.supervisor = s
	r.mu.Unlock()
}

// TakeSupervisor atomically detaches and returns the supervisor handle (nil if
// none). The swap-and-return makes teardown race-free: exactly one caller stops
// a given supervisor.
func (r *Record) TakeSupervisor() *supervisor.Supervisor {
	r.mu.Lock()
	s := r.supervisor
	r.supervisor = nil
	r.mu.Unlock()
	return s
}

// Monitor returns the live health monitor handle, or nil when none is running.
func (r *Record) Monitor() *health.Monitor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.monitor
}

// SetMonitor binds the live health monitor handle (set at activation).
func (r *Record) SetMonitor(m *health.Monitor) {
	r.mu.Lock()
	r.monitor = m
	r.mu.Unlock()
}

// TakeMonitor atomically detaches and returns the monitor handle (nil if none),
// so exactly one caller stops a given monitor.
func (r *Record) TakeMonitor() *health.Monitor {
	r.mu.Lock()
	m := r.monitor
	r.monitor = nil
	r.mu.Unlock()
	return m
}

// SetMux binds (or, with nil, clears) core's end of the module's broker wire.
// Called from the supervisor's per-launch OnChannel callback and from teardown.
func (r *Record) SetMux(m *wire.Mux) {
	r.mu.Lock()
	r.mux = m
	r.mu.Unlock()
}

// GetMux returns the live broker wire mux, or nil when the module is not
// currently running.
func (r *Record) GetMux() *wire.Mux {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.mux
}

// IsInert reports whether the record cannot be acted on — unavailable (missing
// requirements) or its files have vanished from disk.
func (r *Record) IsInert() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state == StateUnavailable || r.state == StateMissing
}

// Store is the modules store: the single source of truth for module config,
// identity, and supervision, keyed by short name and guarded by mu.
type Store struct {
	mu      sync.RWMutex
	records map[string]*Record
}

// NewStore returns an empty modules store.
func NewStore() *Store { return &Store{records: make(map[string]*Record)} }

// Get returns the record for name and whether it is present.
func (s *Store) Get(name string) (*Record, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.records[name]
	return r, ok
}

// Put inserts or replaces the record for name.
func (s *Store) Put(name string, r *Record) {
	s.mu.Lock()
	s.records[name] = r
	s.mu.Unlock()
}

// Delete removes the record for name.
func (s *Store) Delete(name string) {
	s.mu.Lock()
	delete(s.records, name)
	s.mu.Unlock()
}

// Len returns the number of records.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.records)
}

// Names returns all record keys in lexicographic order.
func (s *Store) Names() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.records))
	for k := range s.records {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// snapshot returns a shallow copy of the records map. Record pointers are
// shared, so mutating a record through the snapshot mutates the stored record;
// only the map structure is decoupled, which is all the pure folds need.
func (s *Store) snapshot() map[string]*Record {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]*Record, len(s.records))
	for k, v := range s.records {
		out[k] = v
	}
	return out
}

// RegisterHandler installs an in-process capability handler under a virtual
// module record, creating the record (active, with the given manifest) if it
// does not yet exist.
func (s *Store) RegisterHandler(moduleName string, m *manifest.Manifest, capName string, h InProcessHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[moduleName]
	if !ok {
		r = &Record{state: StateActive, Manifest: m, Handlers: map[string]InProcessHandler{}}
		s.records[moduleName] = r
	}
	if r.Handlers == nil {
		r.Handlers = map[string]InProcessHandler{}
	}
	r.Handlers[capName] = h
}
