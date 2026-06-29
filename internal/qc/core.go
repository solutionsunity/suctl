// SPDX-License-Identifier: Apache-2.0

package qc

import (
	"bufio"
	"os"
	"os/exec"
	"strings"

	"github.com/solutionsunity/suctl/internal/system"
)

func runCore() {
	var checks []Check

	checks = append(checks, checkCQ01())
	checks = append(checks, checkCQ02())
	checks = append(checks, checkCQ03())
	checks = append(checks, checkCQ04())
	checks = append(checks, checkCQ05())
	checks = append(checks, checkCQ06())
	checks = append(checks, checkCQ07())
	checks = append(checks, checkCQ08())
	checks = append(checks, checkCQ09())
	checks = append(checks, checkCQ11())
	checks = append(checks, checkCQ12())
	checks = append(checks, checkCQ14())
	checks = append(checks, checkCQ15())
	checks = append(checks, checkCQ16())
	checks = append(checks, checkCQ20())
	checks = append(checks, checkCQ21())
	checks = append(checks, checkCQ17())
	checks = append(checks, checkCQ18())

	Report(checks)
}

func checkCQ01() Check {
	f, err := os.Open("cmd/main.go")
	if err != nil {
		return Check{ID: "CQ01", Name: "Bootstrap sequence order", Result: Blocked, Message: err.Error()}
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.Contains(line, "bootstrap.Run()") {
			lines = append(lines, "bootstrap")
		} else if strings.Contains(line, "config.Load()") {
			lines = append(lines, "config")
		} else if strings.Contains(line, "logging.Init(") {
			lines = append(lines, "logging")
		} else if strings.Contains(line, "probe.Run()") {
			lines = append(lines, "probe")
		} else if strings.Contains(line, "module.Scan(") {
			lines = append(lines, "scan")
		}
	}

	expected := []string{"bootstrap", "config", "logging", "probe", "scan"}
	if len(lines) < len(expected) {
		return Check{ID: "CQ01", Name: "Bootstrap sequence order", Result: Blocked, Message: "missing phases"}
	}
	for i, name := range expected {
		if lines[i] != name {
			return Check{ID: "CQ01", Name: "Bootstrap sequence order", Result: Blocked, Message: "wrong order"}
		}
	}
	return Check{ID: "CQ01", Name: "Bootstrap sequence order", Result: Pass}
}

func checkCQ03() Check {
	b, _ := os.ReadFile("internal/module/scan.go")
	if strings.Contains(string(b), "existing.SetStatus(StateUnavailable") && strings.Contains(string(b), "ce := &ConflictError") {
		return Check{ID: "CQ03", Name: "Non-fatal conflicts", Result: Pass}
	}
	return Check{ID: "CQ03", Name: "Non-fatal conflicts", Result: Blocked, Message: "scan.go does not mark conflicts as StateUnavailable"}
}

func checkCQ04() Check {
	b, _ := os.ReadFile("cmd/main.go")
	s := string(b)
	i := strings.Index(s, "module.MarkMissing(store, activatedNames)")
	j := strings.Index(s, "module.EvaluateRequirements(store)")
	if i != -1 && j != -1 && i < j {
		return Check{ID: "CQ04", Name: "MarkMissing before phase 3a", Result: Pass}
	}
	return Check{ID: "CQ04", Name: "MarkMissing before phase 3a", Result: Blocked, Message: "MarkMissing must be called before EvaluateRequirements"}
}

func checkCQ05() Check {
	b, _ := os.ReadFile("internal/module/requirements.go")
	s := string(b)
	// Two-phase contract inside EvaluateRequirements: Phase 1 runs system
	// checks across the store, then the ready surface is built from the
	// survivors, then Phase 2 checks capability requirements.
	fn := strings.Index(s, "func EvaluateRequirements(s *Store)")
	if fn == -1 {
		return Check{ID: "CQ05", Name: "Two-phase requirement evaluation", Result: Blocked, Message: "EvaluateRequirements signature not found"}
	}
	body := s[fn:]
	p1 := strings.Index(body, "evaluateSystemReqs(r)")
	surf := strings.Index(body, "readySurface(recs)")
	if p1 == -1 || surf == -1 || surf < p1 {
		return Check{ID: "CQ05", Name: "Two-phase requirement evaluation", Result: Blocked, Message: "ready surface must be built after Phase 1 system checks"}
	}
	return Check{ID: "CQ05", Name: "Two-phase requirement evaluation", Result: Pass}
}

func checkCQ06() Check {
	b, _ := os.ReadFile("internal/startup/startup.go")
	if strings.Contains(string(b), "for _, shortName := range module.SortedKeys(store)") {
		return Check{ID: "CQ06", Name: "Phase 3b sorted iteration", Result: Pass}
	}
	return Check{ID: "CQ06", Name: "Phase 3b sorted iteration", Result: Blocked, Message: "startup loop must use module.SortedKeys(store)"}
}

func checkCQ07() Check {
	b, _ := os.ReadFile("internal/activation/activation.go")
	s := string(b)
	if strings.Contains(s, "coreNames = map[string]bool{") &&
		strings.Contains(s, `"system": true`) &&
		strings.Contains(s, "if coreNames[shortName] {") {
		return Check{ID: "CQ07", Name: "system protected from deactivation", Result: Pass}
	}
	return Check{ID: "CQ07", Name: "system protected from deactivation", Result: Blocked, Message: "activation.Deactivate must refuse core names (system)"}
}

func checkCQ08() Check {
	b, _ := os.ReadFile("internal/startup/handshake.go")
	s := string(b)
	if strings.Contains(s, "var result protocol.HandshakeResult") &&
		strings.Contains(s, "live.Protocol != onDisk.Protocol") {
		return Check{ID: "CQ08", Name: "Handshake validates wrapper", Result: Pass}
	}
	return Check{ID: "CQ08", Name: "Handshake validates wrapper", Result: Blocked, Message: "handshake logic incomplete"}
}

func checkCQ09() Check {
	b, _ := os.ReadFile("internal/hooks/hooks.go")
	if strings.Contains(string(b), "SUCTL_REQ_TYPE=") && strings.Contains(string(b), "SUCTL_REQ_VALUE=") {
		return Check{ID: "CQ09", Name: "on-requirement-missing", Result: Pass}
	}
	return Check{ID: "CQ09", Name: "on-requirement-missing", Result: Blocked, Message: "hook environment missing requirement variables"}
}

func checkCQ12() Check {
	// activate.go must delegate both health events to the orchestrator, which
	// owns the failure escalation and recovery.
	a, _ := os.ReadFile("internal/startup/activate.go")
	act := string(a)
	if !strings.Contains(act, "c.onHealthFail(runner, entry, shortName)") ||
		!strings.Contains(act, "c.onHealthRecover(runner, entry, shortName)") {
		return Check{ID: "CQ12", Name: "on-health hooks non-blocking", Result: Blocked, Message: "health callbacks not delegated to the orchestrator"}
	}
	// Both events must fire through the async hook chokepoint, and the
	// chokepoint itself must be non-blocking (RunHookAsync).
	l, _ := os.ReadFile("internal/startup/lifecycle.go")
	lc := string(l)
	if !strings.Contains(lc, "c.fireAsync(runner, entry, \"on-health-fail\"") ||
		!strings.Contains(lc, "c.fireAsync(runner, entry, \"on-health-recover\"") {
		return Check{ID: "CQ12", Name: "on-health hooks non-blocking", Result: Blocked, Message: "health hooks not routed through c.fireAsync"}
	}
	if !strings.Contains(lc, "func (c *Coordinator) fireAsync(") || !strings.Contains(lc, "RunHookAsync(") {
		return Check{ID: "CQ12", Name: "on-health hooks non-blocking", Result: Blocked, Message: "fireAsync chokepoint is not non-blocking"}
	}
	return Check{ID: "CQ12", Name: "on-health hooks non-blocking", Result: Pass}
}

func checkCQ02() Check {
	f, _ := os.Open("cmd/main.go")
	defer f.Close()
	count := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), "activation.List(") {
			count++
		}
	}
	if count > 1 {
		return Check{ID: "CQ02", Name: "Activation list read once", Result: Blocked, Message: "called multiple times"}
	}
	return Check{ID: "CQ02", Name: "Activation list read once", Result: Pass}
}

func checkCQ11() Check {
	b, _ := os.ReadFile("internal/supervisor/supervisor.go")
	if strings.Contains(string(b), "go s.cfg.OnCrash()") {
		return Check{ID: "CQ11", Name: "on-crash non-blocking", Result: Pass}
	}
	return Check{ID: "CQ11", Name: "on-crash non-blocking", Result: Blocked, Message: "on-crash hook not launched in goroutine"}
}

func checkCQ14() Check {
	b, _ := os.ReadFile("internal/startup/stop.go")
	s := string(b)
	// Shutdown order is monitors -> modules. The broker owns no listening
	// socket (possession = identity), so there is no broker-close
	// step: each module wire closes when its process exits.
	mon := strings.Index(s, "r.TakeMonitor()")
	sup := strings.Index(s, "r.TakeSupervisor()")
	if mon == -1 || sup == -1 || mon >= sup {
		return Check{ID: "CQ14", Name: "Shutdown order", Result: Blocked, Message: "order is not monitors -> modules"}
	}
	if strings.Contains(s, "Broker.Stop()") {
		return Check{ID: "CQ14", Name: "Shutdown order", Result: Blocked, Message: "broker owns no socket; there must be no broker-close step"}
	}
	return Check{ID: "CQ14", Name: "Shutdown order", Result: Pass}
}

func checkCQ15() Check {
	b, _ := os.ReadFile("internal/logging/logging.go")
	if strings.Contains(string(b), "io.MultiWriter(f, os.Stdout)") {
		return Check{ID: "CQ15", Name: "Logging stdout tee", Result: Pass}
	}
	return Check{ID: "CQ15", Name: "Logging stdout tee", Result: Warn, Message: "MultiWriter for stdout tee not found"}
}

func checkCQ16() Check {
	b, _ := os.ReadFile("sdk/protocol/types.go")
	if strings.Contains(string(b), "\"crypto/rand\"") && strings.Contains(string(b), "rand.Read(b[:])") {
		return Check{ID: "CQ16", Name: "Job tokens UUID", Result: Pass}
	}
	return Check{ID: "CQ16", Name: "Job tokens UUID", Result: Warn, Message: "crypto/rand not used for job tokens"}
}

func checkCQ20() Check {
	m := system.Manifest()
	if m.Protocol != "1" {
		return Check{ID: "CQ20", Name: "System module contract", Result: Blocked, Message: "protocol must be 1"}
	}
	// Check surface completeness for system module
	rc := system.SurfaceConfig()
	if rc.Survey.Entry == "" || rc.Focus.Entry == "" {
		return Check{ID: "CQ20", Name: "System module contract", Result: Blocked, Message: "REPL survey or focus entry missing"}
	}
	return Check{ID: "CQ20", Name: "System module contract", Result: Pass}
}

func checkCQ21() Check {
	// CQ21: REPL must be a strict protocol client. It is forbidden from
	// importing core state (internal/module, internal/system) or lifecycle
	// logic (internal/startup).
	entries, err := os.ReadDir("internal/repl")
	if err != nil {
		return Check{ID: "CQ21", Name: "REPL protocol boundary", Result: Blocked, Message: err.Error()}
	}

	forbidden := []string{
		"\"github.com/solutionsunity/suctl/internal/module\"",
		"\"github.com/solutionsunity/suctl/internal/system\"",
		"\"github.com/solutionsunity/suctl/internal/startup\"",
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, _ := os.ReadFile("internal/repl/" + e.Name())
		s := string(b)

		// Strip comments before checking for imports to avoid false positives
		// from the architectural explanation in ctx.go.
		clean := stripComments(s)

		for _, p := range forbidden {
			if strings.Contains(clean, p) {
				return Check{ID: "CQ21", Name: "REPL protocol boundary", Result: Blocked, Message: e.Name() + " violates boundary by importing " + p}
			}
		}
	}
	return Check{ID: "CQ21", Name: "REPL protocol boundary", Result: Pass}
}

func stripComments(s string) string {
	var out []string
	scanner := bufio.NewScanner(strings.NewReader(s))
	for scanner.Scan() {
		line := scanner.Text()
		if i := strings.Index(line, "//"); i != -1 {
			line = line[:i]
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func checkCQ17() Check {
	cmd := exec.Command("go", "build", "./...")
	if err := cmd.Run(); err != nil {
		return Check{ID: "CQ17", Name: "Build passes", Result: Blocked, Message: err.Error()}
	}
	return Check{ID: "CQ17", Name: "Build passes", Result: Pass}
}

func checkCQ18() Check {
	cmd := exec.Command("go", "test", "./...")
	if err := cmd.Run(); err != nil {
		return Check{ID: "CQ18", Name: "Tests pass", Result: Warn, Message: err.Error()}
	}
	return Check{ID: "CQ18", Name: "Tests pass", Result: Pass}
}
