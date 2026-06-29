// SPDX-License-Identifier: Apache-2.0

// Package wire implements the core side of a module's single bidirectional
// broker wire.
//
// Core hands each module one end of a private, address-less socketpair at spawn
// and keeps the other; that one wire carries both directions. Core's outbound
// requests (handshake, health, invoke, hook capabilities) and the module's
// inbound requests (invoke, job_update, job_status) are multiplexed over it. A
// single read loop owns the connection: it demultiplexes inbound requests (cmd
// set) — dispatched to the handler in their own goroutine — from responses
// (status set) delivered to the pending outbound call matched by envelope id.
// Writes are serialized; outbound calls may be concurrent and are correlated by
// id rather than position. Once the stream breaks, err is sticky and every
// pending and future call fails fast.
//
// Mux is the exact mirror of the module-side brokerclient wire: possession of
// the socketpair end is the module's identity, so there is no socket to dial.
package wire

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/solutionsunity/suctl/sdk/protocol"
)

// DefaultCallTimeout is the fallback deadline for a convenience round trip.
const DefaultCallTimeout = 30 * time.Second

// Handler dispatches an inbound module->core request (invoke, job_update,
// job_status) and returns the response to write back. The broker installs one
// bound to the owning module's caller identity (possession = identity).
type Handler func(req *protocol.Request) *protocol.Response

// Mux multiplexes both directions of one module's broker wire — core's far end
// of the socketpair it created at spawn.
type Mux struct {
	conn io.ReadWriteCloser

	writeMu sync.Mutex
	enc     *json.Encoder

	mu      sync.Mutex
	pending map[string]chan *protocol.Response
	err     error

	handler Handler
}

// New wraps conn as a bidirectional broker wire and starts its read loop.
// handler services inbound module->core requests; a nil handler refuses them.
// conn is any io.ReadWriteCloser: a net.Conn over the Unix socketpair, or the
// bonded anonymous-pipe pair on Windows — the mux only ever reads, writes, and
// closes, never relying on net-specific features such as deadlines.
func New(conn io.ReadWriteCloser, handler Handler) *Mux {
	m := &Mux{
		conn:    conn,
		enc:     json.NewEncoder(conn),
		pending: make(map[string]chan *protocol.Response),
		handler: handler,
	}
	go m.readLoop()
	return m
}

// Close tears down the wire; the read loop then fails any pending calls.
func (m *Mux) Close() error { return m.conn.Close() }

// readLoop is the sole reader. It routes each envelope by shape: a request (cmd
// set) is dispatched to the handler in its own goroutine so a slow or
// re-entrant handler never blocks the reader; a response is delivered to the
// outbound call waiting on its id. It returns when the wire closes, failing all
// pending calls.
func (m *Mux) readLoop() {
	dec := json.NewDecoder(bufio.NewReader(m.conn))
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			m.fail(fmt.Errorf("wire: closed: %w", err))
			return
		}
		var peek struct {
			Cmd string `json:"cmd"`
		}
		_ = json.Unmarshal(raw, &peek)
		if peek.Cmd != "" {
			var req protocol.Request
			if json.Unmarshal(raw, &req) == nil {
				go m.serveInbound(&req)
			}
			continue
		}
		var resp protocol.Response
		if json.Unmarshal(raw, &resp) == nil {
			m.deliver(&resp)
		}
	}
}

// serveInbound dispatches one inbound request to the handler and writes its
// response. With no handler it refuses the request rather than dropping it, so
// the module's call fails fast instead of hanging.
func (m *Mux) serveInbound(req *protocol.Request) {
	var resp *protocol.Response
	if m.handler == nil {
		resp = &protocol.Response{
			V: protocol.Version, ID: req.ID, TsSent: protocol.Timestamp(),
			Status: "error", JobToken: req.JobToken,
			Error: &protocol.ErrorDetail{Code: protocol.CodeUnknownCommand, Message: "wire: no request handler installed"},
		}
	} else {
		resp = m.handler(req)
	}
	_ = m.write(resp)
}

// deliver hands a response to the outbound call waiting on its id, if one still
// waits (a timed-out caller has removed its waiter).
func (m *Mux) deliver(resp *protocol.Response) {
	m.mu.Lock()
	ch, ok := m.pending[resp.ID]
	if ok {
		delete(m.pending, resp.ID)
	}
	m.mu.Unlock()
	if ok {
		ch <- resp
	}
}

// fail marks the wire broken and releases every pending caller with a nil
// sentinel; each then returns the sticky err. Idempotent under mu.
func (m *Mux) fail(err error) {
	m.mu.Lock()
	if m.err == nil {
		m.err = err
	}
	pending := m.pending
	m.pending = make(map[string]chan *protocol.Response)
	m.mu.Unlock()
	for _, ch := range pending {
		ch <- nil
	}
}

// write serializes one envelope onto the wire so concurrent senders never
// interleave a frame.
func (m *Mux) write(v interface{}) error {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	return m.enc.Encode(v)
}
