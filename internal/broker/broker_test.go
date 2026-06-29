// SPDX-License-Identifier: Apache-2.0

package broker_test

import (
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/solutionsunity/suctl/internal/broker"
	"github.com/solutionsunity/suctl/internal/messages"
	"github.com/solutionsunity/suctl/internal/module"
	"github.com/solutionsunity/suctl/sdk/manifest"
	"github.com/solutionsunity/suctl/sdk/protocol"
)

// --------------------------------------------------------------------------
// Test helpers
// --------------------------------------------------------------------------

// The broker owns no listening socket: it is reached in-process via Invoke (the
// originating face, CallerIdentity{}) or over a registered wire via
// RegisterWire (a module, attributed by possession). Routing and the
// requires-gate resolve from the modules store, so tests stage capabilities as
// in-process handler records and caller-declared requires on the store.

// newBroker builds a broker over store wired with a fresh messages store. The
// broker is its own queue manager: admission reads each footprint on demand from
// the store, so a module with no requires has footprint {itself} and two
// originating invokes of one module serialize.
func newBroker(store *module.Store) *broker.Broker {
	return broker.New(store, messages.New(), 0)
}

// invoke builds a complete originating envelope (id + job_token) the way the
// surface orchestrator does and hands it to the broker's in-process entry. The
// broker no longer mints identity, so the test is the initiator here.
func invoke(b *broker.Broker, jobToken, capName string, args interface{}) (*protocol.Response, error) {
	req := &protocol.Request{
		V:        protocol.Version,
		ID:       protocol.NewID(),
		TsSent:   protocol.Timestamp(),
		Cmd:      "invoke",
		JobToken: jobToken,
		Params:   map[string]interface{}{"name": capName, "args": args},
	}
	return b.InvokeEnvelope(req)
}

// handlerModule returns a store with one virtual module (moduleName) providing
// capName via the in-process handler h, declared with the given async mode so
// Resolve reports it.
func handlerModule(moduleName, capName string, async bool, h module.InProcessHandler) *module.Store {
	store := module.NewStore()
	store.RegisterHandler(moduleName, &manifest.Manifest{
		Capabilities: []manifest.Capability{{Name: capName, Async: async}},
	}, capName, h)
	return store
}

// okHandler returns a handler that replies ok, echoing the request's job token
// and carrying the given InvokeResponse as its result.
func okHandler(ir protocol.InvokeResponse) module.InProcessHandler {
	return func(req *protocol.Request, _ module.CallerIdentity) *protocol.Response {
		out, _ := json.Marshal(ir)
		return &protocol.Response{V: protocol.Version, Status: "ok", JobToken: req.JobToken, Result: out}
	}
}

// wire registers a persistent module wire on b and returns a roundtrip func that
// sends one request as that module (possession identity) and reads the response.
// Reusing the same wire across calls proves it is persistent and serially
// framed. net.Pipe stands in for the inherited socketpair; the module end is
// closed via t.Cleanup.
func wire(t *testing.T, b *broker.Broker, moduleName string) func(*protocol.Request) *protocol.Response {
	t.Helper()
	coreEnd, moduleEnd := net.Pipe()
	t.Cleanup(func() { moduleEnd.Close() })
	b.RegisterWire(moduleName, coreEnd)
	enc := json.NewEncoder(moduleEnd)
	dec := json.NewDecoder(moduleEnd)
	return func(req *protocol.Request) *protocol.Response {
		t.Helper()
		if err := enc.Encode(req); err != nil {
			t.Fatalf("encode on %s wire: %v", moduleName, err)
		}
		var resp protocol.Response
		if err := dec.Decode(&resp); err != nil {
			t.Fatalf("decode on %s wire: %v", moduleName, err)
		}
		return &resp
	}
}

// invokeReq builds an invoke request for capName under token.
func invokeReq(capName, token string) *protocol.Request {
	return &protocol.Request{
		V:        protocol.Version,
		Cmd:      "invoke",
		JobToken: token,
		Params:   map[string]interface{}{"name": capName, "args": map[string]interface{}{}},
	}
}

// --------------------------------------------------------------------------
// Broker routing
// --------------------------------------------------------------------------

func TestBroker_RoutesInvokeToHandler(t *testing.T) {
	store := handlerModule("nginx", "nginx.domain.create", false,
		okHandler(protocol.InvokeResponse{Name: "nginx.domain.create"}))
	b := newBroker(store)

	resp, err := invoke(b, "tok-1", "nginx.domain.create", nil)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("status = %q; want ok", resp.Status)
	}
}

func TestBroker_CapabilityNotActive(t *testing.T) {
	b := newBroker(module.NewStore()) // empty — nothing provides it
	_, err := invoke(b, "tok-2", "nginx.domain.create", nil)

	var det *protocol.ErrorDetail
	if !errors.As(err, &det) {
		t.Fatalf("err = %v; want a *protocol.ErrorDetail", err)
	}
	if det.Code != protocol.CodeCapabilityNotActive {
		t.Errorf("error code = %s; want %s", det.Code, protocol.CodeCapabilityNotActive)
	}
}

func TestBroker_UnknownCommand(t *testing.T) {
	b := newBroker(module.NewStore())
	send := wire(t, b, "nginx")

	resp := send(&protocol.Request{V: protocol.Version, Cmd: "health", Params: map[string]interface{}{}})
	if resp.Status != "error" {
		t.Fatalf("status = %q; want error", resp.Status)
	}
	if resp.Error == nil || resp.Error.Code != protocol.CodeUnknownCommand {
		t.Errorf("error code = %v; want %s", resp.Error, protocol.CodeUnknownCommand)
	}
}

func TestBroker_MissingCapabilityName(t *testing.T) {
	b := newBroker(module.NewStore())
	send := wire(t, b, "nginx")

	resp := send(&protocol.Request{V: protocol.Version, Cmd: "invoke", Params: map[string]interface{}{}})
	if resp.Status != "error" {
		t.Fatalf("status = %q; want error", resp.Status)
	}
	if resp.Error == nil || resp.Error.Code != protocol.CodeInvalidParams {
		t.Errorf("error code = %v; want %s", resp.Error, protocol.CodeInvalidParams)
	}
}

func TestBroker_JobUpdateAndStatus(t *testing.T) {
	resB, _ := json.Marshal(protocol.InvokeResponse{Name: "nginx.provision", Accepted: true})
	store := handlerModule("nginx", "nginx.provision", true,
		func(req *protocol.Request, _ module.CallerIdentity) *protocol.Response {
			return &protocol.Response{V: protocol.Version, Status: "ok", JobToken: req.JobToken, Result: resB}
		})
	b := newBroker(store)

	// Originating invoke registers the job; the cap is async and the handler
	// accepted it, so core leaves the job running before any job_update arrives.
	resp, err := invoke(b, "tok-async", "nginx.provision", nil)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if resp.Status != "ok" || resp.JobToken != "tok-async" {
		t.Fatalf("invoke failed: status=%s", resp.Status)
	}

	// job_update and job_status arrive over the module's wire.
	send := wire(t, b, "nginx")

	updateResp := send(&protocol.Request{
		V: protocol.Version, Cmd: "job_update", JobToken: "tok-async",
		Params: protocol.JobUpdateParams{State: "running", Message: "working hard", Progress: 50},
	})
	if updateResp.Status != "ok" {
		t.Errorf("job_update status = %q; want ok", updateResp.Status)
	}

	statusResp := send(&protocol.Request{V: protocol.Version, Cmd: "job_status", JobToken: "tok-async"})
	if statusResp.Status != "ok" {
		t.Errorf("job_status status = %q; want ok", statusResp.Status)
	}
	var js protocol.JobStatusResponse
	if err := json.Unmarshal(statusResp.Result, &js); err != nil {
		t.Fatalf("unmarshal status result: %v", err)
	}
	if js.State != "running" {
		t.Errorf("js.State = %q; want running", js.State)
	}
}

func TestBroker_SyncInvokeRegistered(t *testing.T) {
	store := handlerModule("nginx", "nginx.reload", false,
		okHandler(protocol.InvokeResponse{Name: "nginx.reload", Output: json.RawMessage(`{"reloaded":true}`)}))
	b := newBroker(store)

	if _, err := invoke(b, "tok-sync", "nginx.reload", nil); err != nil {
		t.Fatalf("invoke: %v", err)
	}

	// The sync return is the terminal result — the job folds to done.
	send := wire(t, b, "nginx")
	statusResp := send(&protocol.Request{V: protocol.Version, Cmd: "job_status", JobToken: "tok-sync"})
	if statusResp.Status != "ok" {
		t.Errorf("job_status status = %q; want ok", statusResp.Status)
	}
	var js protocol.JobStatusResponse
	if err := json.Unmarshal(statusResp.Result, &js); err != nil {
		t.Fatalf("unmarshal status result: %v", err)
	}
	if js.State != "done" {
		t.Errorf("js.State = %q; want done", js.State)
	}
	if string(js.Output) != `{"reloaded":true}` {
		t.Errorf("js.Output = %s; want {\"reloaded\":true}", string(js.Output))
	}
}

// TestBroker_AsyncRejected verifies the async truth-reading: an async cap whose
// module returns accepted:false is the module declining the job. The broker
// converts that decline into a terminal error returned to the caller, and the
// recorded exchange folds to a failed job (not one left running).
func TestBroker_AsyncRejected(t *testing.T) {
	resB, _ := json.Marshal(protocol.InvokeResponse{Name: "nginx.provision", Accepted: false})
	store := handlerModule("nginx", "nginx.provision", true,
		func(req *protocol.Request, _ module.CallerIdentity) *protocol.Response {
			return &protocol.Response{V: protocol.Version, Status: "ok", JobToken: req.JobToken, Result: resB}
		})
	b := newBroker(store)

	// The decline surfaces to the caller as a terminal CALLABLE_FAILED error.
	_, err := invoke(b, "tok-reject", "nginx.provision", nil)
	var det *protocol.ErrorDetail
	if !errors.As(err, &det) {
		t.Fatalf("err = %v; want a *protocol.ErrorDetail", err)
	}
	if det.Code != protocol.CodeCallableFailed {
		t.Errorf("error code = %s; want %s", det.Code, protocol.CodeCallableFailed)
	}

	// The recorded terminal exchange folds the job to failed.
	send := wire(t, b, "nginx")
	statusResp := send(&protocol.Request{V: protocol.Version, Cmd: "job_status", JobToken: "tok-reject"})
	var js protocol.JobStatusResponse
	if err := json.Unmarshal(statusResp.Result, &js); err != nil {
		t.Fatalf("unmarshal status result: %v", err)
	}
	if js.State != "failed" {
		t.Errorf("js.State = %q; want failed", js.State)
	}
}

// --------------------------------------------------------------------------
// Inherited broker wire (possession identity)
// --------------------------------------------------------------------------

// TestBroker_ServeWire_PossessionIdentity proves the core-managed path: a
// request arriving on a module's inherited wire (RegisterWire) is attributed to
// that module by possession alone — no SO_PEERCRED, no identity-registry entry.
// The in-process handler observes caller.Module == the wire's module name. A
// second request on the same wire proves it is persistent and serially framed
// (one response fully written before the next request is read).
func TestBroker_ServeWire_PossessionIdentity(t *testing.T) {
	gotCaller := make(chan module.CallerIdentity, 1)
	store := handlerModule("nginx", "nginx.reload", false,
		func(req *protocol.Request, caller module.CallerIdentity) *protocol.Response {
			gotCaller <- caller
			out, _ := json.Marshal(protocol.InvokeResponse{Name: "nginx.reload", Output: json.RawMessage(`{"reloaded":true}`)})
			return &protocol.Response{V: protocol.Version, Status: "ok", JobToken: req.JobToken, Result: out}
		})
	b := newBroker(store)

	// The wire is an address-less socketpair end only "nginx" could hold, so the
	// broker attributes every request on it to "nginx".
	send := wire(t, b, "nginx")

	resp := send(invokeReq("nginx.reload", "tok-wire-1"))
	if resp.Status != "ok" {
		t.Fatalf("status = %q; want ok", resp.Status)
	}
	if caller := <-gotCaller; caller.Module != "nginx" {
		t.Errorf("caller.Module = %q; want nginx (possession identity)", caller.Module)
	}

	// Same wire, second request — proves persistence and sequential framing.
	resp = send(invokeReq("nginx.reload", "tok-wire-2"))
	if resp.Status != "ok" {
		t.Fatalf("second invoke status = %q; want ok", resp.Status)
	}
	if caller := <-gotCaller; caller.Module != "nginx" {
		t.Errorf("second caller.Module = %q; want nginx", caller.Module)
	}
}

// TestBroker_RejectsUndeclaredCrossModuleCall proves the requires-gate: a
// cross-module hop (a request on nginx's wire, attributed to "nginx") to a
// certbot capability is rejected with CAPABILITY_NOT_DECLARED unless nginx has
// declared that capability in requires.capabilities. Once declared, the same
// hop succeeds. The declared set is read from the caller module's record in the
// store (store.Allows), so the test toggles it on the record itself.
//
// This test is the canonical pin for the D60 undeclared-hop-rejection invariant.
// It cannot live in the runtime BIST: the originating face (caller.Module == "")
// is exempt from the requires-gate, so the BIST cannot trigger
// CAPABILITY_NOT_DECLARED without forging a module identity it does not have.
// RegisterWire provides a genuinely attributed caller — hence the pin belongs here.
func TestBroker_RejectsUndeclaredCrossModuleCall(t *testing.T) {
	store := handlerModule("certbot", "certbot.cert.list", false,
		okHandler(protocol.InvokeResponse{Name: "certbot.cert.list", Output: json.RawMessage(`{"certs":[]}`)}))
	// nginx is the caller; its record's requires.capabilities is the requires-gate.
	nginx := module.NewRecord(module.StateActive, &manifest.Manifest{})
	store.Put("nginx", nginx)

	b := newBroker(store)
	send := wire(t, b, "nginx")

	// Undeclared — rejected.
	resp := send(invokeReq("certbot.cert.list", "tok-undeclared"))
	if resp.Status != "error" {
		t.Fatalf("undeclared cross-module call: status = %q; want error", resp.Status)
	}
	if resp.Error == nil || resp.Error.Code != protocol.CodeCapabilityNotDeclared {
		t.Fatalf("undeclared call error = %+v; want code %s", resp.Error, protocol.CodeCapabilityNotDeclared)
	}

	// Declared — admitted. The roundtrip above has returned, so serveWire is
	// blocked on the next read and this mutation cannot race it.
	nginx.Manifest.Requires.Capabilities = []string{"certbot.cert.list"}
	resp = send(invokeReq("certbot.cert.list", "tok-declared"))
	if resp.Status != "ok" {
		t.Fatalf("declared cross-module call: status = %q (err=%+v); want ok", resp.Status, resp.Error)
	}
}

// --------------------------------------------------------------------------
// Gate admission
// --------------------------------------------------------------------------

// TestBroker_Admission_SerializesSameModule proves the broker reserves a job's
// footprint through the gate before dispatch: two originating invokes of the
// same module run one-at-a-time, not concurrently. Job A enters its handler and
// holds the {nginx} reservation; job B is admitted only after A returns and the
// reservation is released.
//
// This test is the canonical pin for the footprint-serialization invariant (R5).
// It cannot live in the runtime BIST: the assertion needs one job to hold a
// module's reservation while a second job provably waits — which requires a
// controllable blocking handler. A live module cannot safely provide that without
// executing real side-effecting capabilities. RegisterInProcess does it faithfully.
func TestBroker_Admission_SerializesSameModule(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	store := handlerModule("nginx", "nginx.reload", false,
		func(req *protocol.Request, _ module.CallerIdentity) *protocol.Response {
			entered <- struct{}{}
			<-release
			out, _ := json.Marshal(protocol.InvokeResponse{Name: "nginx.reload", Output: json.RawMessage(`{"ok":true}`)})
			return &protocol.Response{V: protocol.Version, Status: "ok", JobToken: req.JobToken, Result: out}
		})
	b := newBroker(store)

	// fire is a t.Fatalf-free invoke sender, safe to call from a goroutine. The
	// in-process face is the originating caller (caller.Module == ""), so
	// admission applies.
	fire := func(token string, done chan<- *protocol.Response) {
		resp, _ := invoke(b, token, "nginx.reload", nil)
		done <- resp
	}

	doneA := make(chan *protocol.Response, 1)
	go fire("tok-A", doneA)

	// Job A is now running and holds the {nginx} reservation.
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("job A never entered handler")
	}

	doneB := make(chan *protocol.Response, 1)
	go fire("tok-B", doneB)

	// Job B must NOT enter while A holds nginx — admission serializes them.
	select {
	case <-entered:
		t.Fatal("job B entered handler while job A held the nginx reservation")
	case <-time.After(250 * time.Millisecond):
	}

	// Release both: A returns and frees nginx, then B is admitted and runs.
	close(release)

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("job B never entered handler after A released")
	}

	for _, d := range []chan *protocol.Response{doneA, doneB} {
		select {
		case resp := <-d:
			if resp == nil || resp.Status != "ok" {
				t.Fatalf("invoke status = %v; want ok", resp)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("invoke never completed")
		}
	}
}
