// SPDX-License-Identifier: Apache-2.0

// Package broker — probe.go is the unrecorded dispatch path used only by
// CORE BIST self-tests.
//
// BIST proves a declared in-process capability is actually wired into the
// dispatch table — a reachability ping, not a behavioral call. The recorded
// path (handleInvoke) opens a messages record, enters the admission queue, and
// completes the record on return; for a self-test probe that is pure noise in
// the store and the queue. probeDispatch is the in-process analogue of the wire
// handshake: it resolves the route and invokes the handler directly, with no
// Open/Complete, no queue admission, and no requires-gate.
//
// It is reachable only through ProbeInvoker(); it is not a wire verb, not a
// broker route, and invisible to modules and to the protocol.
package broker

import (
	"fmt"
	"time"

	"github.com/solutionsunity/suctl/sdk/protocol"
)

// probeDispatch resolves capName and invokes its target WITHOUT recording or
// queueing. A missing route returns CAPABILITY_NOT_ACTIVE — identical to the
// recorded path — so BIST's "declared but not registered" check is unchanged.
func (b *Broker) probeDispatch(req *protocol.Request) *protocol.Response {
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

	// In-process handler: invoke directly under the originating identity.
	if route.Handler != nil {
		return route.Handler(req, CallerIdentity{})
	}

	// Wire module: a direct mux round trip, the same off-ledger crossing the
	// handshake uses. CORE BIST probes only in-process system caps today; this
	// branch keeps the probe path symmetric for any future wire self-test.
	if route.Mux == nil {
		return errorResponse(req.ID, req.JobToken, protocol.CodeInternalError,
			fmt.Sprintf("broker: module %q has no live wire", route.Module))
	}
	fwd := *req
	hr, err := route.Mux.RoundTrip(&fwd, 0)
	if err != nil {
		return errorResponse(req.ID, req.JobToken, protocol.CodeInternalError,
			"broker: route to module: "+err.Error())
	}
	return hr
}

// probeInvoker adapts probeDispatch to protocol.Invoker so the existing CORE
// BIST probe bodies run unchanged over the unrecorded path.
type probeInvoker struct{ b *Broker }

// ProbeInvoker returns a protocol.Invoker that dispatches off-ledger — the only
// exported seam, handed to conformance.ProbeCore by startup. It exposes no
// broker internals: the return is an interface and every dispatch detail stays
// private to the package.
func (b *Broker) ProbeInvoker() protocol.Invoker { return probeInvoker{b} }

// Invoke mirrors Broker.Invoke's error contract (a protocol error becomes a
// wrapped *protocol.ErrorDetail) so ProbeCore's errors.As logic is identical.
func (p probeInvoker) Invoke(jobToken, callableName string, args interface{}) (*protocol.Response, error) {
	req := &protocol.Request{
		V:        protocol.Version,
		ID:       protocol.NewID(),
		TsSent:   protocol.Timestamp(),
		Cmd:      "invoke",
		JobToken: jobToken,
		Params:   map[string]interface{}{"name": callableName, "args": args},
	}
	resp := p.b.probeDispatch(req)
	if resp.Status == "error" && resp.Error != nil {
		return nil, fmt.Errorf("invoke %s: %w", callableName, resp.Error)
	}
	return resp, nil
}

// InvokeWithTimeout satisfies protocol.Invoker. Dispatch is synchronous and
// in-process, so the timeout is advisory — it matches Broker.InvokeWithTimeout.
func (p probeInvoker) InvokeWithTimeout(jobToken, callableName string, args interface{}, _ time.Duration) (*protocol.Response, error) {
	return p.Invoke(jobToken, callableName, args)
}
