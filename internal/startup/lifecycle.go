// SPDX-License-Identifier: Apache-2.0

package startup

import (
	"fmt"
	"log/slog"

	"github.com/solutionsunity/suctl/internal/hooks"
	"github.com/solutionsunity/suctl/internal/module"
	"github.com/solutionsunity/suctl/sdk/manifest"
	"github.com/solutionsunity/suctl/sdk/protocol"
)

// Coordinator is the single lifecycle orchestrator. It owns every module state
// transition — Ready → Activating → Active → Deactivating → {Ready |
// Unavailable | Failed} — and is the only component that fires module hooks.
// The transition→event→blocking policy lives here and nowhere else:
//
//	boot         : on-start (blocking, abort) → post-activate (blocking, abort+rollback)
//	first-activate: pre-activate (blocking, abort) → post-activate (blocking, abort+rollback)
//	activate-fail: on-activate-fail (async)
//	deactivate   : pre-deactivate (blocking, proceed-after-timeout) → post-deactivate (async)
//	runtime      : on-crash / on-health-fail / on-health-recover (async)
//	shutdown     : SIGTERM every spawned module → on-stop (async)
type Coordinator struct {
	rt *Runtime
}

// lc returns a Coordinator bound to rt. It carries no state beyond the Runtime
// pointer, so a fresh value per call is intentional and cheap.
func (rt *Runtime) lc() *Coordinator { return &Coordinator{rt: rt} }

// fire runs a blocking hook through the single hook chokepoint and returns its
// error. An undeclared hook is a no-op (nil error). Capability-form hooks reach
// the module over its live broker wire, resolved from the record here so a hook
// always uses the current wire (and gets a clean nil when none is bound, e.g.
// pre-activate or post-deactivate).
func (c *Coordinator) fire(r *hooks.Runner, entry *module.Record, event string) error {
	return r.RunHook(entry.Manifest, event, muxInvoker(entry))
}

// fireAsync fires a non-blocking hook through the single hook chokepoint.
func (c *Coordinator) fireAsync(r *hooks.Runner, entry *module.Record, event string) {
	r.RunHookAsync(entry.Manifest, event, muxInvoker(entry))
}

// muxInvoker returns the record's live broker wire as a protocol.Invoker, or a
// clean nil interface when the module holds no wire — so the hooks runner's
// nil-client check fires correctly instead of dispatching onto a typed nil.
func muxInvoker(entry *module.Record) protocol.Invoker {
	if mx := entry.GetMux(); mx != nil {
		return mx
	}
	return nil
}

// fireRequirementMissing fires on-requirement-missing for one failing
// requirement through the single hook chokepoint. Only the exec form is valid —
// the module process is not running yet.
func (c *Coordinator) fireRequirementMissing(r *hooks.Runner, decl *manifest.HookDecl, reqType, reqValue string) error {
	return r.RunRequirementMissing(decl, reqType, reqValue)
}

// onHealthFail is the runtime health-failure escalation. The health
// monitor calls it on every failed streak. The orchestrator fires the
// on-health-fail hook, then restarts the core-managed module up to
// HealthMaxRestarts times within one unhealthy episode; once that budget is
// exhausted it gives up and marks the module failed. The per-episode counter is
// reset by onHealthRecover (success) or teardown (deactivation/failure).
func (c *Coordinator) onHealthFail(runner *hooks.Runner, entry *module.Record, shortName string) {
	rt := c.rt
	// Ignore stale callbacks for a module that is no longer active (already
	// failed, deactivating, or rolled back).
	if entry.State() != module.StateActive {
		return
	}

	c.fireAsync(runner, entry, "on-health-fail")

	rt.healthMu.Lock()
	attempts := rt.healthRestarts[shortName]
	if attempts >= rt.HealthMaxRestarts {
		rt.healthMu.Unlock()
		c.failHealth(shortName, entry)
		return
	}
	rt.healthRestarts[shortName] = attempts + 1
	attempt := attempts + 1
	rt.healthMu.Unlock()

	sup := entry.Supervisor()
	if sup == nil {
		// An active module with no supervisor is a core invariant violation —
		// every module is spawned. It cannot be restarted, so attempts still
		// accrue toward the failed verdict.
		rt.warn(fmt.Sprintf("module %q health failing; no supervisor, cannot restart (attempt %d/%d)",
			shortName, attempt, rt.HealthMaxRestarts))
		return
	}
	rt.warn(fmt.Sprintf("module %q health failing; restarting (attempt %d/%d)",
		shortName, attempt, rt.HealthMaxRestarts))
	sup.Restart()
}

// onHealthRecover resets the health-restart budget and fires the
// on-health-recover hook through the chokepoint.
func (c *Coordinator) onHealthRecover(runner *hooks.Runner, entry *module.Record, shortName string) {
	rt := c.rt
	rt.healthMu.Lock()
	delete(rt.healthRestarts, shortName)
	rt.healthMu.Unlock()
	c.fireAsync(runner, entry, "on-health-recover")
	slog.Info("module health recovered", "module", shortName)
}

// failHealth gives up on a persistently unhealthy module: it tears down the
// live runtime artifacts and lands the module in StateFailed with a reason
// Distinct from Deactivate — no deactivation hooks fire and the module
// does not return to StateReady.
func (c *Coordinator) failHealth(shortName string, entry *module.Record) {
	rt := c.rt
	max := rt.HealthMaxRestarts
	c.teardown(shortName, entry)
	reason := fmt.Sprintf("health checks failing; %d restart attempts exhausted", max)
	entry.SetStatus(module.StateFailed, reason)
	rt.warn(fmt.Sprintf("module %q marked failed: %s", shortName, reason))
}

// teardown stops the live runtime artifacts for a module — health monitor,
// surface registration, supervisor process, client, and the health-restart
// counter. It fires no hooks and sets no final state; callers decide the
// resulting state (Deactivate → Ready, failHealth → Failed).
//
// It is the deregister-then-stop sequence in one call, used where the module is
// torn down atomically (failHealth). Deactivate splits the two around its drain
// hook: deregister first so no new traffic lands, drain on the
// still-live client, then stop the process.
func (c *Coordinator) teardown(shortName string, entry *module.Record) {
	c.deregister(shortName, entry)
	c.stopProcess(shortName, entry)
}

// deregister makes a module not-available without stopping its process: it stops
// the health monitor and flips the record off StateActive so the store resolves
// no new route into it. After this returns the broker routes no new traffic to
// the module, but the process and its client are still alive — so a drain hook
// can reach it over the direct client. The caller sets the final
// resting state (Deactivate → Ready, failHealth → Failed).
func (c *Coordinator) deregister(shortName string, entry *module.Record) {
	if mon := entry.TakeMonitor(); mon != nil {
		mon.Stop()
	}
	// Routing resolves from the store keyed on StateActive (module.Resolve), so
	// dropping out of StateActive is what removes the module from the surface.
	entry.SetStatus(module.StateReady, "")
}

// stopProcess stops the supervised process, drops the client, and clears the
// health-restart counter. Call after deregister so no traffic is in flight when
// the process goes away.
func (c *Coordinator) stopProcess(shortName string, entry *module.Record) {
	rt := c.rt

	if sup := entry.TakeSupervisor(); sup != nil {
		sup.Stop()
	}

	// The supervisor closed core's end of the wire on Stop; drop the now-dead
	// mux so the store resolves no transport into a stopped module.
	entry.SetMux(nil)

	rt.healthMu.Lock()
	delete(rt.healthRestarts, shortName)
	rt.healthMu.Unlock()
}
