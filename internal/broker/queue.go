// SPDX-License-Identifier: Apache-2.0

// Package broker — queue.go is the queue manager: applied admission policy over
// the recorded messages. The messages store is the queue (queued/running are
// derived facts, not a separate structure); this file advances jobs from queued
// to running by stamping the Started fact, then wakes the parked callers.
//
// It splits the pure decision (planPromotions) from the effecting reconcile
// (advance). planPromotions is the full queue logic and is unit-testable without
// sockets; advance is the only writer of the Started fact and the only signaller
// of the wakeup. Both run only inside the broker — the store stays a passive
// truth container and the gate stays a pure predicate.
package broker

import (
	"time"

	"github.com/solutionsunity/suctl/internal/gate"
	"github.com/solutionsunity/suctl/internal/messages"
	"github.com/solutionsunity/suctl/internal/module"
)

// footprint returns the reservation set for a job rooted at moduleName, read on
// demand from the modules store. module.Footprint always includes the root.
func (b *Broker) footprint(moduleName string) map[string]bool {
	return module.Footprint(moduleName, b.store)
}

// planPromotions is the pure queue policy: given the FIFO-ordered queued jobs and
// the currently running jobs, it returns the tokens that may start now. busy
// folds the running footprints; a queued job is promoted when the gate finds its
// footprint disjoint from busy, and its footprint then joins busy so a later
// disjoint job can pass a blocked one (head-of-line non-blocking) while a job
// sharing a footprint waits its turn. Pure: no store writes, no side effects.
func planPromotions(queued, running []messages.Job, fp func(string) map[string]bool) []string {
	busy := map[string]bool{}
	for _, r := range running {
		for m := range fp(r.Module) {
			busy[m] = true
		}
	}
	var promote []string
	for _, q := range queued {
		f := fp(q.Module)
		if gate.Admissible(f, busy) {
			promote = append(promote, q.Token)
			for m := range f {
				busy[m] = true
			}
		}
	}
	return promote
}

// advance is the queue manager's reconcile step. Under the admission lock it
// promotes every queued job the policy admits — stamping the Started fact, which
// flips the job from queued to running in the store — then broadcasts so every
// parked caller re-checks whether its own job is now running. It is called on the
// two queue-changing events: a new originating invoke entering the store, and a
// job becoming terminal (which frees its footprint). It never runs a handler and
// never blocks: routing happens afterwards, outside the lock, on the woken
// caller's goroutine.
func (b *Broker) advance() {
	b.admitMu.Lock()
	for _, tok := range planPromotions(b.msgs.Queued(), b.msgs.Running(), b.footprint) {
		b.msgs.Start(tok)
	}
	b.cond.Broadcast()
	b.admitMu.Unlock()
}

// waitStarted parks the calling goroutine until token's Started fact is stamped
// (the queue manager promoted it to running) or admitTimeout elapses. A timer
// broadcasts at the deadline so the waiter wakes to re-check even when no
// promotion happens (its footprint never frees). On timeout it returns an error;
// the caller records the job failed so an unsatisfiable queue entry is observably
// failed, never a silent hang.
func (b *Broker) waitStarted(token string) error {
	b.admitMu.Lock()
	defer b.admitMu.Unlock()

	deadline := time.Now().Add(b.admitTimeout)
	timer := time.AfterFunc(b.admitTimeout, func() {
		b.admitMu.Lock()
		b.cond.Broadcast()
		b.admitMu.Unlock()
	})
	defer timer.Stop()

	for {
		if _, started := b.msgs.StartedAt(token); started {
			return nil
		}
		if !time.Now().Before(deadline) {
			return &admitTimeoutError{timeout: b.admitTimeout}
		}
		b.cond.Wait()
	}
}

// admitTimeoutError reports that a job sat queued past the admission backstop.
type admitTimeoutError struct{ timeout time.Duration }

func (e *admitTimeoutError) Error() string {
	return "admission timed out after " + e.timeout.String()
}
