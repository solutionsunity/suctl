// SPDX-License-Identifier: Apache-2.0

// Package gate is core's central ops-policy authority: pure admission policy over
// module footprints. It holds no state and reserves nothing — the queue manager
// (broker) derives the running set from the messages store and each footprint
// from the modules store, then consults these predicates to decide what may run.
// Per-module serialization is emergent: every footprint includes its own module,
// so two jobs touching one module can never both be admitted. One policy, two
// callers — the queue manager (Admissible) and the lifecycle ops-gate (Busy).
package gate

import (
	"github.com/solutionsunity/suctl/internal/messages"
	"github.com/solutionsunity/suctl/internal/module"
)

// Admissible reports whether a job with the given footprint may start now: none
// of its modules appear in busy, the union of the running jobs' footprints. An
// empty footprint, or an empty busy set, is admissible. This is the queue
// manager's per-candidate predicate; the caller folds busy and adds each
// promotion's footprint to it so a later disjoint job can pass a blocked one.
func Admissible(footprint, busy map[string]bool) bool {
	for m := range footprint {
		if busy[m] {
			return false
		}
	}
	return true
}

// Busy reports the job_token of the running job whose footprint currently covers
// moduleName, if any — the lifecycle ops-gate's deactivation check. A module is
// held exactly when it falls inside some running job's footprint, read on demand
// from the modules store. Sharing this with Admissible keeps one policy: a module
// is deactivatable precisely when no running footprint covers it.
func Busy(moduleName string, running []messages.Job, store *module.Store) (token string, busy bool) {
	for _, j := range running {
		if module.Footprint(j.Module, store)[moduleName] {
			return j.Token, true
		}
	}
	return "", false
}
