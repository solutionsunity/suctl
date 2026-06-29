// SPDX-License-Identifier: Apache-2.0

package startup

import (
	"fmt"
	"log/slog"

	"github.com/solutionsunity/suctl/internal/gate"
	"github.com/solutionsunity/suctl/internal/hooks"
	"github.com/solutionsunity/suctl/internal/module"
	"github.com/solutionsunity/suctl/sdk/paths"
)

// Deactivate gracefully stops a running module and resets its state to
// StateReady. It mirrors Activate in reverse, and matches failHealth's order so
// the module is not-available before drain: ops-check → monitor
// stop → surface deregister → identity drop → pre-deactivate (drain) →
// supervisor stop → wire drop → footprint unregister → state reset →
// post-deactivate.
//
// It returns an ops error and tears nothing down when the ops-gate reports the
// module busy — a running job's footprint covers it (its own module or a module
// it can reach). The footprint frees on its own when the job becomes terminal;
// the operator retries then.
func (c *Coordinator) Deactivate(shortName string, entry *module.Record) error {
	rt := c.rt
	runner := hooks.New(shortName, entry.Dir, paths.ModuleConfDir, rt.tune.hookTimeout)

	// Ops-gate: refuse to tear down a module a running job still covers. Checked
	// before any deregister/stop so a busy module is left fully intact. Busy is
	// derived from the messages store's running set through the pure gate policy —
	// the same policy the queue manager admits with.
	if rt.Messages != nil {
		if token, busy := gate.Busy(shortName, rt.Messages.Running(), rt.Store); busy {
			return fmt.Errorf("module %q is busy (job %s holds its footprint); deactivation refused", shortName, token)
		}
	}

	// Deregister first (monitor, surface, identity) so the broker routes no new
	// traffic into a module that is draining. The process and its client stay
	// alive for the drain hook below.
	c.deregister(shortName, entry)

	// pre-deactivate (blocking): the drain signal, delivered over the still-live
	// broker wire. suctl proceeds after the declared timeout — the system is not
	// held hostage by a misbehaving hook.
	if err := c.fire(runner, entry, "pre-deactivate"); err != nil {
		slog.Warn("pre-deactivate hook warning during deactivation", "module", shortName, "error", err)
	}

	// Drain complete — stop the process, drop the wire, clear the counter.
	// The gate reads each footprint on demand from the store and the broker
	// reads each requires set from the store, so there is nothing to unregister
	// here: dropping out of StateActive (below) is what removes the module from
	// both. The ops-check above guarantees no reservation is stranded.
	c.stopProcess(shortName, entry)

	// Reset to StateReady so the module can be re-activated later without restart.
	entry.SetStatus(module.StateReady, "")

	// post-deactivate (non-blocking): the process is gone, so only the exec form
	// is meaningful — the chokepoint receives no wire.
	c.fireAsync(runner, entry, "post-deactivate")

	slog.Info("module deactivated", "module", shortName)
	return nil
}

// DeactivateModule hot-unloads a single module by short name — the direct path
// the system.module.deactivate handler takes after dropping the activation flag,
// replacing the former whole-store Rescan poll. A StateFailed module is recovered
// to StateReady: dropping the flag clears the failed verdict so the
// operator can re-activate. A busy module refuses teardown; the refusal
// surfaces as a warning the home page can show, and the operator retries once the
// job frees it. Unknown, inert, and not-active modules are no-ops.
func (rt *Runtime) DeactivateModule(shortName string) {
	entry, ok := rt.Store.Get(shortName)
	if !ok || entry.IsInert() {
		return
	}
	if entry.State() == module.StateFailed {
		entry.SetStatus(module.StateReady, "")
		return
	}
	if entry.State() != module.StateActive {
		return
	}
	if err := rt.lc().Deactivate(shortName, entry); err != nil {
		rt.warn(err.Error())
	}
}
