// SPDX-License-Identifier: Apache-2.0

// Package system — jobs.go implements the system.jobs.survey and
// system.jobs.focus capabilities. These read the in-memory job
// routing table and apply caller-identity filtering: the
// originating face (caller.Module == "") sees every job; a module caller
// sees only jobs whose owning module short name equals its own.
//
// The view is read-only on purpose — there are no inline actions and no
// focus actions. Operators wanting to cancel a job invoke a cancel
// capability on the owning module itself, not through this aggregated face.
package system

import (
	"fmt"
	"strings"
	"time"

	"github.com/solutionsunity/suctl/internal/module"
	"github.com/solutionsunity/suctl/sdk/protocol"
	"github.com/solutionsunity/suctl/sdk/surface"
)

// jobFilterForCaller returns the moduleFilter argument to pass to the messages
// store's Jobs derivation for the given caller. The originating face (Module ==
// "") receives "" so every job is enumerated; a module caller receives its own
// short name so only its jobs come back.
func jobFilterForCaller(caller module.CallerIdentity) string {
	return caller.Module
}

func (h *handlers) replJobsSurvey(req *protocol.Request, caller module.CallerIdentity) *protocol.Response {
	if h.msgs == nil {
		return okResponse(surface.SurveyResponse{Subjects: []surface.Subject{}})
	}
	filter := jobFilterForCaller(caller)

	params := invokeArgs(req)
	showSystem, _ := params["show_system"].(bool)

	all := h.msgs.Jobs(filter)
	var queued, running, done, failed, total int
	subjects := make([]surface.Subject, 0, len(all))
	for _, j := range all {
		// show_system is a scope toggle, not a facet — when system jobs are
		// hidden they are excluded from Total and StatusSummary too, so the
		// numbers stay consistent with the visible list.
		if !showSystem && strings.HasPrefix(j.Capability, "system.") {
			continue
		}
		total++
		switch j.State {
		case "queued":
			queued++
		case "running":
			running++
		case "done":
			done++
		case "failed":
			failed++
		}
		subjects = append(subjects, surface.Subject{
			ID:   j.Token,
			Name: shortToken(j.Token),
			Columns: map[string]surface.Column{
				"module":     surface.Col(j.Module, "blue"),
				"capability": surface.Col(j.Capability, "code"),
				"state":      surface.Col(j.State, jobStateColor(j.State)),
				"started":    surface.Col(relativeAge(j.StartedAt), "dim"),
			},
			Facets: []string{j.State},
		})
	}

	return okResponse(surface.SurveyResponse{
		Total:         total,
		StatusSummary: jobsSummary(queued, running, done, failed),
		Subjects:      subjects,
	})
}

func (h *handlers) replJobsFocus(req *protocol.Request, caller module.CallerIdentity) *protocol.Response {
	if h.msgs == nil {
		return errorResponse("NOT_FOUND", "messages store not configured")
	}
	params := invokeArgs(req)
	token, _ := params["subject"].(string)
	if token == "" {
		return errorResponse(protocol.CodeInvalidParams, "subject (job token) is required")
	}
	j, ok := h.msgs.Job(token)
	if !ok {
		return errorResponse("NOT_FOUND", fmt.Sprintf("job %q not found", shortToken(token)))
	}
	// Visibility check: module callers can only focus their own jobs.
	if caller.Module != "" && caller.Module != j.Module {
		return errorResponse("NOT_FOUND", fmt.Sprintf("job %q not found", shortToken(token)))
	}

	identity := []surface.Field{
		{Label: "token", Value: j.Token, FullWidth: true},
		{Label: "module", Value: j.Module, Color: "blue"},
		{Label: "capability", Value: j.Capability, Color: "code"},
	}
	// call type is the capability's declared async mode — the broker's own
	// contract (route.Async), never inferred from the runtime accepted flag.
	// Shown only while the (module, capability) is still resolvable in the store.
	if h.store != nil {
		if async, ok := h.store.CapabilityAsync(j.Module, j.Capability); ok {
			identity = append(identity, surface.Field{Label: "call", Value: callType(async), Color: "code"})
		}
	}
	identity = append(identity,
		surface.Field{Label: "state", Value: j.State, Color: jobStateColor(j.State)},
		surface.Field{Label: "queued", Value: j.QueuedAt.Format(time.RFC3339)},
	)
	// started is the promotion stamp — absent while the job is still queued.
	if !j.StartedAt.IsZero() {
		identity = append(identity, surface.Field{Label: "started", Value: j.StartedAt.Format(time.RFC3339)})
	}
	if !j.FinishedAt.IsZero() {
		identity = append(identity, surface.Field{Label: "finished", Value: j.FinishedAt.Format(time.RFC3339)})
		// Duration runs from promotion when known (a job that ran), else from
		// enqueue (a job that failed before ever starting, e.g. admission timeout).
		from := j.StartedAt
		if from.IsZero() {
			from = j.QueuedAt
		}
		identity = append(identity, surface.Field{Label: "duration", Value: j.FinishedAt.Sub(from).Round(time.Millisecond).String()})
	}
	if j.Progress > 0 {
		identity = append(identity, surface.Field{Label: "progress", Value: fmt.Sprintf("%d%%", j.Progress)})
	}

	sections := []surface.Section{{Title: "identity", Fields: identity}}
	// The per-job exchange record is reached by drilling into the messages
	// surface (scoped by job_token) — it shows the actual invoke, hops, and
	// reports, superseding a digest of job_update text here.
	if len(j.Output) > 0 {
		sections = append(sections, surface.Section{Title: "output", Fields: []surface.Field{
			{Label: "value", Value: formatJobOutput(j.Output), FullWidth: true},
		}})
	}
	if j.Error != nil {
		sections = append(sections, surface.Section{Title: "error", Fields: []surface.Field{
			{Label: "code", Value: j.Error.Code, Color: "alert"},
			{Label: "message", Value: j.Error.Message, Color: "alert", FullWidth: true},
		}})
	}

	return okResponse(surface.FocusResponse{
		ID:       j.Token,
		Name:     shortToken(j.Token),
		Sections: sections,
		// Read-only by design: no actions.
	})
}
