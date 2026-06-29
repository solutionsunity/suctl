// SPDX-License-Identifier: Apache-2.0

package conformance

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/solutionsunity/suctl/sdk/manifest"
	"github.com/solutionsunity/suctl/sdk/protocol"
)

// Check name constants — stable identifiers used in reports and failure reasons.
const (
	CheckCapDispatch    = "capability dispatch"
	CheckUnknownReject  = "unknown-capability rejection"
	CheckHealth         = "health probe"
	CheckSurfaceSurvey  = "surface survey"
	CheckSurfaceColumns = "surface column contract"
	CheckSurfaceFocus   = "surface focus"
	CheckHookExists     = "hook exists"
	CheckInventory      = "inventory contract"
	CheckEnvelope       = "envelope identity"
	CheckSurfaceFacets  = "surface facet vocabulary"
)

// Check is the result of one BIST sub-check.
type Check struct {
	Name    string // one of the Check* constants above
	Subject string // capability name, column ID, hook event — may be empty
	Passed  bool
	Message string // detail on failure; empty on pass
}

// Report is the structured output of conformance probes.
type Report struct {
	Passed bool    // true only when every Check.Passed is true
	Checks []Check // in execution order
}

// FailReason returns a compact single-line summary of the first failing check.
func (r *Report) FailReason() string {
	for _, c := range r.Checks {
		if !c.Passed {
			parts := []string{"bist", c.Name}
			if c.Subject != "" {
				parts = append(parts, c.Subject)
			}
			msg := strings.Join(parts, "/")
			if c.Message != "" {
				msg += ": " + c.Message
			}
			return msg
		}
	}
	return "bist: unknown failure"
}

// ModuleClient is the driver surface ProbeModule needs to exercise a live
// module: the invoke surface (Invoker) plus the module-only handshake and
// health commands. The harness's core-side wire mux and the core's own wire mux
// both satisfy it, so a module is probed the same way over its inherited wire
// whether driven by BIST or by the running core (possession = identity).
type ModuleClient interface {
	protocol.Invoker
	Handshake() (*protocol.Response, error)
	HealthWithTimeout(timeout time.Duration) (*protocol.HealthResult, error)
}

// Options configures a ProbeModule call.
type Options struct {
	// ProbeTimeout is the per-probe request deadline. Default: 2 s.
	ProbeTimeout time.Duration
	// ModuleDir, when non-empty, enables hook-existence checks.
	ModuleDir string
}

func (o *Options) applyDefaults() {
	if o.ProbeTimeout == 0 {
		o.ProbeTimeout = 2 * time.Second
	}
}

// ProbeModule runs the BIST checks against a live module over its wire.
//
// Precondition: handshake has already succeeded — the module is running and
// speaking the protocol. client drives the module's inherited wire; m is the
// validated on-disk manifest; surfaces is the parsed surface.json (empty if the
// module has no surface.json) — every declared surface is probed independently.
func ProbeModule(client ModuleClient, m *manifest.Manifest, surfaces []manifest.SurfaceConfig, opts Options) *Report {
	opts.applyDefaults()
	r := &Report{Passed: true}
	ns := manifest.ShortNameFromDir(opts.ModuleDir)

	add := func(c Check) {
		if !c.Passed {
			r.Passed = false
		}
		r.Checks = append(r.Checks, c)
	}

	// 0 ── Envelope identity probe ─────────────────────────────────────
	// The responder must echo the per-exchange id and stamp an RFC3339 ts_sent.
	if hs, err := client.Handshake(); err != nil {
		add(Check{Name: CheckEnvelope, Passed: false, Message: fmt.Sprintf("handshake failed: %v", err)})
	} else if hs.ID == "" {
		add(Check{Name: CheckEnvelope, Passed: false, Message: "response did not echo envelope id"})
	} else if hs.TsSent == "" {
		add(Check{Name: CheckEnvelope, Passed: false, Message: "response missing ts_sent"})
	} else if _, perr := time.Parse(time.RFC3339, hs.TsSent); perr != nil {
		add(Check{Name: CheckEnvelope, Passed: false, Message: fmt.Sprintf("ts_sent not RFC3339: %q", hs.TsSent)})
	} else {
		add(Check{Name: CheckEnvelope, Passed: true})
	}

	// 1 ── Capability dispatch probe ─────────────────────────────────────────
	// DISABLED — no decision yet.
	// This proof-of-existence invoked every declared capability with empty params
	// and accepted a typed error as proof the handler is wired. This is problematic
	// in case of parameterless capabilities, a better design/channel should be used:
	// a capability with no required params does not reject the empty invoke — it
	// runs to success and performs its real work during a mere existence check.
	// for _, cap := range m.Capabilities {
	// 	_, err := client.InvokeWithTimeout(
	// 		protocol.NewJobToken(), cap.Name, struct{}{}, opts.ProbeTimeout,
	// 	)
	// 	if err == nil {
	// 		add(Check{Name: CheckCapDispatch, Subject: cap.Name, Passed: true})
	// 		continue
	// 	}
	// 	var ed *protocol.ErrorDetail
	// 	switch {
	// 	case errors.As(err, &ed) && ed.Code == protocol.CodeUnknownCommand:
	// 		add(Check{
	// 			Name:    CheckCapDispatch,
	// 			Subject: cap.Name,
	// 			Passed:  false,
	// 			Message: "declared in manifest but returned UNKNOWN_COMMAND (not in dispatch)",
	// 		})
	// 	case errors.As(err, &ed):
	// 		add(Check{Name: CheckCapDispatch, Subject: cap.Name, Passed: true})
	// 	default:
	// 		add(Check{
	// 			Name:    CheckCapDispatch,
	// 			Subject: cap.Name,
	// 			Passed:  false,
	// 			Message: fmt.Sprintf("network/timeout: %v", err),
	// 		})
	// 	}
	// }

	// 2 ── Unknown-capability rejection ──────────────────────────────────────
	fakeCapName := ns + ".__bist__.noexist"
	_, err := client.InvokeWithTimeout(
		protocol.NewJobToken(), fakeCapName, struct{}{}, opts.ProbeTimeout,
	)
	if err == nil {
		add(Check{
			Name:    CheckUnknownReject,
			Passed:  false,
			Message: fmt.Sprintf("%q returned ok; module must reject unknown capabilities", fakeCapName),
		})
	} else {
		var ed *protocol.ErrorDetail
		if errors.As(err, &ed) {
			add(Check{Name: CheckUnknownReject, Passed: true})
		} else {
			add(Check{
				Name:    CheckUnknownReject,
				Passed:  false,
				Message: fmt.Sprintf("network/timeout probing unknown capability: %v", err),
			})
		}
	}

	// 3 ── Health probe ───────────────────────────────────────────────────────
	health, err := client.HealthWithTimeout(opts.ProbeTimeout)
	if err != nil {
		add(Check{Name: CheckHealth, Passed: false, Message: fmt.Sprintf("health failed: %v", err)})
	} else if health.Status == "" {
		add(Check{Name: CheckHealth, Passed: false, Message: "health response missing status field"})
	} else {
		add(Check{Name: CheckHealth, Passed: true})
	}

	// 4 ── REPL contract ──────────────────────────────────────────────────────
	// DISABLED — no decision yet; to be checked with a better design.
	// This walked the surface tree and invoked each surface's survey (and focus)
	// capability for real to validate the REPL render contract (columns/facets/
	// focus). It is the same hazard as the capability dispatch probe above: the
	// survey runs its real work during a mere conformance check. For an async-
	// declared or otherwise expensive survey that work runs inline under the probe
	// budget and cannot honor the accepted:true + job_update contract — BIST drives
	// the module wire directly, with no broker to hold the job or route the pushed
	// result — so it either times out or false-passes on the bare accept ack. A
	// better design/channel is owed before this is re-enabled; disabled until then.
	// for i := range surfaces {
	// 	probeReplContractTree(client, ns, &surfaces[i], false, opts.ProbeTimeout, add)
	// }

	// 5 ── Hook existence ─────────────────────────────────────────────────────
	if opts.ModuleDir != "" {
		for event, hook := range m.Hooks {
			if hook.Exec == "" {
				continue
			}
			hookPath := filepath.Join(opts.ModuleDir, hook.Exec)
			info, statErr := os.Stat(hookPath)
			switch {
			case statErr != nil:
				add(Check{
					Name:    CheckHookExists,
					Subject: event,
					Passed:  false,
					Message: fmt.Sprintf("exec %q not found: %v", hookPath, statErr),
				})
			case info.Mode()&0111 == 0:
				add(Check{
					Name:    CheckHookExists,
					Subject: event,
					Passed:  false,
					Message: fmt.Sprintf("exec %q is not executable", hookPath),
				})
			default:
				add(Check{Name: CheckHookExists, Subject: event, Passed: true})
			}
		}
	}

	return r
}

// probeReplContractTree walks a surface node and all its drill descendants.
// isDrill distinguishes the root (normal survey, no scope) from nested drill
// levels (scope-aware survey). Recursion handles arbitrarily deep nesting.
//
// DISABLED — disabled together with the REPL contract probe in ProbeModule
// (step 4), which is its only caller: it invoked the survey/focus capabilities
// for real. Kept here as the reference implementation for when an async-aware
// conformance design lands. ProbeReplContract / ProbeReplContractDrill remain
// live — core BIST (ProbeCore) drives them directly over in-process handlers.
// func probeReplContractTree(
// 	client ModuleClient,
// 	ns string,
// 	rc *manifest.SurfaceConfig,
// 	isDrill bool,
// 	timeout time.Duration,
// 	add func(Check),
// ) {
// 	if isDrill {
// 		ProbeReplContractDrill(client, ns, rc, timeout, add)
// 	} else {
// 		ProbeReplContract(client, ns, rc, timeout, add)
// 	}
// 	for i := range rc.Drills {
// 		probeReplContractTree(client, ns, &rc.Drills[i], true, timeout, add)
// 	}
// }

// PrintReport writes the formatted BIST report to stdout.
func (r *Report) PrintReport() {
	prev := ""
	for _, c := range r.Checks {
		if c.Name != prev {
			if c.Name == CheckCapDispatch {
				fmt.Printf("  capability dispatch\n")
			}
			prev = c.Name
		}
		label := c.Name
		if c.Name == CheckCapDispatch {
			label = "    " + c.Subject
		} else if c.Subject != "" {
			label = "  " + c.Name + ": " + c.Subject
		} else {
			label = "  " + c.Name
		}
		status := "PASS"
		if !c.Passed {
			status = "FAIL"
		}
		fmt.Printf("  %-52s%s\n", label, status)
		if !c.Passed && c.Message != "" {
			fmt.Printf("    [%s]\n", c.Message)
		}
	}
}

// Stats returns the total, passed, and failed check counts.
func (r *Report) Stats() (total, passed, failed int) {
	total = len(r.Checks)
	for _, c := range r.Checks {
		if c.Passed {
			passed++
		} else {
			failed++
		}
	}
	return total, passed, failed
}

// UnwrapOutput extracts the inner output from an invoke result envelope.
func UnwrapOutput(result json.RawMessage) json.RawMessage {
	var ir protocol.InvokeResponse
	if err := json.Unmarshal(result, &ir); err == nil && ir.Output != nil {
		return ir.Output
	}
	return result
}
