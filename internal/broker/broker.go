// SPDX-License-Identifier: Apache-2.0

// Package broker implements the orchestrator's routing core.
//
// Core validates that the target capability is in the active surface, routes
// the envelope to the target module's socket, and returns the response
// verbatim. The calling module never knows the target module's socket path or
// name.
//
// There are two callers. Core-managed modules reach the broker over their
// inherited socketpair wire — the wire mux dispatches their inbound requests to
// the broker, and possession of that wire is the module's identity.
// The face (REPL today) is in-process: its surface orchestrator mints the
// originating id and job_token, builds a complete envelope, and calls
// InvokeEnvelope with the empty CallerIdentity. The broker originates nothing.
// There is no shared listening socket.
//
// The broker uses the same JSON envelope format as the module protocol.
// It accepts only "invoke" commands — other command verbs are rejected
// with UNKNOWN_COMMAND.
//
// Cross-module capability calls route through the orchestrator.
// DNS resolution rejected.
package broker

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/solutionsunity/suctl/internal/messages"
	"github.com/solutionsunity/suctl/internal/module"
	"github.com/solutionsunity/suctl/internal/wire"
	"github.com/solutionsunity/suctl/sdk/protocol"
)

// DefaultAdmitTimeout backstops queue admission so an unsatisfiable queue entry
// can never block an invoke forever. It is deliberately generous — admission is
// expected to succeed once the contended footprint frees; this only fires on a
// bug or a permanently busy footprint. It is the fallback when New is given a
// non-positive admitTimeout; core overrides it from suctl.conf (Gate D).
const DefaultAdmitTimeout = 5 * time.Minute

// --------------------------------------------------------------------------
// Broker server
// --------------------------------------------------------------------------

// Broker routes invoke envelopes to target modules and owns the queue manager
// (queue.go). It owns no listening socket: modules reach it over their inherited
// wire mux and the in-process face calls Invoke directly. Routing and the
// requires-gate are resolved from the modules store; admission is resolved from
// the messages store (the queue) through the pure gate policy.
type Broker struct {
	store        *module.Store
	msgs         *messages.Store
	admitTimeout time.Duration

	// admitMu guards the queue manager's promotion step and is the Locker behind
	// cond. A parked caller waits on cond until advance() promotes its job (stamps
	// the Started fact) and broadcasts. The footprint of a job is freed implicitly
	// when it becomes terminal in the store, so there is no reservation to release.
	admitMu sync.Mutex
	cond    *sync.Cond

	// surfaceSink, when set, receives every job_update on a surface-originated
	// job after it is recorded. The broker is the message orchestrator: a
	// job_update is a message it records (truth) then routes to its live
	// destination — the surface that originated the job. nil until the face
	// registers one (RegisterSurfaceSink); it stays neutral (no UI types). A
	// module- or system-originated job is never routed here.
	surfaceSink func(token string, params protocol.JobUpdateParams)
}

// RegisterSurfaceSink installs the destination for job_update messages on
// surface-originated jobs. It is wired once at startup, before any module wire
// exists, so no module read loop races the assignment. The sink must not block:
// it runs on the reporting module's wire read loop.
func (b *Broker) RegisterSurfaceSink(sink func(token string, params protocol.JobUpdateParams)) {
	b.surfaceSink = sink
}

// surfaceOriginated reports whether the job named by token was originated by the
// in-process face: its originating invoke — the first invoke record on the token
// — carries the empty caller. A module-originated hop or a system-internal job
// carries a non-empty caller and is never routed to the surface.
func (b *Broker) surfaceOriginated(token string) bool {
	for _, r := range b.msgs.ByToken(token) {
		if r.Request.Cmd == "invoke" && r.Caller == "" {
			return true
		}
	}
	return false
}

// New creates a Broker over the given modules store. msgs is the messages store;
// the broker is its sole runtime writer — it opens a record when a request
// arrives and completes it when the response returns, and every job state
// (queued/running/done/failed) is derived from those records plus the Started
// fact, never folded here. The broker is also the queue manager: an originating
// invoke enters the queue (Open) and parks until promoted. admitTimeout backstops
// admission; a non-positive value falls back to DefaultAdmitTimeout.
func New(store *module.Store, msgs *messages.Store, admitTimeout time.Duration) *Broker {
	if admitTimeout <= 0 {
		admitTimeout = DefaultAdmitTimeout
	}
	b := &Broker{
		store:        store,
		msgs:         msgs,
		admitTimeout: admitTimeout,
	}
	b.cond = sync.NewCond(&b.admitMu)
	return b
}

// InvokeEnvelope is the in-process face entry point. The face's surface
// orchestrator is the initiator: it mints the request id and job_token and
// hands the broker a complete envelope, so the broker originates nothing — it
// records and routes what it receives, exactly as it does for an envelope
// arriving over the wire. The envelope carries the empty CallerIdentity (the
// originating face). It mirrors protocol.Client.Invoke's error contract: on a
// protocol error it returns a wrapped *protocol.ErrorDetail; transport-less, it
// never returns a Go transport error.
func (b *Broker) InvokeEnvelope(req *protocol.Request) (*protocol.Response, error) {
	resp := b.dispatch(req, CallerIdentity{})
	if resp.Status == "error" && resp.Error != nil {
		name := ""
		if p, ok := req.Params.(map[string]interface{}); ok {
			name, _ = p["name"].(string)
		}
		return nil, fmt.Errorf("invoke %s: %w", name, resp.Error)
	}
	return resp, nil
}

// RegisterWire binds a core-managed module's inherited broker wire and returns
// the duplex mux that owns it. The mux's read loop dispatches every inbound
// module->core request to the broker, attributed to moduleName with no
// credential check — the wire is an address-less socketpair end only that module
// could hold (possession = identity). The same mux is the transport for
// core->module calls (handshake, health, hooks, forwards). Called from the
// supervisor's per-launch OnChannel callback, so it returns a fresh mux on every
// restart; the prior mux's read loop returns on its own when that conn closes.
func (b *Broker) RegisterWire(moduleName string, conn io.ReadWriteCloser) *wire.Mux {
	caller := CallerIdentity{Module: moduleName}
	return wire.New(conn, func(req *protocol.Request) *protocol.Response {
		return b.dispatch(req, caller)
	})
}

// dispatch routes one decoded request to its handler and returns the response.
// Shared by the inherited wire (serveWire) and the in-process face (Invoke) so
// both apply identical routing — only the caller-identity source differs.
func (b *Broker) dispatch(req *protocol.Request, caller CallerIdentity) *protocol.Response {
	switch req.Cmd {
	case "invoke":
		return b.handleInvoke(req, caller)
	case "job_update":
		return b.handleJobUpdate(req, caller)
	case "job_status":
		return b.handleJobStatus(req)
	default:
		return errorResponse(req.ID, req.JobToken, protocol.CodeUnknownCommand,
			fmt.Sprintf("broker: command %q not supported", req.Cmd))
	}
}

func (b *Broker) handleInvoke(req *protocol.Request, caller CallerIdentity) *protocol.Response {
	// Extract the capability name from params.
	params, ok := req.Params.(map[string]interface{})
	if !ok {
		return errorResponse(req.ID, req.JobToken, protocol.CodeInvalidParams,
			"broker: params must be an object with a \"name\" field")
	}
	capName, _ := params["name"].(string)
	if capName == "" {
		return errorResponse(req.ID, req.JobToken, protocol.CodeInvalidParams,
			"broker: invoke params.name is required")
	}

	route, found := b.store.Resolve(capName)
	if !found {
		return errorResponse(req.ID, req.JobToken, protocol.CodeCapabilityNotActive,
			fmt.Sprintf("%s is not in the active capability surface", capName))
	}

	// Requires-gate. A cross-module hop (caller.Module != "") may invoke
	// only a capability its calling module declared in requires.capabilities. A
	// module's own capabilities (route.Module == caller.Module) and the
	// originating face (caller.Module == "") are exempt. This is the runtime
	// counterpart to the activation-time requirement check: activation decides a
	// module CAN run; this decides which capabilities it MAY reach at call time.
	if caller.Module != "" && route.Module != caller.Module &&
		!b.store.Allows(caller.Module, capName) {
		return errorResponse(req.ID, req.JobToken, protocol.CodeCapabilityNotDeclared,
			fmt.Sprintf("module %q has not declared %q in requires.capabilities", caller.Module, capName))
	}

	// Every invoke must carry its initiator's job_token: the face's surface
	// orchestrator mints it for originating work; a module propagates its
	// originator's. The broker originates nothing — a token-less invoke names a
	// job no bucket tracks and no gate admits, so reject it rather than mint one.
	if req.JobToken == "" {
		who := "the face"
		if caller.Module != "" {
			who = fmt.Sprintf("module %q", caller.Module)
		}
		return errorResponse(req.ID, "", protocol.CodeInvalidParams,
			fmt.Sprintf("broker: %s sent an invoke without a job_token", who))
	}

	// Originator vs. hop. Only an ORIGINATING invoke (caller.Module == "") enters
	// the admission queue. A cross-module hop carries the originator's propagated
	// token and runs re-entrantly inside the footprint that job already holds, so
	// admitting it again would deadlock on the module its originator holds. Both
	// are still recorded as exchanges — derivation reads the originator (the first
	// invoke record on the token) for job state.
	isOriginator := caller.Module == ""

	// Registered first → runs last (defers are LIFO): after the terminal response
	// is recorded below, advance() reconciles the queue so the footprint this job
	// frees promotes the next waiter. For an accepted async job the response is not
	// terminal, so the job stays running and advance() promotes nothing — harmless.
	if isOriginator {
		defer b.advance()
	}

	// Open the exchange and guarantee it is completed on every return path. One
	// record per request id holds both envelopes: a module route forwards this same
	// id outward and the returned response completes this record — routing is a
	// field, not a sibling row. The originating invoke is the first record to carry
	// this job_token, so messages.Store.Job reads its attribution and timing as the
	// job's.
	b.msgs.Open(*req, route.Module, capName, caller.Module)
	var resp *protocol.Response
	defer func() {
		if resp == nil {
			return
		}
		// Echo the per-exchange id back to the caller; guarantee an origin
		// ts_sent even when an in-process handler did not stamp one. The same
		// completed response is stored as this exchange's record.
		resp.ID = req.ID
		if resp.TsSent == "" {
			resp.TsSent = protocol.Timestamp()
		}
		b.msgs.Complete(req.ID, *resp)
	}()

	// Queue admission. The originating invoke is now queued (its Open stamped the
	// enqueue time). advance() promotes it if its footprint is free; then the
	// caller parks until its Started fact is stamped — running one-at-a-time with
	// any job sharing its footprint. A hop is already inside its originator's
	// running footprint, so it skips the queue and routes inline. A queue entry
	// that never promotes within admitTimeout is recorded failed, never hung.
	if isOriginator {
		b.advance()
		if err := b.waitStarted(req.JobToken); err != nil {
			resp = errorResponse(req.ID, req.JobToken, protocol.CodeInternalError,
				"broker: "+err.Error())
			return resp
		}
	}

	if route.Handler != nil {
		resp = route.Handler(req, caller)
	} else {
		// Route to the target module over its bidirectional broker wire, forwarding
		// the same exchange: the request keeps its id, so the response that returns
		// completes this one record — the crossing is a field of the exchange, not a
		// sibling row. The job_token rides as-is (the job bucket, propagated across
		// the boundary); RoundTrip re-stamps ts_sent per wire write. The forward is
		// a copy so that re-stamp never mutates the request already recorded by Open.
		// A nil mux means the module dropped its wire mid-route.
		if route.Mux == nil {
			resp = errorResponse(req.ID, req.JobToken, protocol.CodeInternalError,
				fmt.Sprintf("broker: module %q has no live wire", route.Module))
			return resp
		}
		fwd := *req
		hr, err := route.Mux.RoundTrip(&fwd, 0)
		if err != nil {
			resp = errorResponse(req.ID, req.JobToken, protocol.CodeInternalError,
				"broker: route to module: "+err.Error())
			return resp
		}
		resp = hr
	}

	// Async admission contract. For an originating invoke into an async
	// capability the synchronous return is only an acceptance ack: accepted:true
	// keeps the job running (its terminal state arrives later via job_update);
	// anything else (a decode failure or accepted:false) is the module declining
	// the job — convert it to a terminal error so derivation reads it as failed
	// and the caller gets a clear message. Core reads the capability's declared
	// async mode, never infers it from the runtime accepted flag. On acceptance
	// the job stays running in the store (its footprint stays held) until the
	// module reports terminal via job_update, which advances the queue.
	if isOriginator && route.Async && resp.Status == "ok" {
		var ir protocol.InvokeResponse
		if err := json.Unmarshal(resp.Result, &ir); err != nil || !ir.Accepted {
			resp = errorResponse(req.ID, req.JobToken, protocol.CodeCallableFailed,
				"module did not accept async job")
			return resp
		}
	}

	return resp
}

func (b *Broker) handleJobUpdate(req *protocol.Request, caller CallerIdentity) *protocol.Response {
	if req.JobToken == "" {
		return errorResponse(req.ID, "", protocol.CodeInvalidParams, "broker: job_token is required for job_update")
	}

	// Params for job_update are JobUpdateParams — decoded for the terminal-state
	// check below; the report is stored verbatim and folded by job derivation.
	var params protocol.JobUpdateParams
	bparams, err := json.Marshal(req.Params)
	if err != nil {
		return errorResponse(req.ID, req.JobToken, protocol.CodeInvalidParams, "broker: invalid params: "+err.Error())
	}
	if err := json.Unmarshal(bparams, &params); err != nil {
		return errorResponse(req.ID, req.JobToken, protocol.CodeInvalidParams, "broker: invalid params: "+err.Error())
	}

	// The job must already exist — its originating invoke is the first record to
	// carry the token. Check before recording this report, otherwise the report
	// itself would satisfy the check.
	if len(b.msgs.ByToken(req.JobToken)) == 0 {
		return errorResponse(req.ID, req.JobToken, protocol.CodeJobNotFound, "broker: job not found")
	}

	// Record the report as its own exchange; messages.Store.Job folds every
	// job_update record sharing the token into the job's terminal state.
	b.msgs.Open(*req, caller.Module, "", caller.Module)
	resp := &protocol.Response{
		V:      protocol.Version,
		ID:     req.ID,
		TsSent: protocol.Timestamp(),
		Status: "ok",
	}
	b.msgs.Complete(req.ID, *resp)

	// Route the recorded message to its live destination. record-then-route: the
	// store has the truth above; if this job was originated by the face, deliver
	// the same report to the surface so the face sees progress/terminal updates
	// without polling. Every job_update is routed — progress and terminal alike.
	if b.surfaceSink != nil && b.surfaceOriginated(req.JobToken) {
		b.surfaceSink(req.JobToken, params)
	}

	// Terminal report — the job is now terminal in the store, so its footprint is
	// freed. Advance the queue so a waiter that was blocked behind this job's
	// footprint is promoted (no-op for a non-terminal progress update).
	if params.State == "done" || params.State == "failed" {
		b.advance()
	}

	return resp
}

func (b *Broker) handleJobStatus(req *protocol.Request) *protocol.Response {
	var token string
	if params, ok := req.Params.(map[string]interface{}); ok {
		token, _ = params["job_token"].(string)
	}
	if token == "" {
		token = req.JobToken
	}

	if token == "" {
		return errorResponse(req.ID, "", protocol.CodeInvalidParams, "broker: job_token is required for job_status")
	}

	job, ok := b.msgs.Job(token)
	if !ok {
		return errorResponse(req.ID, token, protocol.CodeJobNotFound, "broker: job not found")
	}

	js := protocol.JobStatusResponse{
		JobToken: job.Token,
		State:    job.State,
		Error:    job.Error,
	}
	// started_at is the promotion stamp — absent while the job is still queued.
	if !job.StartedAt.IsZero() {
		js.StartedAt = job.StartedAt.Format(time.RFC3339)
	}
	if !job.FinishedAt.IsZero() {
		js.FinishedAt = job.FinishedAt.Format(time.RFC3339)
	}

	if len(job.Output) > 0 {
		js.Output = job.Output
	}

	resB, _ := json.Marshal(js)
	return &protocol.Response{
		V:        protocol.Version,
		ID:       req.ID,
		TsSent:   protocol.Timestamp(),
		Status:   "ok",
		JobToken: token,
		Result:   resB,
	}
}

// errorResponse builds a protocol error envelope. id echoes the request's
// per-exchange id; ts_sent is the broker's origin time.
func errorResponse(id, jobToken, code, message string) *protocol.Response {
	return &protocol.Response{
		V:        protocol.Version,
		ID:       id,
		TsSent:   protocol.Timestamp(),
		Status:   "error",
		JobToken: jobToken,
		Error:    &protocol.ErrorDetail{Code: code, Message: message},
	}
}
