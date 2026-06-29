// SPDX-License-Identifier: Apache-2.0

// Package brokerclient sends invoke envelopes to the suctl core broker so a
// module can call capabilities declared by other modules.
//
// Transport. Core-managed modules inherit a private, address-less
// broker wire at spawn — one end of a socketpair core created and kept the
// other end of. Its fd is named in SUCTL_BROKER_FD. Possession of that wire is
// the module's identity; there is no socket path to dial and no peer-cred.
//
// The wire is bidirectional: the same socketpair carries this module's outbound
// calls (invoke, job_update) AND core's inbound requests (handshake, health,
// invoke). A single read loop demultiplexes by envelope shape — a request
// carries cmd and is dispatched to the handler modserver installs via
// SetRequestHandler; a response carries status and is matched to a pending
// outbound call by id. Writes are serialized; outbound calls may be concurrent
// and are correlated by id rather than by position.
//
// SUCTL_BROKER_FD is the only path: a module that did not inherit the wire is
// not a core-managed module and cannot reach the broker. There is no shared
// socket to dial — its absence is a hard error, not a fallback.
//
// The envelope format is identical to the core-to-module protocol — same
// "invoke" command, same job_token, same response shape. Modules never connect
// to other modules' sockets directly.
package brokerclient

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/solutionsunity/suctl/sdk/channel"
	"github.com/solutionsunity/suctl/sdk/protocol"
)

// DefaultCallTimeout is the maximum time for a full broker round trip.
const DefaultCallTimeout = 30 * time.Second

// Client invokes capabilities on the suctl broker over the inherited wire. The
// zero value is usable with default timeouts; override CallTimeout to customize.
// Calls are multiplexed over the single bidirectional wire and correlated by
// envelope id, so concurrent calls are safe.
type Client struct {
	CallTimeout time.Duration
}

func (c *Client) callTimeout() time.Duration {
	if c.CallTimeout <= 0 {
		return DefaultCallTimeout
	}
	return c.CallTimeout
}

// --------------------------------------------------------------------------
// Inherited broker wire (possession = identity)
// --------------------------------------------------------------------------

// RequestHandler dispatches an inbound core->module request (handshake, health,
// invoke) read off the bidirectional wire and returns the response to send
// back. modserver installs one via SetRequestHandler; until it does, inbound
// requests are refused with UNKNOWN_COMMAND.
type RequestHandler func(req *protocol.Request) *protocol.Response

// brokerWire is the core-managed module's single bidirectional wire — core's far
// end of the socketpair it inherited at spawn. One read loop owns the
// connection: it demultiplexes inbound requests (dispatched to handler in their
// own goroutine) from responses (delivered to the pending outbound call matched
// by envelope id). Writes are serialized under writeMu so concurrent senders —
// the read loop writing inbound responses and outbound calls writing requests —
// never interleave a frame. Once the stream breaks, err is sticky and every
// pending and future call fails fast.
type brokerWire struct {
	conn io.ReadWriteCloser

	writeMu sync.Mutex
	enc     *json.Encoder

	mu      sync.Mutex
	pending map[string]chan *protocol.Response
	err     error

	handlerMu sync.RWMutex
	handler   RequestHandler
}

var (
	wireOnce sync.Once
	wire     *brokerWire
)

// newWire wraps conn as a bidirectional broker wire and starts its read loop.
// conn is any io.ReadWriteCloser: a net.Conn over the inherited Unix socketpair
// fd, or the bonded anonymous-pipe pair recovered from the two inherited Windows
// handles. The mux only reads, writes, and closes — never net-specific features.
func newWire(conn io.ReadWriteCloser) *brokerWire {
	w := &brokerWire{
		conn:    conn,
		enc:     json.NewEncoder(conn),
		pending: make(map[string]chan *protocol.Response),
	}
	go w.readLoop()
	return w
}

// inheritedWire returns the process-wide broker wire recovered from the
// inherited end(s) of the channel core created at spawn, or nil when none was
// inherited (callers then fail: there is no shared socket to dial). The
// OS-specific recovery — a single fd on Unix, a read/write handle pair on
// Windows — lives in the sdk/channel transport seam (channel.Inherit);
// everything here is OS-agnostic. Resolved exactly once per process; the read
// loop starts here so both inbound dispatch and outbound calls share one reader.
func inheritedWire() *brokerWire {
	wireOnce.Do(func() {
		conn, ok := channel.Inherit()
		if !ok {
			return
		}
		wire = newWire(conn)
	})
	return wire
}

// SetRequestHandler installs the dispatcher for inbound core->module requests on
// the module's bidirectional wire. modserver calls this once at
// startup; until it does, inbound requests are refused with UNKNOWN_COMMAND.
// Returns an error if the module did not inherit a broker wire.
func SetRequestHandler(h RequestHandler) error {
	w := inheritedWire()
	if w == nil {
		return fmt.Errorf("brokerclient: no inherited broker wire (not a core-managed module)")
	}
	w.handlerMu.Lock()
	w.handler = h
	w.handlerMu.Unlock()
	return nil
}

// readLoop is the sole reader of the wire. It decodes one envelope at a time and
// routes it by shape: a request (cmd set) is dispatched to the inbound handler
// in its own goroutine so a slow handler — or one that makes its own outbound
// call on this wire — never blocks the reader; a response is delivered to the
// matching pending outbound call. It returns when the wire closes, failing all
// pending calls.
func (w *brokerWire) readLoop() {
	dec := json.NewDecoder(bufio.NewReader(w.conn))
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			w.fail(fmt.Errorf("brokerclient: wire closed: %w", err))
			return
		}
		var peek struct {
			Cmd string `json:"cmd"`
		}
		_ = json.Unmarshal(raw, &peek)
		if peek.Cmd != "" {
			var req protocol.Request
			if err := json.Unmarshal(raw, &req); err != nil {
				continue
			}
			go w.serveInbound(&req)
		} else {
			var resp protocol.Response
			if err := json.Unmarshal(raw, &resp); err != nil {
				continue
			}
			w.deliver(&resp)
		}
	}
}

// serveInbound dispatches one inbound request to the installed handler and
// writes its response. With no handler installed yet it refuses the request
// rather than dropping it, so core's call fails fast instead of hanging.
func (w *brokerWire) serveInbound(req *protocol.Request) {
	w.handlerMu.RLock()
	h := w.handler
	w.handlerMu.RUnlock()

	var resp *protocol.Response
	if h == nil {
		resp = &protocol.Response{
			V:        protocol.Version,
			ID:       req.ID,
			TsSent:   protocol.Timestamp(),
			Status:   "error",
			JobToken: req.JobToken,
			Error:    &protocol.ErrorDetail{Code: protocol.CodeUnknownCommand, Message: "brokerclient: no request handler installed"},
		}
	} else {
		resp = h(req)
	}
	_ = w.write(resp)
}

// deliver hands a decoded response to the outbound call waiting on its id, if
// any still waits (a timed-out caller has removed its waiter).
func (w *brokerWire) deliver(resp *protocol.Response) {
	w.mu.Lock()
	ch, ok := w.pending[resp.ID]
	if ok {
		delete(w.pending, resp.ID)
	}
	w.mu.Unlock()
	if ok {
		ch <- resp
	}
}

// fail marks the wire broken and releases every pending caller with a nil
// sentinel; each returns the sticky err. Idempotent under mu.
func (w *brokerWire) fail(err error) {
	w.mu.Lock()
	if w.err == nil {
		w.err = err
	}
	pending := w.pending
	w.pending = make(map[string]chan *protocol.Response)
	w.mu.Unlock()
	for _, ch := range pending {
		ch <- nil
	}
}

// write serializes one envelope onto the wire so concurrent senders never
// interleave a frame.
func (w *brokerWire) write(v interface{}) error {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	return w.enc.Encode(v)
}

// roundTrip registers a waiter under the request's envelope id, writes the
// request, and blocks until the read loop delivers the matching response, the
// wire fails, or timeout elapses. The connection carries no deadline — it is
// shared and persistent — so the per-call timeout is enforced here, not on the
// socket.
func (w *brokerWire) roundTrip(req *protocol.Request, timeout time.Duration) (*protocol.Response, error) {
	ch := make(chan *protocol.Response, 1)
	w.mu.Lock()
	if w.err != nil {
		w.mu.Unlock()
		return nil, w.err
	}
	w.pending[req.ID] = ch
	w.mu.Unlock()

	if err := w.write(req); err != nil {
		w.mu.Lock()
		delete(w.pending, req.ID)
		w.mu.Unlock()
		return nil, fmt.Errorf("brokerclient: encode request: %w", err)
	}

	select {
	case resp := <-ch:
		if resp == nil {
			w.mu.Lock()
			err := w.err
			w.mu.Unlock()
			if err == nil {
				err = fmt.Errorf("brokerclient: wire closed")
			}
			return nil, err
		}
		return resp, nil
	case <-time.After(timeout):
		w.mu.Lock()
		delete(w.pending, req.ID)
		w.mu.Unlock()
		return nil, fmt.Errorf("brokerclient: timeout after %v waiting for response", timeout)
	}
}

// roundTrip performs one broker exchange over the inherited wire. The returned
// Response is decoded but not interpreted — callers apply command-specific error
// handling. Without an inherited wire the caller is not a core-managed module
// and the call fails: there is no shared socket to dial.
func (c *Client) roundTrip(req *protocol.Request) (*protocol.Response, error) {
	w := inheritedWire()
	if w == nil {
		return nil, fmt.Errorf("brokerclient: no inherited broker wire (not a core-managed module)")
	}
	return w.roundTrip(req, c.callTimeout())
}

// jobTokenFor returns the job token to stamp on a sub-call: the originator's
// token propagated on ctx when present, else a fresh one. Propagating
// the token makes a cross-module hop re-enter the originator's single job
// bucket and running footprint rather than minting a new bucket per hop.
func jobTokenFor(ctx context.Context) string {
	if t := protocol.JobToken(ctx); t != "" {
		return t
	}
	return protocol.NewJobToken()
}

// InvokeContext is Invoke with job-token propagation: it reuses the token
// carried on ctx instead of minting a fresh one. A module handler should
// pass the ctx it received so its cross-module calls share the originator's job.
func (c *Client) InvokeContext(ctx context.Context, capName string, args interface{}) (*protocol.Response, error) {
	return c.invoke(jobTokenFor(ctx), capName, args)
}

// Invoke sends an invoke envelope to the broker for capName with the supplied
// args, minting a fresh job token. It returns the decoded Response. If the
// response carries a protocol error the Response is still returned non-nil and
// the error is the *protocol.ErrorDetail. Transport errors return a Go error
// and nil Response.
func (c *Client) Invoke(capName string, args interface{}) (*protocol.Response, error) {
	return c.invoke(protocol.NewJobToken(), capName, args)
}

func (c *Client) invoke(token, capName string, args interface{}) (*protocol.Response, error) {
	req := &protocol.Request{
		V:        protocol.Version,
		ID:       protocol.NewID(),
		TsSent:   protocol.Timestamp(),
		Cmd:      "invoke",
		JobToken: token,
		Params:   protocol.InvokeRequest{Name: capName, Args: args},
	}

	resp, err := c.roundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.Status == "error" && resp.Error != nil {
		return resp, resp.Error
	}
	return resp, nil
}

// JobUpdate sends a job_update envelope to the broker to update a job's state.
func (c *Client) JobUpdate(jobToken string, params protocol.JobUpdateParams) error {
	req := &protocol.Request{
		V:        protocol.Version,
		ID:       protocol.NewID(),
		TsSent:   protocol.Timestamp(),
		Cmd:      "job_update",
		JobToken: jobToken,
		Params:   params,
	}

	resp, err := c.roundTrip(req)
	if err != nil {
		return err
	}

	if resp.Status == "error" && resp.Error != nil {
		return resp.Error
	}
	return nil
}

// InvokeAndWaitContext is InvokeAndWait with job-token propagation: it reuses
// the token carried on ctx so a cross-module hop shares the originator's
// job bucket and running footprint.
func (c *Client) InvokeAndWaitContext(ctx context.Context, capName string, args interface{}) (*protocol.InvokeResponse, error) {
	return c.invokeAndWait(jobTokenFor(ctx), capName, args)
}

// InvokeAndWait sends an invoke envelope and returns the call's terminal
// InvokeResponse. A cross-module invoke returns its result inline — execution
// is synchronous and re-entrant within the originator's running footprint
// — so there is no module-side polling: the broker's response already
// carries the terminal output.
func (c *Client) InvokeAndWait(capName string, args interface{}) (*protocol.InvokeResponse, error) {
	return c.invokeAndWait(protocol.NewJobToken(), capName, args)
}

func (c *Client) invokeAndWait(token, capName string, args interface{}) (*protocol.InvokeResponse, error) {
	resp, err := c.invoke(token, capName, args)
	if err != nil {
		return nil, err
	}

	var ir protocol.InvokeResponse
	if err := json.Unmarshal(resp.Result, &ir); err != nil {
		// resp.Status was already checked for "error" in invoke; an ok result is
		// always an InvokeResponse.
		return nil, fmt.Errorf("brokerclient: unmarshal invoke result: %w", err)
	}
	return &ir, nil
}

// Invoke is a convenience wrapper that uses a zero-value Client. Use it for
// one-off calls that do not need custom timeouts or a non-default socket path.
func Invoke(capName string, args interface{}) (*protocol.Response, error) {
	return (&Client{}).Invoke(capName, args)
}

// InvokeContext is a convenience wrapper that uses a zero-value Client and
// propagates the job token carried on ctx.
func InvokeContext(ctx context.Context, capName string, args interface{}) (*protocol.Response, error) {
	return (&Client{}).InvokeContext(ctx, capName, args)
}

// InvokeAndWait is a convenience wrapper that uses a zero-value Client.
func InvokeAndWait(capName string, args interface{}) (*protocol.InvokeResponse, error) {
	return (&Client{}).InvokeAndWait(capName, args)
}

// InvokeAndWaitContext is a convenience wrapper that uses a zero-value Client
// and propagates the job token carried on ctx.
func InvokeAndWaitContext(ctx context.Context, capName string, args interface{}) (*protocol.InvokeResponse, error) {
	return (&Client{}).InvokeAndWaitContext(ctx, capName, args)
}

// JobUpdate is a convenience wrapper that uses a zero-value Client.
func JobUpdate(jobToken string, params protocol.JobUpdateParams) error {
	return (&Client{}).JobUpdate(jobToken, params)
}
