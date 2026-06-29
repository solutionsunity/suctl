// SPDX-License-Identifier: Apache-2.0

package system

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/solutionsunity/suctl/internal/activation"
	"github.com/solutionsunity/suctl/internal/broker"
	"github.com/solutionsunity/suctl/internal/messages"
	"github.com/solutionsunity/suctl/internal/module"
	"github.com/solutionsunity/suctl/sdk/protocol"
	"github.com/solutionsunity/suctl/sdk/surface"
)

// TestSurfaceConfigEntries pins the system module's survey/focus capability
// names and subject label. The "repl" segment was dropped
// in favour of the module-oriented names; guard against its return.
func TestSurfaceConfigEntries(t *testing.T) {
	rc := SurfaceConfig()
	if rc.Subject != "module" {
		t.Errorf("Subject = %q, want %q", rc.Subject, "module")
	}
	if rc.Name != "system" {
		t.Errorf("Name = %q, want %q", rc.Name, "system")
	}
	if rc.Desc == "" {
		t.Error("Desc is empty; want the control-plane description")
	}
	if rc.Survey.Entry != "system.module.survey" {
		t.Errorf("Survey.Entry = %q, want %q", rc.Survey.Entry, "system.module.survey")
	}
	if rc.Focus.Entry != "system.module.focus" {
		t.Errorf("Focus.Entry = %q, want %q", rc.Focus.Entry, "system.module.focus")
	}
}

// TestJobsSurfaceConfigEntries pins the jobs view's survey/focus capability
// names and subject label.
func TestJobsSurfaceConfigEntries(t *testing.T) {
	rc := JobsSurfaceConfig()
	if rc.Subject != "job" {
		t.Errorf("Subject = %q, want %q", rc.Subject, "job")
	}
	if rc.Name != "jobs" {
		t.Errorf("Name = %q, want %q", rc.Name, "jobs")
	}
	if rc.Desc == "" {
		t.Error("Desc is empty; want the jobs view description")
	}
	if rc.Survey.Entry != "system.jobs.survey" {
		t.Errorf("Survey.Entry = %q, want %q", rc.Survey.Entry, "system.jobs.survey")
	}
	if rc.Focus.Entry != "system.jobs.focus" {
		t.Errorf("Focus.Entry = %q, want %q", rc.Focus.Entry, "system.jobs.focus")
	}
}

// TestMessagesSurfaceConfigEntries pins the messages view's survey/focus
// capability names and subject label.
func TestMessagesSurfaceConfigEntries(t *testing.T) {
	rc := MessagesSurfaceConfig()
	if rc.Subject != "message" {
		t.Errorf("Subject = %q, want %q", rc.Subject, "message")
	}
	if rc.Name != "messages" {
		t.Errorf("Name = %q, want %q", rc.Name, "messages")
	}
	if rc.Desc == "" {
		t.Error("Desc is empty; want the messages view description")
	}
	if rc.Survey.Entry != "system.messages.survey" {
		t.Errorf("Survey.Entry = %q, want %q", rc.Survey.Entry, "system.messages.survey")
	}
	if rc.Focus.Entry != "system.messages.focus" {
		t.Errorf("Focus.Entry = %q, want %q", rc.Focus.Entry, "system.messages.focus")
	}
}

// TestJobsSurfaceDrill pins the jobs→exchange drill: the job surface nests a
// single drill whose subject is unique (it cannot reuse "message", which is a
// home root) and whose survey/focus reuse the messages capabilities.
func TestJobsSurfaceDrill(t *testing.T) {
	rc := JobsSurfaceConfig()
	if len(rc.Drills) != 1 {
		t.Fatalf("len(Drills) = %d, want 1", len(rc.Drills))
	}
	d := rc.Drills[0]
	if d.Subject != "exchange" {
		t.Errorf("drill Subject = %q, want %q", d.Subject, "exchange")
	}
	if d.Survey.Entry != "system.messages.survey" {
		t.Errorf("drill Survey.Entry = %q, want %q", d.Survey.Entry, "system.messages.survey")
	}
	if d.Focus.Entry != "system.messages.focus" {
		t.Errorf("drill Focus.Entry = %q, want %q", d.Focus.Entry, "system.messages.focus")
	}
}

// scopedMessagesStore seeds two jobs owned by different modules: "alpha" (token
// tok-alpha) and "beta" (token tok-beta), each one completed invoke exchange.
func scopedMessagesStore() *messages.Store {
	s := messages.New()
	s.Open(protocol.Request{ID: "a1", Cmd: "invoke", JobToken: "tok-alpha"}, "alpha", "alpha.do", "")
	s.Complete("a1", protocol.Response{Status: "ok"})
	s.Open(protocol.Request{ID: "b1", Cmd: "invoke", JobToken: "tok-beta"}, "beta", "beta.do", "")
	s.Complete("b1", protocol.Response{Status: "ok"})
	return s
}

func surveyResult(t *testing.T, resp *protocol.Response) surface.SurveyResponse {
	t.Helper()
	if resp.Status != "ok" {
		t.Fatalf("survey Status = %q; want ok", resp.Status)
	}
	var sr surface.SurveyResponse
	if err := json.Unmarshal(resp.Result, &sr); err != nil {
		t.Fatalf("decode survey response: %v", err)
	}
	return sr
}

func msgReq(args map[string]interface{}) *protocol.Request {
	return &protocol.Request{Cmd: "invoke", Params: args}
}

// TestMessagesSurveyScoping pins the caller-identity scoping mirrored from jobs:
// the system caller sees every exchange; a module caller sees only its own jobs'
// exchanges; the drill (scope) is allowed only on a job the caller owns.
func TestMessagesSurveyScoping(t *testing.T) {
	h := &handlers{msgs: scopedMessagesStore()}

	// system caller (empty Module): all exchanges.
	sys := surveyResult(t, h.replMessagesSurvey(msgReq(nil), module.CallerIdentity{}))
	if sys.Total != 2 {
		t.Fatalf("system Total = %d, want 2", sys.Total)
	}

	// module caller: only its own job's exchange.
	alpha := surveyResult(t, h.replMessagesSurvey(msgReq(nil), module.CallerIdentity{Module: "alpha"}))
	if alpha.Total != 1 || alpha.Subjects[0].ID != "a1" {
		t.Fatalf("alpha survey = %+v, want only a1", alpha.Subjects)
	}

	// drill into own job: allowed.
	own := surveyResult(t, h.replMessagesSurvey(msgReq(map[string]interface{}{"scope": "tok-alpha"}), module.CallerIdentity{Module: "alpha"}))
	if own.Total != 1 || own.Subjects[0].ID != "a1" {
		t.Fatalf("alpha own-drill = %+v, want only a1", own.Subjects)
	}

	// drill into another module's job: empty.
	other := surveyResult(t, h.replMessagesSurvey(msgReq(map[string]interface{}{"scope": "tok-beta"}), module.CallerIdentity{Module: "alpha"}))
	if other.Total != 0 {
		t.Fatalf("alpha cross-drill Total = %d, want 0", other.Total)
	}
}

// TestMessagesFocusScoping pins focus visibility: a module caller may focus its
// own exchange but gets NOT_FOUND for another's; the system caller sees both.
func TestMessagesFocusScoping(t *testing.T) {
	h := &handlers{msgs: scopedMessagesStore()}

	own := h.replMessagesFocus(msgReq(map[string]interface{}{"subject": "a1"}), module.CallerIdentity{Module: "alpha"})
	if own.Status != "ok" {
		t.Fatalf("alpha focus own Status = %q; want ok", own.Status)
	}

	cross := h.replMessagesFocus(msgReq(map[string]interface{}{"subject": "b1"}), module.CallerIdentity{Module: "alpha"})
	if cross.Status != "error" || cross.Error == nil || cross.Error.Code != "NOT_FOUND" {
		t.Fatalf("alpha focus cross = %+v; want NOT_FOUND error", cross)
	}

	sys := h.replMessagesFocus(msgReq(map[string]interface{}{"subject": "b1"}), module.CallerIdentity{})
	if sys.Status != "ok" {
		t.Fatalf("system focus Status = %q; want ok", sys.Status)
	}
}

// TestNoReplCapabilityNames asserts no registered or manifest-declared
// capability still carries the legacy ".repl." segment.
func TestNoReplCapabilityNames(t *testing.T) {
	for _, c := range Manifest().Capabilities {
		if strings.Contains(c.Name, ".repl.") {
			t.Errorf("capability %q still contains legacy .repl. segment", c.Name)
		}
	}
}

// deactivateReq builds an invoke request for system.module.deactivate against
// the given subject.
func deactivateReq(subject string) *protocol.Request {
	return &protocol.Request{Params: map[string]interface{}{"subject": subject}}
}

// TestModuleDeactivate_RefusesBusyModule proves the operator-facing ops-gate:
// when the busy-check reports the module busy, moduleDeactivate returns an
// error and the activation flag survives — disk intent never diverges from a
// module that keeps running because the lifecycle teardown is itself refused.
func TestModuleDeactivate_RefusesBusyModule(t *testing.T) {
	stateDir := t.TempDir()
	if err := activation.Activate(stateDir, "foo"); err != nil {
		t.Fatalf("seed activation flag: %v", err)
	}

	store := module.NewStore()
	store.Put("foo", module.NewRecord(module.StateActive, nil))
	h := &handlers{
		store:    store,
		stateDir: stateDir,
		busyFn:   func(string) (string, bool) { return "job-1", true },
	}

	resp := h.moduleDeactivate(deactivateReq("foo"), broker.CallerIdentity{})
	if resp.Status != "error" {
		t.Fatalf("Status = %q; want error for a busy module", resp.Status)
	}
	if active, _ := activation.IsActivated(stateDir, "foo"); !active {
		t.Error("activation flag dropped despite refused deactivation")
	}
}

// TestModuleDeactivate_ProceedsWhenIdle proves the happy path: with the busy-check
// reporting the module idle, moduleDeactivate drops the activation flag and
// reports success.
func TestModuleDeactivate_ProceedsWhenIdle(t *testing.T) {
	stateDir := t.TempDir()
	if err := activation.Activate(stateDir, "foo"); err != nil {
		t.Fatalf("seed activation flag: %v", err)
	}

	store := module.NewStore()
	store.Put("foo", module.NewRecord(module.StateActive, nil))
	h := &handlers{
		store:    store,
		stateDir: stateDir,
		busyFn:   func(string) (string, bool) { return "", false },
	}

	resp := h.moduleDeactivate(deactivateReq("foo"), broker.CallerIdentity{})
	if resp.Status != "ok" {
		t.Fatalf("Status = %q; want ok for an idle module", resp.Status)
	}
	if active, _ := activation.IsActivated(stateDir, "foo"); active {
		t.Error("activation flag not dropped on a permitted deactivation")
	}
}

// TestModuleDeactivate_UnknownModule proves the existence check: deactivating a
// module that is not in the store is a NOT_FOUND error, not a silent ok — it
// mirrors moduleActivate and prevents a bogus subject from reporting success.
func TestModuleDeactivate_UnknownModule(t *testing.T) {
	h := &handlers{
		store:    module.NewStore(),
		stateDir: t.TempDir(),
		busyFn:   func(string) (string, bool) { return "", false },
	}

	resp := h.moduleDeactivate(deactivateReq("ghost"), broker.CallerIdentity{})
	if resp.Status != "error" || resp.Error == nil || resp.Error.Code != "NOT_FOUND" {
		t.Fatalf("Status/Error = %+v; want NOT_FOUND for an unknown module", resp)
	}
}

// TestInvokeEnvelopeNoSubjectLeak pins the invokeArgs fix: an invoke routed
// through the broker whose args is not an object (the in-process BIST probe
// sends struct{}{}) must surface as INVALID_PARAMS "subject is required" — the
// envelope's "name" (the capability name) must never leak through as the
// subject and resolve to a spurious NOT_FOUND. Covers module.focus, activate,
// and deactivate, the three handlers that read "subject".
func TestInvokeEnvelopeNoSubjectLeak(t *testing.T) {
	h := &handlers{store: module.NewStore(), stateDir: t.TempDir()}

	cases := []struct {
		name string
		call func(*protocol.Request) *protocol.Response
		cap  string
	}{
		{"focus", func(r *protocol.Request) *protocol.Response { return h.replFocus(r, module.CallerIdentity{}) }, "system.module.focus"},
		{"activate", func(r *protocol.Request) *protocol.Response { return h.moduleActivate(r, module.CallerIdentity{}) }, "system.module.activate"},
		{"deactivate", func(r *protocol.Request) *protocol.Response { return h.moduleDeactivate(r, module.CallerIdentity{}) }, "system.module.deactivate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Broker envelope with a non-object args payload, mirroring the
			// in-process dispatch probe (struct{}{}).
			req := &protocol.Request{Cmd: "invoke", Params: map[string]interface{}{
				"name": tc.cap,
				"args": struct{}{},
			}}
			resp := tc.call(req)
			if resp.Status != "error" || resp.Error == nil || resp.Error.Code != protocol.CodeInvalidParams {
				t.Fatalf("%s = %+v; want INVALID_PARAMS, not a capability-name leak", tc.cap, resp)
			}
		})
	}
}
