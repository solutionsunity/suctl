// SPDX-License-Identifier: Apache-2.0

// Package startup executes the full module activation sequence at core startup.
//
// It is the single place where module supervisors, broker wires, handshakes,
// hook calls, broker registration, and health monitors are wired together.
// Run returns a Runtime; the REPL face drives it today, and any future face
// (gRPC / HTTP) will call the same Run and receive the same Runtime.
//
// The package is split across several files within the same package:
//   - startup.go      — public API: Runtime, Run, constants, helpers
//   - lifecycle.go    — Coordinator: the lifecycle orchestrator and sole hook caller
//   - activate.go     — Coordinator.Activate, Runtime.ActivateModule, startProcess
//   - deactivate.go   — Coordinator.Deactivate, Runtime.DeactivateModule
//   - stop.go         — Coordinator.Shutdown (graceful shutdown sequence)
//   - requirements.go — on-requirement-missing pre-pass
//   - handshake.go    — handshake primitive over the broker wire
package startup

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/solutionsunity/suctl/internal/broker"
	"github.com/solutionsunity/suctl/internal/config"
	"github.com/solutionsunity/suctl/internal/conformance"
	"github.com/solutionsunity/suctl/internal/gate"
	"github.com/solutionsunity/suctl/internal/hooks"
	"github.com/solutionsunity/suctl/internal/messages"
	"github.com/solutionsunity/suctl/internal/module"
	"github.com/solutionsunity/suctl/internal/stores"
	"github.com/solutionsunity/suctl/internal/surface"
	"github.com/solutionsunity/suctl/internal/system"
	"github.com/solutionsunity/suctl/sdk/paths"
)

// tunables holds the core-internal timeouts/limits resolved from suctl.conf
// (Gate D). Run builds it once from *config.Config; the Coordinator reads it
// when constructing per-module handshakes, monitors, supervisors, and hooks.
// Downstream constructors apply their own zero→default fallback, so a zero
// here is still safe.
type tunables struct {
	handshake        time.Duration // module first-handshake deadline
	healthInterval   time.Duration // per-module health-check period
	healthFailThresh int           // consecutive failures before escalation
	hookTimeout      time.Duration // fallback per-hook deadline
	supMaxRestarts   int           // crash-loop guard threshold
	supRestartWindow time.Duration // crash-loop guard window
	supStopTimeout   time.Duration // SIGTERM→SIGKILL grace period
	admitTimeout     time.Duration // gate admission backstop
}

// tunablesFromConfig converts the seconds/count fields of cfg into resolved
// durations. cfg fields are already clamped to positive defaults by config
// loading; the conversion is a straight multiply.
func tunablesFromConfig(cfg *config.Config) tunables {
	return tunables{
		handshake:        time.Duration(cfg.HandshakeTimeoutSeconds) * time.Second,
		healthInterval:   time.Duration(cfg.HealthCheckIntervalSeconds) * time.Second,
		healthFailThresh: cfg.HealthFailThreshold,
		hookTimeout:      time.Duration(cfg.HookTimeoutSeconds) * time.Second,
		supMaxRestarts:   cfg.SupervisorMaxRestarts,
		supRestartWindow: time.Duration(cfg.SupervisorRestartWindowSeconds) * time.Second,
		supStopTimeout:   time.Duration(cfg.SupervisorStopTimeoutSeconds) * time.Second,
		admitTimeout:     time.Duration(cfg.AdmitTimeoutSeconds) * time.Second,
	}
}

// Runtime holds the live state produced by Run. The REPL face uses it today;
// any future face receives the same Runtime and uses it for its own loop.
type Runtime struct {
	// Store is the modules store: the single source of truth for every module's
	// config, identity (wire), and supervision (supervisor/monitor/client)
	// facets. Activation mutates records in place.
	Store *module.Store
	// Messages is core's messages store: the recorded work, keyed by request id.
	// The broker is its sole runtime writer; job state is derived from it
	// (Messages.Job / Messages.Jobs), never folded. It is also the queue: queued
	// and running jobs are derived facts the broker's queue manager reconciles, and
	// the Coordinator's deactivation check reads its running set through gate.Busy.
	Messages *messages.Store
	// Broker is the running cross-module call router and queue manager.
	Broker *broker.Broker
	// Surface is the storeless surface orchestrator: the face's single door for
	// loading/refreshing a surface. It is the initiator that mints the
	// originating id+job_token and calls the broker, which originates nothing.
	Surface *surface.Orchestrator
	// HealthMaxRestarts is how many times the orchestrator restarts an
	// unhealthy core-managed module before marking it failed.
	HealthMaxRestarts int
	// tune holds the core-internal timeouts/limits resolved from suctl.conf.
	tune tunables
	// healthMu guards healthRestarts.
	healthMu sync.Mutex
	// healthRestarts counts restart attempts in the current unhealthy episode,
	// keyed by module short name. Reset on recovery and on teardown.
	healthRestarts map[string]int
	// Warns are non-fatal warnings accumulated during startup.
	Warns []string
}

// Run executes phases 3b through end-of-startup:
//  1. Start broker.
//  2. Visit every discovered module in deterministic (sorted) order.
//     For each module: skip if unavailable/missing; activate if it was
//     previously activated (or is the system module); otherwise leave ready.
//
// st holds both stores, already built by stores.Build (phases 1–3a for the
// modules store: Scan + MarkMissing + EvaluateRequirements +
// EvaluateConfigRequirements, plus the empty messages store). Run is the point
// where each store's single runtime writer takes over: the supervisor over
// modules, the broker over messages.
// activatedNames is the persisted activation list from activation.List(),
// already read by the caller — Run does not make a second read.
// cfg supplies the core-internal timeouts/limits (Gate D); a nil cfg uses
// compiled-in defaults, and HealthMaxRestarts caps the orchestrator's
// per-episode health restart attempts before a module is marked failed.
//
// A CORE BIST failure returns an error (if core is not adhering, we stop) —
// the caller owns process exit, since it may be running under a TUI that must
// be torn down first.
func Run(st *stores.Stores, activatedNames []string, cfg *config.Config) (*Runtime, error) {
	if cfg == nil {
		cfg = config.Default()
	}
	store := st.Modules
	healthMaxRestarts := cfg.HealthMaxRestarts
	if healthMaxRestarts <= 0 {
		healthMaxRestarts = config.DefaultHealthMaxRestarts
	}
	rt := &Runtime{
		Store:             store,
		Messages:          st.Messages,
		HealthMaxRestarts: healthMaxRestarts,
		tune:              tunablesFromConfig(cfg),
		healthRestarts:    make(map[string]int),
		Warns:             nil,
	}

	// Build the broker — it owns no listening socket. Modules reach it over
	// their inherited wire; the in-process face calls it directly. It resolves
	// routing from the store and admission from the messages store (the queue), so
	// it must exist before any module handshake for cross-module capability calls
	// to work the moment a module activates.
	rt.Broker = broker.New(store, rt.Messages, rt.tune.admitTimeout)

	// The surface orchestrator sits in front of the broker as the in-process
	// face's initiator: it mints the originating id+job_token and hands the
	// broker a complete envelope. Storeless — it caches nothing.
	rt.Surface = surface.New(rt.Broker)

	// Close the inbound loop: register the orchestrator as the broker's surface
	// sink so a job_update on a face-originated job is routed back to the face
	// (record-then-route). Wired here, before any module activates, so no wire
	// read loop races the registration.
	rt.Broker.RegisterSurfaceSink(rt.Surface.Deliver)

	// Register in-process system capabilities as a virtual record in the store.
	// The activate/deactivate callbacks let those handlers drive a single
	// module's lifecycle directly after persisting intent to disk — the record's
	// State is updated in place, with no whole-store rescan poll. The busy-check
	// closure is the pure ops-gate: a module is busy when a running job's footprint
	// covers it (gate.Busy over the messages store's running set).
	system.Register(store, paths.ModuleStateDir, rt.Messages, rt.ActivateModule, rt.DeactivateModule,
		func(shortName string) (string, bool) {
			return gate.Busy(shortName, rt.Messages.Running(), store)
		})

	// ─────────────────────────────────────────────────────────────────────────
	// CORE BIST — self-test system capabilities before loading modules.
	// If core is not adhering, we stop.
	// ─────────────────────────────────────────────────────────────────────────
	if rep := conformance.ProbeCore(rt.Broker.ProbeInvoker(), system.Manifest(), system.AllSurfaces(), 2*time.Second); !rep.Passed {
		return nil, fmt.Errorf("CORE BIST failed: %s", rep.FailReason())
	}

	// Build the activation set from the persisted list. system is never in the
	// set — Run handles it through the in-process system.Register call above and
	// never iterates it here.
	activatedSet := activatedSetFromList(activatedNames)

	// Rebuild the pending capability surface from the current store.
	// Used by on-requirement-missing re-checks to evaluate capability requirements.
	pendingSurface := module.PendingSurface(store)

	// on-requirement-missing pre-pass — for each previously-activated module that
	// is currently StateUnavailable: if it declares the hook and requirements are
	// the cause, fire the hook for each failing requirement. On hook exit 0,
	// re-check that requirement. If requirements are eventually satisfied, reset
	// state to StateReady so the main activation loop below can proceed with it.
	for _, shortName := range module.SortedKeys(store) {
		if !activatedSet[shortName] {
			continue
		}
		entry, ok := store.Get(shortName)
		if !ok || entry.State() != module.StateUnavailable || entry.Manifest == nil {
			continue
		}
		hookDecl, hasHook := entry.Manifest.Hooks["on-requirement-missing"]
		if !hasHook {
			continue
		}
		runner := hooks.New(shortName, entry.Dir, paths.ModuleConfDir, rt.tune.hookTimeout)
		rt.runRequirementMissingHooks(shortName, entry, hookDecl, runner, pendingSurface)
	}

	// Phase 3b — iterate ALL discovered modules in deterministic sorted order.
	// Every module in the index is evaluated; modules not in the activation set
	// are left at StateReady (not activated).
	for _, shortName := range module.SortedKeys(store) {
		entry, ok := store.Get(shortName)
		if !ok {
			continue
		}

		switch entry.State() {
		case module.StateMissing:
			if activatedSet[shortName] {
				rt.warn(fmt.Sprintf("module %q is missing from disk; skipping activation", shortName))
			}
			continue
		case module.StateUnavailable:
			if activatedSet[shortName] {
				rt.warn(fmt.Sprintf("module %q is unavailable (%s); skipping activation", shortName, entry.Reason()))
			}
			continue
		case module.StateActive:
			continue // idempotent
		}

		if !activatedSet[shortName] {
			// Discovered and requirements-met, but operator has not activated it.
			// Leave at StateReady — nothing to do.
			continue
		}

		// Boot restart of an already-persisted module. on-start is the normal
		// per-boot reconciliation path: the one-time setup (drop-ins, external
		// service wiring) was done at first activation. But if the installed
		// content changed since this module was last activated — an effective
		// upgrade via reinstall — the new version's one-time setup must run, so
		// select the pre-activate arm instead, before the module goes live. The
		// drift verdict is read from reality every boot, never persisted (D72).
		if rt.driftedSinceActivation(shortName, entry) {
			rt.warn(fmt.Sprintf("module %q installed content changed since activation; running upgrade (pre-activate)", shortName))
			rt.lc().Activate(shortName, entry)
		} else {
			rt.lc().BootActivate(shortName, entry)
		}
	}
	return rt, nil
}

// activatedSetFromList materialises the persisted activation list as a set
// keyed by module short name.
func activatedSetFromList(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, name := range names {
		set[name] = true
	}
	return set
}

// warn records a non-fatal startup warning.
func (rt *Runtime) warn(msg string) {
	slog.Warn(msg)
	rt.Warns = append(rt.Warns, msg)
}
