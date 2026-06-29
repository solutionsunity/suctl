// SPDX-License-Identifier: Apache-2.0

package conformance

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/solutionsunity/suctl/sdk/manifest"
	"github.com/solutionsunity/suctl/sdk/protocol"
	"github.com/solutionsunity/suctl/sdk/surface"
	"github.com/solutionsunity/suctl/sdk/system"
)

// ProbeInventory validates that system.module.inventory returns a valid response.
func ProbeInventory(client protocol.Invoker, timeout time.Duration, add func(Check)) {
	resp, err := client.InvokeWithTimeout(
		protocol.NewJobToken(), "system.module.inventory", struct{}{}, timeout,
	)
	if err != nil {
		add(Check{Name: CheckInventory, Passed: false, Message: fmt.Sprintf("inventory failed: %v", err)})
	} else {
		output := UnwrapOutput(resp.Result)
		var ir system.InventoryResponse
		if decErr := json.Unmarshal(output, &ir); decErr != nil {
			add(Check{Name: CheckInventory, Passed: false, Message: fmt.Sprintf("decode failed: %v", decErr)})
		} else {
			add(Check{Name: CheckInventory, Passed: true})
		}
	}
}

// ProbeSurvey validates that the named survey capability returns a valid response.
// A typed error (*protocol.ErrorDetail) is treated as pass: the capability dispatched
// correctly; the failure is a runtime dependency concern (e.g. backing service down),
// not a conformance defect. Only network/timeout errors fail the check.
// When a typed error is returned nil is returned so column and focus checks are skipped.
func ProbeSurvey(client protocol.Invoker, capName string, timeout time.Duration, add func(Check)) *surface.SurveyResponse {
	resp, err := client.InvokeWithTimeout(
		protocol.NewJobToken(), capName, map[string]interface{}{}, timeout,
	)
	if err != nil {
		var ed *protocol.ErrorDetail
		if errors.As(err, &ed) {
			// Typed error: capability dispatched; backing service unavailable at probe time.
			add(Check{Name: CheckSurfaceSurvey, Passed: true,
				Message: fmt.Sprintf("typed error (service unavailable): %s", ed.Code)})
			return nil
		}
		add(Check{Name: CheckSurfaceSurvey, Passed: false, Message: fmt.Sprintf("survey failed: %v", err)})
		return nil
	}

	output := UnwrapOutput(resp.Result)
	var sr surface.SurveyResponse
	if decErr := json.Unmarshal(output, &sr); decErr != nil {
		add(Check{Name: CheckSurfaceSurvey, Passed: false, Message: fmt.Sprintf("decode failed: %v", decErr)})
		return nil
	}

	add(Check{Name: CheckSurfaceSurvey, Passed: true})
	return &sr
}

// ProbeFocus validates that the named focus capability returns a valid response for the given ID.
// A typed error (*protocol.ErrorDetail) is treated as pass: the capability dispatched correctly;
// the failure is a runtime dependency concern, not a conformance defect.
// Network and timeout errors are failures: focus must respond within the probe budget.
func ProbeFocus(client protocol.Invoker, capName, subjectID string, timeout time.Duration, add func(Check)) {
	resp, err := client.InvokeWithTimeout(
		protocol.NewJobToken(), capName, map[string]interface{}{"subject": subjectID}, timeout,
	)
	if err != nil {
		var ed *protocol.ErrorDetail
		if errors.As(err, &ed) {
			add(Check{Name: CheckSurfaceFocus, Passed: true,
				Message: fmt.Sprintf("typed error (service unavailable): %s", ed.Code)})
			return
		}
		add(Check{Name: CheckSurfaceFocus, Passed: false, Message: fmt.Sprintf("focus failed: %v", err)})
		return
	}

	output := UnwrapOutput(resp.Result)
	var fr surface.FocusResponse
	if decErr := json.Unmarshal(output, &fr); decErr != nil {
		add(Check{Name: CheckSurfaceFocus, Passed: false, Message: fmt.Sprintf("decode failed: %v", decErr)})
	} else if fr.ID == "" || fr.Name == "" {
		add(Check{Name: CheckSurfaceFocus, Passed: false, Message: "missing id or name field"})
	} else {
		add(Check{Name: CheckSurfaceFocus, Passed: true})
	}
}

// ProbeReplContract validates the survey and focus contract for a module.
func ProbeReplContract(
	client protocol.Invoker,
	ns string,
	rc *manifest.SurfaceConfig,
	timeout time.Duration,
	add func(Check),
) {
	// 1 ── Survey contract
	sr := ProbeSurvey(client, rc.Survey.Entry, timeout, add)
	if sr == nil {
		return // survey failed; cannot test columns or focus
	}

	// 2 ── Column contract
	// All columns in surface.json must exist in every subject of the survey response.
	if len(sr.Subjects) > 0 {
		allColsPresent := true
		for _, sub := range sr.Subjects {
			for _, col := range rc.Survey.Columns {
				if _, ok := sub.Columns[col.ID]; !ok {
					add(Check{
						Name:    CheckSurfaceColumns,
						Subject: col.ID,
						Passed:  false,
						Message: fmt.Sprintf("subject %q missing column %q declared in surface.json", sub.ID, col.ID),
					})
					allColsPresent = false
				}
			}
		}
		if allColsPresent {
			add(Check{Name: CheckSurfaceColumns, Passed: true})
		}
	} else {
		add(Check{Name: CheckSurfaceColumns, Passed: true, Message: "skipped (no subjects)"})
	}

	// 3 ── Facet vocabulary contract (D68)
	// When surface.json declares facets, every facet value on every returned
	// subject must belong to the declared vocabulary. An undeclared value means
	// the module is tagging rows with values the REPL cannot map to a chip —
	// those tags would silently never match any active filter.
	ProbeFacetVocabulary(sr.Subjects, rc.Survey.Facets, add)

	// 4 ── Focus contract
	// Pick the first subject from the survey and try to focus it.
	if len(sr.Subjects) > 0 {
		ProbeFocus(client, rc.Focus.Entry, sr.Subjects[0].ID, timeout, add)
	} else {
		// No subjects? We can't test focus, but that's not a failure of the module
		// itself — just an empty state.
		add(Check{Name: CheckSurfaceFocus, Passed: true, Message: "skipped (no subjects in survey)"})
	}
}

// ProbeFacetVocabulary checks that every facet value on every subject belongs
// to the declared vocabulary. Skipped when no facets are declared or no
// subjects were returned. An out-of-vocabulary value means the module is
// producing tags the REPL cannot map to a chip — they would silently never
// match any active filter.
func ProbeFacetVocabulary(subjects []surface.Subject, declared []manifest.SurfaceFacetConfig, add func(Check)) {
	if len(declared) == 0 || len(subjects) == 0 {
		add(Check{Name: CheckSurfaceFacets, Passed: true, Message: "skipped (no facets declared or no subjects)"})
		return
	}
	vocab := make(map[string]bool, len(declared))
	for _, f := range declared {
		vocab[f.Value] = true
	}
	for _, sub := range subjects {
		for _, v := range sub.Facets {
			if !vocab[v] {
				add(Check{
					Name:    CheckSurfaceFacets,
					Subject: sub.ID,
					Passed:  false,
					Message: fmt.Sprintf("subject %q carries facet value %q not declared in surface.json", sub.ID, v),
				})
				return
			}
		}
	}
	add(Check{Name: CheckSurfaceFacets, Passed: true})
}

// ProbeSurveyScoped calls a drill survey capability with an injected sentinel
// scope. BIST cannot supply a real parent-row id at probe time, so a typed-
// error response (e.g. INVALID_PARAMS for an unrecognised scope) is still a
// PASS — the capability dispatched correctly; only a network/timeout failure
// or a successful-but-undecodable response is a FAIL.
func ProbeSurveyScoped(client protocol.Invoker, capName string, timeout time.Duration, add func(Check)) *surface.SurveyResponse {
	resp, err := client.InvokeWithTimeout(
		protocol.NewJobToken(), capName,
		map[string]interface{}{"scope": "__bist__"},
		timeout,
	)
	if err != nil {
		var ed *protocol.ErrorDetail
		if errors.As(err, &ed) {
			// Typed error (e.g. INVALID_PARAMS for synthetic scope) — dispatch verified.
			add(Check{
				Name:    CheckSurfaceSurvey,
				Subject: capName,
				Passed:  true,
				Message: "scope-gated: " + ed.Code,
			})
			return nil
		}
		add(Check{
			Name:    CheckSurfaceSurvey,
			Subject: capName,
			Passed:  false,
			Message: fmt.Sprintf("drill survey network/timeout: %v", err),
		})
		return nil
	}
	output := UnwrapOutput(resp.Result)
	var sr surface.SurveyResponse
	if decErr := json.Unmarshal(output, &sr); decErr != nil {
		add(Check{
			Name:    CheckSurfaceSurvey,
			Subject: capName,
			Passed:  false,
			Message: fmt.Sprintf("drill survey decode failed: %v", decErr),
		})
		return nil
	}
	add(Check{Name: CheckSurfaceSurvey, Subject: capName, Passed: true})
	return &sr
}

// ProbeReplContractDrill validates the REPL contract for a drill surface.
// Drill surveys require a runtime scope (parent-row id) that BIST cannot
// manufacture. ProbeSurveyScoped is used: it injects a sentinel scope and
// treats typed errors as dispatch-verified. Column and focus checks are only
// performed when the survey returns subjects despite the sentinel scope
// (some implementations may return empty rather than error — both are valid).
func ProbeReplContractDrill(
	client protocol.Invoker,
	ns string,
	rc *manifest.SurfaceConfig,
	timeout time.Duration,
	add func(Check),
) {
	sr := ProbeSurveyScoped(client, rc.Survey.Entry, timeout, add)
	if sr == nil || len(sr.Subjects) == 0 {
		// Survey returned typed error or no subjects — columns and focus cannot
		// be exercised without a real scope. Both are acceptable at BIST time.
		add(Check{Name: CheckSurfaceColumns, Subject: rc.Survey.Entry, Passed: true, Message: "skipped (drill: needs runtime scope)"})
		add(Check{Name: CheckSurfaceFocus, Subject: rc.Focus.Entry, Passed: true, Message: "skipped (drill: needs runtime scope)"})
		return
	}
	// Survey returned subjects with the sentinel scope (e.g. module returns empty
	// rather than error). Verify column contract and focus with the first subject.
	allOK := true
	for _, sub := range sr.Subjects {
		for _, col := range rc.Survey.Columns {
			if _, ok := sub.Columns[col.ID]; !ok {
				add(Check{
					Name:    CheckSurfaceColumns,
					Subject: col.ID,
					Passed:  false,
					Message: fmt.Sprintf("drill subject %q missing column %q", sub.ID, col.ID),
				})
				allOK = false
			}
		}
	}
	if allOK {
		add(Check{Name: CheckSurfaceColumns, Passed: true})
	}
	ProbeFocus(client, rc.Focus.Entry, sr.Subjects[0].ID, timeout, add)
}
