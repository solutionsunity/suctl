// SPDX-License-Identifier: Apache-2.0

// Package system — messages.go implements the system.messages.survey and
// system.messages.focus capabilities. These list the messages store: each
// record is one exchange (a request envelope and its response) keyed by the
// request id.
//
// Caller-identity scoping mirrors the jobs surface: the system caller
// (caller.Module == "") sees every recorded exchange; a module caller sees only
// the exchanges of jobs it owns. Ownership is read the same way jobs reads it —
// from the owning module of the exchange's job (Job(token).Module) — so a module
// can drill its own job's exchanges but never another's.
package system

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/solutionsunity/suctl/internal/messages"
	"github.com/solutionsunity/suctl/internal/module"
	"github.com/solutionsunity/suctl/sdk/protocol"
	"github.com/solutionsunity/suctl/sdk/surface"
)

func (h *handlers) replMessagesSurvey(req *protocol.Request, caller module.CallerIdentity) *protocol.Response {
	if h.msgs == nil {
		return okResponse(surface.SurveyResponse{Subjects: []surface.Subject{}})
	}

	// scope (a job token) is set when this survey backs the jobs→messages drill;
	// then only that job's exchanges are listed (oldest first, the exchange
	// order). Absent, every recorded exchange is listed (newest first).
	params := invokeArgs(req)
	scope, _ := params["scope"].(string)
	var all []messages.Record
	if scope != "" {
		// A module caller may drill only its own job's exchanges; a scope it does
		// not own yields an empty list (same boundary jobs.focus enforces).
		if caller.Module != "" && caller.Module != h.jobOwner(scope) {
			return okResponse(surface.SurveyResponse{Subjects: []surface.Subject{}})
		}
		all = h.msgs.ByToken(scope)
	} else {
		// The system caller sees every recorded exchange; a module caller sees
		// only the exchanges of jobs it owns.
		for _, m := range h.msgs.List(0) {
			if h.ownsExchange(caller, m) {
				all = append(all, m)
			}
		}
	}

	subjects := make([]surface.Subject, len(all))
	for i, m := range all {
		state := messageState(m)
		subjects[i] = surface.Subject{
			ID:   m.ID(),
			Name: shortToken(m.ID()),
			Columns: map[string]surface.Column{
				"cmd":        surface.Col(m.Request.Cmd, "blue"),
				"caller":     surface.Col(messageCaller(m.Caller), "code"),
				"module":     surface.Col(m.Module, "code"),
				"capability": surface.Col(messageCapability(m.Capability), "code"),
				"state":      surface.Col(state, messageStateColor(state)),
				"age":        surface.Col(relativeAge(m.ReqReceived), "dim"),
			},
			Facets: []string{state},
		}
	}

	return okResponse(surface.SurveyResponse{
		Total:         len(all),
		StatusSummary: fmt.Sprintf("%d messages recorded", len(all)),
		Subjects:      subjects,
	})
}

func (h *handlers) replMessagesFocus(req *protocol.Request, caller module.CallerIdentity) *protocol.Response {
	if h.msgs == nil {
		return errorResponse("NOT_FOUND", "messages store not configured")
	}
	params := invokeArgs(req)
	id, _ := params["subject"].(string)
	if id == "" {
		return errorResponse(protocol.CodeInvalidParams, "subject (message id) is required")
	}

	m, ok := h.msgs.Lookup(id)
	if !ok {
		return errorResponse("NOT_FOUND", fmt.Sprintf("message %q not found", shortToken(id)))
	}
	// Visibility check: a module caller can only focus exchanges of jobs it owns;
	// others are reported NOT_FOUND, the same opaque boundary jobs.focus uses.
	if !h.ownsExchange(caller, m) {
		return errorResponse("NOT_FOUND", fmt.Sprintf("message %q not found", shortToken(id)))
	}

	identity := []surface.Field{
		{Label: "id", Value: m.ID(), FullWidth: true},
		{Label: "cmd", Value: m.Request.Cmd, Color: "blue"},
		{Label: "caller", Value: messageCaller(m.Caller), Color: "code"},
		{Label: "module", Value: m.Module, Color: "code"},
		{Label: "capability", Value: m.Capability, Color: "code"},
		{Label: "token", Value: m.JobToken(), FullWidth: true},
		{Label: "received", Value: m.ReqReceived.Format(time.RFC3339)},
	}
	if m.Done() {
		identity = append(identity, surface.Field{Label: "responded", Value: m.RespReceived.Format(time.RFC3339)})
	}

	sections := []surface.Section{{Title: "exchange", Fields: identity}}

	reqParams := "(none)"
	if m.Request.Params != nil {
		reqParams = formatMessagePayload(m.Request.Params)
	}
	sections = append(sections, surface.Section{
		Title:  "request",
		Fields: []surface.Field{{Label: "params", Value: reqParams, FullWidth: true, Color: "code"}},
	})

	if m.Done() {
		respFields := []surface.Field{{Label: "status", Value: m.Response.Status, Color: "blue"}}
		if len(m.Response.Result) > 0 {
			respFields = append(respFields, surface.Field{Label: "result", Value: formatJobOutput(m.Response.Result), FullWidth: true, Color: "code"})
		}
		if m.Response.Error != nil {
			respFields = append(respFields, surface.Field{
				Label: "error", Value: m.Response.Error.Code + ": " + m.Response.Error.Message,
				FullWidth: true, Color: "alert",
			})
		}
		sections = append(sections, surface.Section{Title: "response", Fields: respFields})
	}

	return okResponse(surface.FocusResponse{
		ID:       m.ID(),
		Name:     shortToken(m.ID()),
		Sections: sections,
	})
}


// ownsExchange reports whether caller may see exchange m. The system caller
// (empty Module) sees every exchange; a module caller sees only exchanges of a
// job it owns — a non-job exchange (empty token) belongs to no module job, so it
// stays system-only.
func (h *handlers) ownsExchange(caller module.CallerIdentity, m messages.Record) bool {
	if caller.Module == "" {
		return true
	}
	token := m.JobToken()
	return token != "" && caller.Module == h.jobOwner(token)
}

// jobOwner returns the owning module short name of the job token, read the same
// way the jobs surface reads it — from the job's originating exchange
// attribution. Empty when the token names no known job.
func (h *handlers) jobOwner(token string) string {
	if j, ok := h.msgs.Job(token); ok {
		return j.Module
	}
	return ""
}

// messageState derives an exchange's state from its recorded response: pending
// until the response side is filled, then ok or error by the response status.
func messageState(m messages.Record) string {
	if !m.Done() {
		return "pending"
	}
	if m.Response.Status == "error" {
		return "error"
	}
	return "ok"
}

// messageStateColor maps an exchange state to the theme colour token used by the
// REPL renderer.
func messageStateColor(state string) interface{} {
	switch state {
	case "pending":
		return "info"
	case "ok":
		return "ok"
	case "error":
		return "alert"
	default:
		return nil
	}
}

// messageCapability renders the routed capability, or an em dash for exchanges
// with none — job_update reports are opened with no capability attribution.
func messageCapability(cap string) string {
	if cap == "" {
		return "—"
	}
	return cap
}

// messageCaller renders the calling module, or "system" for the empty caller —
// the in-process face / control plane that originates a job.
func messageCaller(caller string) string {
	if caller == "" {
		return "system"
	}
	return caller
}

func formatMessagePayload(v interface{}) string {
	if raw, ok := v.(json.RawMessage); ok {
		return formatJobOutput(raw)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%+v", v)
	}
	return prettyJSONString(string(b))
}
