// SPDX-License-Identifier: Apache-2.0

package messages

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/solutionsunity/suctl/sdk/protocol"
)

// invoke opens an originating invoke exchange for token, attributed to
// module/capability, keyed by id.
func invoke(s *Store, id, token, module, cap string) {
	s.Open(protocol.Request{ID: id, Cmd: "invoke", JobToken: token,
		Params: map[string]interface{}{"name": cap}}, module, cap, "")
}

// okResult marshals an InvokeResponse into a response result body.
func okResult(ir protocol.InvokeResponse) json.RawMessage {
	b, _ := json.Marshal(ir)
	return b
}

// TestOpenCompleteLookup pins the request/response halves of one exchange and
// the Done transition.
func TestOpenCompleteLookup(t *testing.T) {
	s := New()
	invoke(s, "r1", "t1", "nginx", "nginx.reload")

	if r, _ := s.Lookup("r1"); r.Done() {
		t.Fatal("record is Done before its response is recorded")
	}
	if ok := s.Complete("r1", protocol.Response{Status: "ok"}); !ok {
		t.Fatal("Complete returned false for an open record")
	}
	r, ok := s.Lookup("r1")
	if !ok || !r.Done() || r.Module != "nginx" || r.Capability != "nginx.reload" {
		t.Fatalf("record after Complete = %+v; want done, attributed", r)
	}
	if s.Complete("missing", protocol.Response{}) {
		t.Error("Complete returned true for an orphan response")
	}
}

// TestListOrderAndByToken pins List (newest first, honouring limit) and ByToken
// (oldest first — the fold order job derivation relies on).
func TestListOrderAndByToken(t *testing.T) {
	s := New()
	invoke(s, "r1", "t1", "nginx", "nginx.reload")
	invoke(s, "r2", "t1", "nginx", "nginx.reload")

	all := s.List(0)
	if len(all) != 2 || all[0].ID() != "r2" {
		t.Fatalf("List newest-first = %v; want [r2 r1]", ids(all))
	}
	if one := s.List(1); len(one) != 1 || one[0].ID() != "r2" {
		t.Fatalf("List(1) = %v; want [r2]", ids(one))
	}
	if grp := s.ByToken("t1"); len(grp) != 2 || grp[0].ID() != "r1" {
		t.Fatalf("ByToken oldest-first = %v; want [r1 r2]", ids(grp))
	}
}

// TestJob_SyncDone proves a synchronous ok terminal response folds the job to
// done and carries the capability output.
func TestJob_SyncDone(t *testing.T) {
	s := New()
	invoke(s, "r1", "t1", "nginx", "nginx.reload")
	s.Complete("r1", protocol.Response{Status: "ok",
		Result: okResult(protocol.InvokeResponse{Name: "nginx.reload", Output: json.RawMessage(`{"ok":true}`)})})

	j, ok := s.Job("t1")
	if !ok || j.State != "done" || j.Module != "nginx" {
		t.Fatalf("Job = %+v; want done/nginx", j)
	}
	if string(j.Output) != `{"ok":true}` {
		t.Errorf("Output = %s; want {\"ok\":true}", j.Output)
	}
}

// TestJob_AsyncRunsThenUpdates proves an accepted async invoke is running until a
// terminal job_update folds it to done, accumulating progress and messages.
func TestJob_AsyncRunsThenUpdates(t *testing.T) {
	s := New()
	invoke(s, "r1", "t1", "nginx", "nginx.provision")
	// The queue manager promotes the job (stamps Started) before the handler
	// runs; without the Started fact the job would derive as queued, not running.
	s.Start("t1")
	s.Complete("r1", protocol.Response{Status: "ok",
		Result: okResult(protocol.InvokeResponse{Name: "nginx.provision", Accepted: true})})

	if j, _ := s.Job("t1"); j.State != "running" {
		t.Fatalf("accepted async job State = %q; want running", j.State)
	}

	s.Open(protocol.Request{ID: "r2", Cmd: "job_update", JobToken: "t1",
		Params: protocol.JobUpdateParams{State: "running", Message: "step 1", Progress: 50}}, "nginx", "nginx.provision", "nginx")
	s.Complete("r2", protocol.Response{Status: "ok"})
	s.Open(protocol.Request{ID: "r3", Cmd: "job_update", JobToken: "t1",
		Params: protocol.JobUpdateParams{State: "done", Output: json.RawMessage(`{"done":true}`)}}, "nginx", "nginx.provision", "nginx")
	s.Complete("r3", protocol.Response{Status: "ok"})

	j, _ := s.Job("t1")
	if j.State != "done" || j.Progress != 50 || len(j.Messages) != 1 {
		t.Fatalf("Job = %+v; want done/progress 50/1 message", j)
	}
	if j.FinishedAt.IsZero() {
		t.Error("FinishedAt not stamped on terminal update")
	}
}

// TestJob_ErrorResponseFails proves an error terminal response folds to failed
// and carries the error detail.
func TestJob_ErrorResponseFails(t *testing.T) {
	s := New()
	invoke(s, "r1", "t1", "nginx", "nginx.reload")
	s.Complete("r1", protocol.Response{Status: "error",
		Error: &protocol.ErrorDetail{Code: protocol.CodeCallableFailed, Message: "boom"}})

	j, _ := s.Job("t1")
	if j.State != "failed" || j.Error == nil || j.Error.Code != protocol.CodeCallableFailed {
		t.Fatalf("Job = %+v; want failed with CALLABLE_FAILED", j)
	}
}

// TestJobs_FilterByModule proves Jobs enumerates one job per token and honours
// the owning-module filter.
func TestJobs_FilterByModule(t *testing.T) {
	s := New()
	invoke(s, "r1", "t1", "nginx", "nginx.reload")
	invoke(s, "r2", "t2", "certbot", "certbot.cert.list")

	if all := s.Jobs(""); len(all) != 2 {
		t.Fatalf("Jobs(all) = %d; want 2", len(all))
	}
	own := s.Jobs("nginx")
	if len(own) != 1 || own[0].Module != "nginx" {
		t.Fatalf("Jobs(nginx) = %v; want one nginx job", own)
	}
	if _, ok := s.Job("missing"); ok {
		t.Error("Job returned ok for an unknown token")
	}
}

// TestTrimPreservesOpenRecords proves eviction (triggered once the store grows
// past twice the cap) drops the oldest completed exchanges yet never evicts an
// open (non-terminal) record, no matter how old.
func TestTrimPreservesOpenRecords(t *testing.T) {
	s := New()
	// An open record up front — the oldest of all, but non-terminal.
	s.Open(protocol.Request{ID: "open", Cmd: "invoke"}, "nginx", "nginx.reload", "")
	// Fill past twice the cap with completed records to trigger trimming.
	for i := 0; i < maxRecords*2; i++ {
		id := "c" + strconv.Itoa(i)
		s.Open(protocol.Request{ID: id, Cmd: "invoke"}, "nginx", "nginx.reload", "")
		s.Complete(id, protocol.Response{Status: "ok"})
	}
	if n := len(s.List(0)); n > maxRecords+1 {
		t.Fatalf("record count after trim = %d; want <= %d", n, maxRecords+1)
	}
	if _, ok := s.Lookup("open"); !ok {
		t.Error("open (non-terminal) record was evicted")
	}
}

// ids extracts record ids for assertion messages.
func ids(recs []Record) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.ID()
	}
	return out
}
