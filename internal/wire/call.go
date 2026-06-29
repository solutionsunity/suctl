// SPDX-License-Identifier: Apache-2.0

package wire

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/solutionsunity/suctl/sdk/protocol"
)

// RoundTrip writes req as an outbound core->module request and blocks until the
// read loop delivers the matching response, the wire fails, or timeout elapses.
// A forwarded request keeps its id end-to-end (the broker copies it through so
// one record spans the crossing); the mint-when-empty is a defensive fallback
// for an id-less request, and ts_sent is always re-stamped. A non-positive
// timeout waits without a deadline — only a wire close releases the caller —
// matching a blocking
// synchronous forward. The connection carries no deadline (it is shared and
// persistent) so the per-call bound is enforced here, not on the socket.
func (m *Mux) RoundTrip(req *protocol.Request, timeout time.Duration) (*protocol.Response, error) {
	if req.ID == "" {
		req.ID = protocol.NewID()
	}
	req.TsSent = protocol.Timestamp()

	ch := make(chan *protocol.Response, 1)
	m.mu.Lock()
	if m.err != nil {
		m.mu.Unlock()
		return nil, m.err
	}
	m.pending[req.ID] = ch
	m.mu.Unlock()

	if err := m.write(req); err != nil {
		m.mu.Lock()
		delete(m.pending, req.ID)
		m.mu.Unlock()
		return nil, fmt.Errorf("wire: encode request: %w", err)
	}

	if timeout <= 0 {
		resp := <-ch
		return m.result(resp)
	}
	select {
	case resp := <-ch:
		return m.result(resp)
	case <-time.After(timeout):
		m.mu.Lock()
		delete(m.pending, req.ID)
		m.mu.Unlock()
		return nil, fmt.Errorf("wire: timeout after %v waiting for response", timeout)
	}
}

// result resolves a channel receive: a nil sentinel means the wire failed, so
// the sticky err (or a generic closed error) is returned.
func (m *Mux) result(resp *protocol.Response) (*protocol.Response, error) {
	if resp != nil {
		return resp, nil
	}
	m.mu.Lock()
	err := m.err
	m.mu.Unlock()
	if err == nil {
		err = fmt.Errorf("wire: closed")
	}
	return nil, err
}

// Handshake sends the handshake command and returns the raw response so the
// caller can decode and validate the module's live manifest.
func (m *Mux) Handshake() (*protocol.Response, error) {
	resp, err := m.RoundTrip(&protocol.Request{V: protocol.Version, Cmd: "handshake", Params: struct{}{}}, DefaultCallTimeout)
	if err != nil {
		return nil, fmt.Errorf("handshake: %w", err)
	}
	if resp.Status == "error" && resp.Error != nil {
		return nil, fmt.Errorf("handshake: %w", resp.Error)
	}
	return resp, nil
}

// Health sends the health command with the default deadline.
func (m *Mux) Health() (*protocol.HealthResult, error) {
	return m.HealthWithTimeout(DefaultCallTimeout)
}

// HealthWithTimeout sends the health command bounded by timeout and returns the
// parsed result.
func (m *Mux) HealthWithTimeout(timeout time.Duration) (*protocol.HealthResult, error) {
	resp, err := m.RoundTrip(&protocol.Request{V: protocol.Version, Cmd: "health", Params: struct{}{}}, timeout)
	if err != nil {
		return nil, fmt.Errorf("health: %w", err)
	}
	if resp.Status == "error" && resp.Error != nil {
		return nil, fmt.Errorf("health: %w", resp.Error)
	}
	var result protocol.HealthResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("health: unmarshal result: %w", err)
	}
	return &result, nil
}

// Invoke sends an invoke command with the default deadline (protocol.Invoker).
func (m *Mux) Invoke(jobToken, callableName string, args interface{}) (*protocol.Response, error) {
	return m.InvokeWithTimeout(jobToken, callableName, args, DefaultCallTimeout)
}

// InvokeWithTimeout sends an invoke command bounded by timeout (protocol.Invoker).
// A protocol error is returned as the wrapped *protocol.ErrorDetail so callers
// can errors.As it; a transport error returns a nil response.
func (m *Mux) InvokeWithTimeout(jobToken, callableName string, args interface{}, timeout time.Duration) (*protocol.Response, error) {
	req := &protocol.Request{
		V:        protocol.Version,
		Cmd:      "invoke",
		JobToken: jobToken,
		Params:   protocol.InvokeRequest{Name: callableName, Args: args},
	}
	resp, err := m.RoundTrip(req, timeout)
	if err != nil {
		return nil, fmt.Errorf("invoke %s: %w", callableName, err)
	}
	if resp.Status == "error" && resp.Error != nil {
		return resp, resp.Error
	}
	return resp, nil
}
