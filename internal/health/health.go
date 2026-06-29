// SPDX-License-Identifier: Apache-2.0

// Package health implements the continuous per-module health monitor.
//
// The monitor sends a health command to the module socket every CheckInterval.
// Every FailThreshold consecutive failures trigger onFail — and trigger it
// again on each further streak while the module stays unhealthy, so the
// lifecycle orchestrator can drive its restart escalation. A successful
// check after a failed streak triggers onRecover once. Callers supply these
// callbacks — the monitor does not reach into the module index directly.
package health

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/solutionsunity/suctl/sdk/protocol"
)

// Checker is the health-probe surface the monitor needs; the module's live
// broker wire mux satisfies it. The monitor reads it through a provider (see
// New) rather than capturing a value, so it always probes the current wire even
// after the module restarts and its wire rebinds.
type Checker interface {
	HealthWithTimeout(timeout time.Duration) (*protocol.HealthResult, error)
}

const (
	// DefaultCheckInterval is how often the health command is sent to each
	// module when New is given a non-positive interval. Core overrides this
	// from suctl.conf (Gate D).
	DefaultCheckInterval = 30 * time.Second

	// DefaultFailThreshold is the consecutive-failure count before onFail fires
	// when New is given a non-positive threshold.
	DefaultFailThreshold = 3
)

// Monitor continuously health-checks one active module process.
type Monitor struct {
	shortName     string
	get           func() Checker // resolves the live wire each check (nil = no wire)
	interval      time.Duration  // health-check period
	failThreshold int            // consecutive failures before onFail fires
	onFail        func()         // called when consecutive failures reach failThreshold
	onRecover     func()         // called when health recovers after failures

	mu        sync.Mutex
	failures  int
	failFired bool // true between onFail and onRecover to avoid repeat calls
	stopCh    chan struct{}
	done      chan struct{}
}

// New returns a Monitor for the given module. get resolves the module's live
// broker wire at each check; it returns nil while the module holds no wire, and
// the monitor counts that as a failed check. interval is the health-check period
// and failThreshold the consecutive-failure count that fires onFail; a
// non-positive value of either falls back to the matching Default. onFail and
// onRecover are called from the monitor goroutine — they must be safe for
// concurrent use and must not block indefinitely.
func New(shortName string, get func() Checker, interval time.Duration, failThreshold int, onFail, onRecover func()) *Monitor {
	if interval <= 0 {
		interval = DefaultCheckInterval
	}
	if failThreshold <= 0 {
		failThreshold = DefaultFailThreshold
	}
	return &Monitor{
		shortName:     shortName,
		get:           get,
		interval:      interval,
		failThreshold: failThreshold,
		onFail:        onFail,
		onRecover:     onRecover,
		stopCh:        make(chan struct{}),
		done:          make(chan struct{}),
	}
}

// Start launches the health check loop in a background goroutine.
// Call Stop to shut it down cleanly.
func (m *Monitor) Start() {
	go m.loop()
}

// Stop signals the monitor to stop and waits for the goroutine to exit.
func (m *Monitor) Stop() {
	select {
	case <-m.stopCh:
	default:
		close(m.stopCh)
	}
	<-m.done
}

// loop runs the periodic health check until Stop is called.
func (m *Monitor) loop() {
	defer close(m.done)
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.check()
		}
	}
}

// check sends one health command and updates failure state. A module with no
// live wire (provider returns nil) is treated as a failed check — the same as a
// health-command error — so a module whose wire has dropped escalates normally.
func (m *Monitor) check() {
	var err error
	if c := m.get(); c != nil {
		_, err = c.HealthWithTimeout(m.interval)
	} else {
		err = fmt.Errorf("module wire not bound")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if err != nil {
		m.failures++
		slog.Warn("module health check failed",
			"module", m.shortName,
			"consecutive_failures", m.failures,
			"error", err)
		// Fire onFail at every FailThreshold streak. Resetting the counter
		// here re-arms detection so a module that stays unhealthy across a
		// restart attempt fires onFail again, letting the orchestrator count
		// attempts and eventually mark it failed.
		if m.failures >= m.failThreshold {
			m.failures = 0
			m.failFired = true
			if m.onFail != nil {
				go m.onFail()
			}
		}
		return
	}

	// Success — clear any partial streak and signal recovery once.
	m.failures = 0
	if m.failFired {
		slog.Info("module health recovered", "module", m.shortName)
		m.failFired = false
		if m.onRecover != nil {
			go m.onRecover()
		}
	}
}
