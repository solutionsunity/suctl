// SPDX-License-Identifier: Apache-2.0

package surface

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/solutionsunity/suctl/sdk/protocol"
)

// fakeBroker is a scripted Broker: it returns a canned response (and error) and
// records the last envelope it received, so tests can assert both the decode and
// the minted envelope shape.
type fakeBroker struct {
	resp *protocol.Response
	err  error
	last *protocol.Request
}

func (f *fakeBroker) InvokeEnvelope(req *protocol.Request) (*protocol.Response, error) {
	f.last = req
	return f.resp, f.err
}

// syncResponse wraps a bare output as a module's synchronous invoke envelope:
// {"name":..., "output":{...}} inside an ok Response.
func syncResponse(t *testing.T, name string, output interface{}) *protocol.Response {
	t.Helper()
	raw, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("marshal output: %v", err)
	}
	ir, err := json.Marshal(protocol.InvokeResponse{Name: name, Output: raw})
	if err != nil {
		t.Fatalf("marshal invoke response: %v", err)
	}
	return &protocol.Response{Status: "ok", Result: ir}
}

func sampleSurveyOutput() map[string]interface{} {
	return map[string]interface{}{
		"total":          2,
		"status_summary": "2 domains",
		"subjects": []map[string]interface{}{
			{"id": "a", "name": "alpha"},
			{"id": "b", "name": "beta"},
		},
		"actions": []map[string]interface{}{
			{"capability": "nginx.reload", "label": "Reload"},
		},
	}
}

func TestLoadSurveySyncDecodesAndMintsEnvelope(t *testing.T) {
	fb := &fakeBroker{resp: syncResponse(t, "nginx.domain.survey", sampleSurveyOutput())}
	o := New(fb)

	load, err := o.LoadSurvey("nginx.domain.survey", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if load.Accepted {
		t.Fatalf("sync survey should not be accepted async: %+v", load)
	}
	if load.Token == "" {
		t.Fatalf("expected a minted job_token, got empty")
	}
	if load.Survey.Total != 2 || len(load.Survey.Subjects) != 2 || load.Survey.StatusSummary != "2 domains" {
		t.Fatalf("survey decode: %+v", load.Survey)
	}
	if len(load.Actions) != 1 || load.Actions[0].Capability != "nginx.reload" {
		t.Fatalf("actions decode: %+v", load.Actions)
	}
	// The orchestrator is the initiator: it mints a complete invoke envelope,
	// and the minted token is the one returned for correlation.
	if fb.last == nil || fb.last.Cmd != "invoke" || fb.last.ID == "" || fb.last.JobToken != load.Token {
		t.Fatalf("minted envelope: %+v (token %q)", fb.last, load.Token)
	}
}

func TestLoadSurveyAsyncAckYieldsEmpty(t *testing.T) {
	ir, _ := json.Marshal(protocol.InvokeResponse{Name: "cap", Accepted: true})
	fb := &fakeBroker{resp: &protocol.Response{Status: "ok", Result: ir}}
	o := New(fb)

	load, err := o.LoadSurvey("cap", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !load.Accepted {
		t.Fatalf("async accept ack should set Accepted, got %+v", load)
	}
	if load.Token == "" {
		t.Fatalf("expected a minted job_token on accept, got empty")
	}
	if load.Survey.Subjects != nil || load.Actions != nil {
		t.Fatalf("accept ack should carry no inline result, got %+v", load)
	}
}

func TestLoadSurveyPropagatesError(t *testing.T) {
	wantErr := &protocol.ErrorDetail{Code: "X", Message: "boom"}
	o := New(&fakeBroker{err: wantErr})

	if _, err := o.LoadSurvey("cap", nil); err != wantErr {
		t.Fatalf("expected wrapped error, got %v", err)
	}
}

// TestDecodeSurveyOutputParity is the heart of "sync is the degenerate case of
// async": the identical payload, whether peeled from a sync invoke response or
// taken from a pushed terminal job_update, decodes to the identical result.
func TestDecodeSurveyOutputParity(t *testing.T) {
	out, _ := json.Marshal(sampleSurveyOutput())

	syncResult := syncResponse(t, "cap", sampleSurveyOutput()).Result
	syncSR, syncActions := DecodeSurveyOutput(unwrapInvokeOutput(syncResult))

	pushed := protocol.JobUpdateParams{State: "done", Output: out}
	asyncSR, asyncActions := DecodeSurveyOutput(pushed.Output)

	if !reflect.DeepEqual(syncSR, asyncSR) {
		t.Fatalf("survey mismatch: %+v vs %+v", syncSR, asyncSR)
	}
	if !reflect.DeepEqual(syncActions, asyncActions) {
		t.Fatalf("actions mismatch: %+v vs %+v", syncActions, asyncActions)
	}
}

func TestLoadFocusDecodesAndParity(t *testing.T) {
	focus := map[string]interface{}{
		"id": "a", "name": "alpha",
		"sections": []map[string]interface{}{
			{"title": "Info", "fields": []map[string]interface{}{{"label": "state", "value": "up"}}},
		},
	}
	fb := &fakeBroker{resp: syncResponse(t, "nginx.domain.focus", focus)}
	o := New(fb)

	fr, err := o.LoadFocus("nginx.domain.focus", "a")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fr.ID != "a" || fr.Name != "alpha" || len(fr.Sections) != 1 {
		t.Fatalf("focus decode: %+v", fr)
	}

	out, _ := json.Marshal(focus)
	if pushed := DecodeFocusOutput(out); !reflect.DeepEqual(fr, pushed) {
		t.Fatalf("focus parity mismatch: %+v vs %+v", fr, pushed)
	}
}

func TestDeliverRoutesToUpdatesAndNeverBlocks(t *testing.T) {
	o := New(&fakeBroker{})

	o.Deliver("tok", protocol.JobUpdateParams{State: "running", Progress: 10})
	select {
	case u := <-o.Updates():
		if u.Token != "tok" || u.Params.State != "running" || u.Params.Progress != 10 {
			t.Fatalf("delivered update: %+v", u)
		}
	default:
		t.Fatal("expected a delivered update")
	}

	// A full buffer drops rather than stalling the broker's read loop.
	for i := 0; i < updatesBuffer+8; i++ {
		o.Deliver("flood", protocol.JobUpdateParams{State: "running"})
	}
}

func sampleCellOutput() map[string]interface{} {
	return map[string]interface{}{
		"rows": map[string]interface{}{
			"prod": map[string]interface{}{
				"columns": map[string]interface{}{
					"modules": map[string]interface{}{"value": "123/866"},
					"users":   map[string]interface{}{"value": "5/12"},
					"status":  map[string]interface{}{"value": "current", "color": "ok"},
				},
			},
		},
	}
}

func TestFillCellsSyncDecodesAndMintsEnvelope(t *testing.T) {
	fb := &fakeBroker{resp: syncResponse(t, "odoo.database.overview", sampleCellOutput())}
	o := New(fb)

	load, err := o.FillCells("odoo.database.overview", map[string]interface{}{"scope": "prod"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if load.Accepted {
		t.Fatalf("sync fill should not be accepted async: %+v", load)
	}
	if load.Token == "" {
		t.Fatalf("expected a minted job_token, got empty")
	}
	row := load.Rows["prod"]
	if len(load.Rows) != 1 || len(row) != 3 || row["modules"].Value != "123/866" || row["status"].Color != "ok" {
		t.Fatalf("cell decode: %+v", load.Rows)
	}
	// args ride through verbatim, and the minted token is the one returned for
	// correlation.
	if fb.last == nil || fb.last.Cmd != "invoke" || fb.last.JobToken != load.Token {
		t.Fatalf("minted envelope: %+v (token %q)", fb.last, load.Token)
	}
	params, _ := fb.last.Params.(map[string]interface{})
	args, _ := params["args"].(map[string]interface{})
	if args == nil || args["scope"] != "prod" {
		t.Fatalf("expected args to ride through {scope: prod}, got %+v", params["args"])
	}
}

func TestFillCellsAsyncAckYieldsEmpty(t *testing.T) {
	ir, _ := json.Marshal(protocol.InvokeResponse{Name: "cap", Accepted: true})
	fb := &fakeBroker{resp: &protocol.Response{Status: "ok", Result: ir}}
	o := New(fb)

	load, err := o.FillCells("cap", map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !load.Accepted {
		t.Fatalf("async accept ack should set Accepted, got %+v", load)
	}
	if load.Token == "" {
		t.Fatalf("expected a minted job_token on accept, got empty")
	}
	if load.Rows != nil {
		t.Fatalf("accept ack should carry no inline rows, got %+v", load.Rows)
	}
}

func TestFillCellsPropagatesError(t *testing.T) {
	wantErr := &protocol.ErrorDetail{Code: "X", Message: "boom"}
	o := New(&fakeBroker{err: wantErr})

	if _, err := o.FillCells("cap", nil); err != wantErr {
		t.Fatalf("expected wrapped error, got %v", err)
	}
}

// TestDecodeCellOutputParity mirrors the survey parity test: the identical
// row-keyed payload decodes the same whether peeled from a sync invoke response
// or taken from a pushed terminal job_update.
func TestDecodeCellOutputParity(t *testing.T) {
	out, _ := json.Marshal(sampleCellOutput())

	syncResult := syncResponse(t, "cap", sampleCellOutput()).Result
	syncCols := DecodeCellOutput(unwrapInvokeOutput(syncResult))

	pushed := protocol.JobUpdateParams{State: "done", Output: out}
	asyncCols := DecodeCellOutput(pushed.Output)

	if !reflect.DeepEqual(syncCols, asyncCols) {
		t.Fatalf("cell parity mismatch: %+v vs %+v", syncCols, asyncCols)
	}
}
