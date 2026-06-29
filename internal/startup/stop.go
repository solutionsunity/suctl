// SPDX-License-Identifier: Apache-2.0

package startup

import (
	"github.com/solutionsunity/suctl/internal/hooks"
	"github.com/solutionsunity/suctl/sdk/paths"
)

// Stop is the public entry to the graceful shutdown sequence. It delegates to
// the lifecycle coordinator.
func (rt *Runtime) Stop() { rt.lc().Shutdown() }

// Shutdown is the graceful core-stop sequence:
//
//  1. stop all health monitors  — no health checks during shutdown
//  2. For each active module: SIGTERM the process, then fire on-stop (async,
//     exec form only — the process is exiting). on-stop is symmetric with
//     on-start: it runs at every suctl shutdown while the module is activated,
//     as the counterpart to on-start which runs at every suctl boot.
//
// Service-attached external services (e.g. suctl-odoo-service under Odoo) keep
// running; only suctl-managed module processes are stopped here.
// The broker owns no listening socket: each module wire is served by a goroutine
// that returns when the process exits and the wire closes.
func (c *Coordinator) Shutdown() {
	rt := c.rt

	// 1. stop every health monitor first so no check fires mid-shutdown.
	for _, name := range rt.Store.Names() {
		if r, ok := rt.Store.Get(name); ok {
			if mon := r.TakeMonitor(); mon != nil {
				mon.Stop()
			}
		}
	}

	// 2. SIGTERM every supervised process, then fire on-stop.
	for _, name := range rt.Store.Names() {
		if r, ok := rt.Store.Get(name); ok {
			if sup := r.TakeSupervisor(); sup != nil {
				sup.Stop() // sends SIGTERM; supervisor waits for process exit
				// on-stop: non-blocking; process is gone, only exec form is
				// meaningful. Symmetric with on-start at boot.
				runner := hooks.New(name, r.Dir, paths.ModuleConfDir, rt.tune.hookTimeout)
				c.fireAsync(runner, r, "on-stop")
			}
		}
	}
}
