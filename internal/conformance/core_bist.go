// SPDX-License-Identifier: Apache-2.0

package conformance

import (
	"errors"
	"fmt"
	"time"

	sdkconf "github.com/solutionsunity/suctl/sdk/conformance"
	sdkmanifest "github.com/solutionsunity/suctl/sdk/manifest"
	"github.com/solutionsunity/suctl/sdk/protocol"
)

// ProbeCore runs BIST checks against the in-process orchestrator (the broker).
// Unlike ProbeModule, it only tests 'invoke' since the broker does not
// support handshake or health probes. The REPL contract is validated for every
// core surface (module, job, message) and every drill nested under them, so each
// surface's survey/columns/facets/focus contract is self-tested at startup, not
// just the module surface.
func ProbeCore(client protocol.Invoker, m *sdkmanifest.Manifest, surfaces []sdkmanifest.SurfaceConfig, timeout time.Duration) *sdkconf.Report {
	r := &sdkconf.Report{Passed: true}

	add := func(c sdkconf.Check) {
		if !c.Passed {
			r.Passed = false
		}
		r.Checks = append(r.Checks, c)
	}

	// 1 ── Capability dispatch probe ─────────────────────────────────────────
	for _, cap := range m.Capabilities {
		token := protocol.NewJobToken()
		_, err := client.InvokeWithTimeout(
			token, cap.Name, struct{}{}, timeout,
		)
		if err == nil {
			add(sdkconf.Check{Name: sdkconf.CheckCapDispatch, Subject: cap.Name, Passed: true})
			continue
		}
		var ed *protocol.ErrorDetail
		if errors.As(err, &ed) && ed.Code == protocol.CodeCapabilityNotActive {
			add(sdkconf.Check{
				Name:    sdkconf.CheckCapDispatch,
				Subject: cap.Name,
				Passed:  false,
				Message: "system capability declared but returned CAPABILITY_NOT_ACTIVE (not registered)",
			})
		} else if errors.As(err, &ed) {
			// INVALID_PARAMS, etc. are fine — it means the handler is wired.
			add(sdkconf.Check{Name: sdkconf.CheckCapDispatch, Subject: cap.Name, Passed: true})
		} else {
			add(sdkconf.Check{
				Name:    sdkconf.CheckCapDispatch,
				Subject: cap.Name,
				Passed:  false,
				Message: fmt.Sprintf("network/timeout: %v", err),
			})
		}
	}

	// 2 ── Unknown-capability rejection ──────────────────────────────────────
	fakeCapName := "system.__bist__.noexist"
	_, err := client.InvokeWithTimeout(
		protocol.NewJobToken(), fakeCapName, struct{}{}, timeout,
	)
	if err == nil {
		add(sdkconf.Check{
			Name:    sdkconf.CheckUnknownReject,
			Passed:  false,
			Message: fmt.Sprintf("%q returned ok; core must reject unknown capabilities", fakeCapName),
		})
	} else {
		var ed *protocol.ErrorDetail
		if errors.As(err, &ed) && ed.Code == protocol.CodeCapabilityNotActive {
			add(sdkconf.Check{Name: sdkconf.CheckUnknownReject, Passed: true})
		} else if errors.As(err, &ed) {
			add(sdkconf.Check{
				Name:    sdkconf.CheckUnknownReject,
				Passed:  false,
				Message: fmt.Sprintf("expected CAPABILITY_NOT_ACTIVE, got %v", ed.Code),
			})
		} else {
			add(sdkconf.Check{
				Name:    sdkconf.CheckUnknownReject,
				Passed:  false,
				Message: fmt.Sprintf("network/timeout: %v", err),
			})
		}
	}

	// 3 ── Inventory contract ───────────────────────────────────────────────
	sdkconf.ProbeInventory(client, timeout, add)

	// 4 ── REPL contract — every core surface and its drills ──────────────────
	// Drill surveys need a runtime scope (a parent-row id) BIST cannot supply, so
	// they are probed scope-gated: typed errors / empty results are dispatch-OK.
	for i := range surfaces {
		sdkconf.ProbeReplContract(client, "system", &surfaces[i], timeout, add)
		for j := range surfaces[i].Drills {
			sdkconf.ProbeReplContractDrill(client, "system", &surfaces[i].Drills[j], timeout, add)
		}
	}

	return r
}
