// SPDX-License-Identifier: Apache-2.0

package startup

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/solutionsunity/suctl/internal/activation"
	"github.com/solutionsunity/suctl/internal/health"
	"github.com/solutionsunity/suctl/internal/hooks"
	"github.com/solutionsunity/suctl/internal/module"
	"github.com/solutionsunity/suctl/internal/supervisor"
	sdkconf "github.com/solutionsunity/suctl/sdk/conformance"
	"github.com/solutionsunity/suctl/sdk/paths"
)

// Activate runs the full activation sequence for a true first activation or
// explicit re-activation (after the operator deactivated the module).
// Fires pre-activate, then the rest of the activation sequence.
// For boot restart of already-persisted modules, use BootActivate.
func (c *Coordinator) Activate(shortName string, entry *module.Record) {
	c.activateWith(shortName, entry, "pre-activate")
}

// BootActivate runs the activation sequence for a module that is already in the
// persisted activation list when suctl starts. Fires on-start instead of
// pre-activate: the module's one-time setup was done at first activation;
// on-start handles per-boot reconciliation (e.g. recreating a /run/ directory
// cleared on reboot) without re-running destructive first-time steps.
func (c *Coordinator) BootActivate(shortName string, entry *module.Record) {
	c.activateWith(shortName, entry, "on-start")
}

// activateWith is the shared activation body. setupHook is the lifecycle event
// fired before the process starts — "pre-activate" for first/re-activation,
// "on-start" for boot restart of an already-persisted module.
//
// Ordered steps:
//  1. setupHook (blocking; failure aborts).
//  2. startProcess — start the supervisor, which creates the module's broker
//     wire (socketpair) and binds core's end to the record as a wire mux.
//  3. handshake — a single bounded round trip over that wire validates the
//     returned manifest (the wire is connected from spawn).
//  4. BIST (protocol conformance) — strict: failure aborts.
//  5. store the supervisor on the record for rollback (the mux is already bound
//     by OnChannel). The record is NOT yet StateActive, so the store resolves no
//     route to it — there is no born-but-reachable window.
//  6. post-activate (blocking; failure rolls back through Deactivate).
//     Hooks reach the module over its broker wire, not a route, so the module
//     need not be StateActive yet.
//  7. flip the record to StateActive — store-resolved routing makes a
//     fully-ready module reachable only here.
//  8. start the health monitor.
func (c *Coordinator) activateWith(shortName string, entry *module.Record, setupHook string) {
	rt := c.rt
	runner := hooks.New(shortName, entry.Dir, paths.ModuleConfDir, rt.tune.hookTimeout)

	if err := c.fire(runner, entry, setupHook); err != nil {
		c.failActivation(entry, shortName, runner, setupHook+" hook failed: "+err.Error())
		return
	}

	// Start the module process. The supervisor creates the module's address-less
	// broker wire per launch and, via OnChannel, binds core's end to the record
	// as a wire mux — possession of the child end is the module's identity
	// possession of the child end is the module's identity. By the time
	// Start returns, the record holds the live mux.
	sup, err := c.startProcess(entry, shortName, runner)
	if err != nil {
		c.failActivation(entry, shortName, runner, err.Error())
		return
	}

	// The mux is bound synchronously by OnChannel during Start; a nil here means
	// the launch never delivered core's wire end.
	mux := entry.GetMux()
	if mux == nil {
		c.failActivation(entry, shortName, runner, "broker wire not bound after launch")
		return
	}

	// Handshake: a single round trip over the inherited broker wire, bounded by
	// the configured timeout. Identity is not derived here — the module is
	// identified by possession of that wire, bound at spawn.
	if err := handshake(mux, entry.Manifest, rt.tune.handshake); err != nil {
		c.failActivation(entry, shortName, runner, err.Error())
		return
	}

	// BIST: protocol conformance checks driven over the same broker wire. Strict
	// — a module that fails the built-in self test is not activated.
	bistReport := sdkconf.ProbeModule(mux, entry.Manifest, entry.Surfaces, sdkconf.Options{
		ModuleDir: entry.Dir,
	})
	if !bistReport.Passed {
		c.failActivation(entry, shortName, runner, bistReport.FailReason())
		return
	}

	// Store the supervisor before post-activate so a failed hook can be rolled
	// back through Deactivate (which drains over the broker wire and stops the
	// supervised process). The module is NOT yet StateActive — it stays
	// unreachable until post-activate succeeds, so there is no born-but-reachable
	// window. The wire mux is already bound by OnChannel.
	if sup != nil {
		entry.SetSupervisor(sup)
	}

	// post-activate (blocking). Hooks reach the module over its broker wire, not
	// a route, so the module need not be StateActive yet. Failure aborts
	// and rolls back through the deactivation path, then marks the module
	// unavailable.
	if err := c.fire(runner, entry, "post-activate"); err != nil {
		rt.warn(fmt.Sprintf("module %q post-activate hook failed: %v — rolling back", shortName, err))
		// Forced rollback: the module never reached the surface and its footprint
		// is not yet registered, so the gate holds no reservation and Deactivate
		// cannot refuse — the error is structurally nil here.
		_ = c.Deactivate(shortName, entry)
		entry.SetStatus(module.StateUnavailable, "post-activate hook failed: "+err.Error())
		c.fireAsync(runner, entry, "on-activate-fail")
		return
	}

	// post-activate succeeded — make the module reachable. Routing, footprint,
	// and the requires-gate are all resolved from the modules store on demand
	// (module.Resolve / module.Footprint / store.Allows), each keyed on
	// StateActive, so flipping the state is the single act that makes the module
	// reachable — there is no separate surface, gate, or requires registration.
	entry.SetStatus(module.StateActive, "")

	// Health monitor: started only after post-activate succeeds so the two
	// never run concurrently on the same module. The provider resolves the
	// record's live broker wire at each check, so the monitor follows the wire
	// across restarts (and counts a dropped wire as a failed check). The
	// orchestrator owns the health-failure escalation and recovery (restart up to
	// HealthMaxRestarts, then mark failed); both callbacks route through the hook
	// chokepoint.
	monitor := health.New(shortName, func() health.Checker {
		if mx := entry.GetMux(); mx != nil {
			return mx
		}
		return nil
	}, rt.tune.healthInterval, rt.tune.healthFailThresh,
		func() { c.onHealthFail(runner, entry, shortName) },
		func() { c.onHealthRecover(runner, entry, shortName) },
	)
	monitor.Start()
	entry.SetMonitor(monitor)

	// Record the checksum of the installed content that was just activated. This
	// is the single write point for every successful path — first activation,
	// boot restart, and upgrade — so the next boot compares against exactly what
	// is now live (D72). Non-fatal: the module is up; at worst the next boot
	// re-runs the pre-activate arm.
	if cs, err := activation.Checksum(entry.Dir); err == nil {
		if err := activation.SetChecksum(paths.ModuleStateDir, shortName, cs); err != nil {
			rt.warn(fmt.Sprintf("module %q: cannot record activation checksum: %v", shortName, err))
		}
	} else {
		rt.warn(fmt.Sprintf("module %q: cannot compute installed checksum: %v", shortName, err))
	}

	slog.Info("module activated", "module", shortName)
}

// driftedSinceActivation reports whether a module's installed content changed
// since it was last successfully activated — the upgrade signal (D72). It
// compares the checksum stored in the activation flag against a fresh checksum
// of the module directory. An empty stored checksum (legacy flag, or never
// recorded) is not drift: history is unknown, so boot treats it as a normal
// restart and the checksum is backfilled on success. Any read/compute error is
// warned and treated as no drift — a module that activated before still boots
// via the on-start arm.
func (rt *Runtime) driftedSinceActivation(shortName string, entry *module.Record) bool {
	stored, err := activation.GetChecksum(paths.ModuleStateDir, shortName)
	if err != nil {
		rt.warn(fmt.Sprintf("module %q: cannot read activation checksum: %v", shortName, err))
		return false
	}
	if stored == "" {
		return false
	}
	current, err := activation.Checksum(entry.Dir)
	if err != nil {
		rt.warn(fmt.Sprintf("module %q: cannot compute installed checksum: %v", shortName, err))
		return false
	}
	return stored != current
}

// ActivateModule hot-loads a single module by short name — the direct path the
// system.module.activate handler takes after persisting intent to disk,
// replacing the former whole-store Rescan poll. It re-evaluates requirements
// against the live system (a required binary, path, socket, config key, or
// capability may have appeared or vanished since boot) and, when satisfied, runs
// the lifecycle activation. StateFailed is terminal here: a module whose
// health-restart budget was exhausted is not re-activated — recovery is via
// DeactivateModule. Unknown, inert, and already-active modules are no-ops.
func (rt *Runtime) ActivateModule(shortName string) {
	entry, ok := rt.Store.Get(shortName)
	if !ok || entry.IsInert() {
		return
	}
	if entry.State() == module.StateActive || entry.State() == module.StateFailed {
		return
	}

	surface := module.ReadySurface(rt.Store)
	if missing := module.FirstMissingRequirement(entry, surface, paths.ModuleConfDir); missing != nil {
		reason := fmt.Sprintf("requires %s %q which is not available", missing.Type, missing.Value)
		entry.SetStatus(module.StateUnavailable, reason)
		rt.warn(fmt.Sprintf("module %q cannot activate: %s", shortName, reason))
		return
	}
	rt.lc().Activate(shortName, entry)
}

// failActivation marks a module unavailable, fires on-activate-fail (async),
// and records a warning.
func (c *Coordinator) failActivation(entry *module.Record, shortName string, runner *hooks.Runner, reason string) {
	entry.SetStatus(module.StateUnavailable, reason)
	c.rt.warn(fmt.Sprintf("module %q activation failed: %s", shortName, reason))
	c.fireAsync(runner, entry, "on-activate-fail")
}

// startProcess starts the module's supervisor, which creates the module's
// bidirectional broker wire (a socketpair) at launch and, via OnChannel, binds
// core's end to the record as a wire mux. Returns the supervisor so the caller
// can store it for graceful shutdown.
func (c *Coordinator) startProcess(entry *module.Record, shortName string, runner *hooks.Runner) (*supervisor.Supervisor, error) {
	m := entry.Manifest

	// Resolve entrypoint parts to absolute paths where possible.
	// For compiled binaries (single element like "suctl-mod-nginx"), the
	// binary lives in entry.Dir — resolve it there. For interpreted scripts
	// (["python3", "main.py"]), "python3" is an interpreter that stays in PATH
	// while "main.py" is resolved relative to entry.Dir.
	// Any element that already exists as a file inside entry.Dir is promoted
	// to its absolute path; otherwise it is left as-is for PATH lookup.
	resolvedEntry := make([]string, len(m.Entrypoint.Parts))
	copy(resolvedEntry, m.Entrypoint.Parts)
	for i, part := range resolvedEntry {
		if filepath.IsAbs(part) {
			continue // already absolute — nothing to do
		}
		candidate := filepath.Join(entry.Dir, part)
		if _, err := os.Stat(candidate); err == nil {
			resolvedEntry[i] = candidate
		}
		// else: leave as-is — PATH lookup (e.g. "python3", "node")
	}

	supCfg := supervisor.Config{
		ShortName:  shortName,
		Entrypoint: resolvedEntry,
		WorkDir:    entry.Dir,
		Env: []string{
			"SUCTL_MODULE=" + shortName,
			"SUCTL_MODULE_DIR=" + entry.Dir,
			"SUCTL_CONF_DIR=" + paths.ModuleConfDir,
		},
		// on-crash: non-blocking, fired when the process exits unexpectedly
		// before any restart attempt. Capability form is invalid here — the
		// process has just exited — so the chokepoint receives no wire.
		OnCrash: func() {
			c.fireAsync(runner, entry, "on-crash")
		},
		// on-channel: fired with core's end of the module's inherited broker
		// wire on every (re)launch. The broker wraps it in a duplex mux serving
		// inbound requests under shortName (possession = identity) and
		// the record binds that mux as the module's live transport. Re-fires with
		// a fresh conn on restart, rebinding a fresh mux.
		OnChannel: func(conn io.ReadWriteCloser) {
			entry.SetMux(c.rt.Broker.RegisterWire(shortName, conn))
		},
		// Crash-loop guard + shutdown grace, resolved from suctl.conf (Gate D).
		MaxRestarts:   c.rt.tune.supMaxRestarts,
		RestartWindow: c.rt.tune.supRestartWindow,
		StopTimeout:   c.rt.tune.supStopTimeout,
	}
	sup := supervisor.New(supCfg)
	if err := sup.Start(); err != nil {
		return nil, fmt.Errorf("start module process: %w", err)
	}
	return sup, nil
}
