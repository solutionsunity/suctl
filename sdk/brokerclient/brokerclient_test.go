// SPDX-License-Identifier: Apache-2.0

package brokerclient

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/solutionsunity/suctl/sdk/protocol"
)

// brokerHandlers maps a request command to its fake-broker handler.
type brokerHandlers = map[string]func(req *protocol.Request) *protocol.Response

// serveWire runs a fake broker over conn: it decodes each request and writes the
// matching handler's response, echoing the request's envelope id so the client's
// duplex mux can correlate the response to its pending call. It models core's
// far end of the bidirectional wire. It returns when the connection closes or a
// request names a command with no handler.
func serveWire(conn net.Conn, handlers brokerHandlers) {
	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)
	for {
		var req protocol.Request
		if err := dec.Decode(&req); err != nil {
			return
		}
		h := handlers[req.Cmd]
		if h == nil {
			return
		}
		resp := h(&req)
		if resp.ID == "" {
			resp.ID = req.ID
		}
		if err := enc.Encode(resp); err != nil {
			return
		}
	}
}

// fakeWire installs the process-wide broker wire for the duration of a test,
// backed by an in-memory pipe whose far end a fake broker serves with handlers.
// It mirrors the inherited-fd transport without an fd: the client
// reaches the broker only by possessing this wire, and newWire starts the same
// duplex read loop production uses. wireOnce is consumed with a no-op so
// inheritedWire never reads SUCTL_BROKER_FD, and wire is restored to nil on
// cleanup. The returned Client carries a short call timeout.
func fakeWire(t *testing.T, handlers brokerHandlers) *Client {
	t.Helper()
	clientEnd, brokerEnd := net.Pipe()
	go serveWire(brokerEnd, handlers)
	wireOnce.Do(func() {})
	wire = newWire(clientEnd)
	t.Cleanup(func() {
		_ = clientEnd.Close()
		_ = brokerEnd.Close()
		wire = nil
	})
	return &Client{CallTimeout: time.Second}
}

func TestInvokeSuccess(t *testing.T) {
	c := fakeWire(t, brokerHandlers{
		"invoke": func(req *protocol.Request) *protocol.Response {
			if req.Cmd != "invoke" {
				t.Errorf("cmd = %q, want invoke", req.Cmd)
			}
			params, _ := req.Params.(map[string]interface{})
			if params["name"] != "test.cap" {
				t.Errorf("name = %v, want test.cap", params["name"])
			}
			out, _ := json.Marshal(map[string]string{"hello": "world"})
			return &protocol.Response{
				V:        protocol.Version,
				Status:   "ok",
				JobToken: req.JobToken,
				Result:   out,
			}
		},
	})
	resp, err := c.Invoke("test.cap", map[string]string{"a": "b"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("status = %q, want ok", resp.Status)
	}
}

func TestInvokeProtocolError(t *testing.T) {
	c := fakeWire(t, brokerHandlers{
		"invoke": func(req *protocol.Request) *protocol.Response {
			return &protocol.Response{
				V:      protocol.Version,
				Status: "error",
				Error:  &protocol.ErrorDetail{Code: protocol.CodeCapabilityNotActive, Message: "nope"},
			}
		},
	})
	resp, err := c.Invoke("missing.cap", nil)
	if err == nil {
		t.Fatal("Invoke: expected error, got nil")
	}
	if resp == nil || resp.Status != "error" {
		t.Errorf("response: want status=error, got %+v", resp)
	}
	if resp.Error.Code != protocol.CodeCapabilityNotActive {
		t.Errorf("error code = %q, want %q", resp.Error.Code, protocol.CodeCapabilityNotActive)
	}
}

// TestInvokeNoInheritedWire verifies that without an inherited broker wire the
// caller is not a core-managed module and the call fails: there is no shared
// socket to dial.
func TestInvokeNoInheritedWire(t *testing.T) {
	wireOnce.Do(func() {})
	wire = nil
	t.Cleanup(func() { wire = nil })
	c := &Client{CallTimeout: 200 * time.Millisecond}
	if _, err := c.Invoke("anything", nil); err == nil {
		t.Fatal("expected error with no inherited wire, got nil")
	}
}

// TestInvokeContextPropagatesToken verifies token propagation:
// InvokeContext reuses the job token carried on ctx instead of minting a fresh
// one, while Invoke mints a new token per call.
func TestInvokeContextPropagatesToken(t *testing.T) {
	const token = "tok-originator"
	var seen string
	c := fakeWire(t, brokerHandlers{
		"invoke": func(req *protocol.Request) *protocol.Response {
			seen = req.JobToken
			res, _ := json.Marshal(protocol.InvokeResponse{Name: "test.cap"})
			return &protocol.Response{V: protocol.Version, Status: "ok", JobToken: req.JobToken, Result: res}
		},
	})

	ctx := protocol.WithJobToken(context.Background(), token)
	if _, err := c.InvokeContext(ctx, "test.cap", nil); err != nil {
		t.Fatalf("InvokeContext: %v", err)
	}
	if seen != token {
		t.Errorf("propagated job_token = %q, want %q", seen, token)
	}

	if _, err := c.Invoke("test.cap", nil); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if seen == "" || seen == token {
		t.Errorf("Invoke job_token = %q, want a fresh minted token", seen)
	}
}

// TestInvokeAndWait verifies the synchronous inline model: a
// cross-module invoke returns its terminal InvokeResponse in the broker's
// response — there is no module-side job_status polling.
func TestInvokeAndWait(t *testing.T) {
	c := fakeWire(t, brokerHandlers{
		"invoke": func(req *protocol.Request) *protocol.Response {
			res, _ := json.Marshal(protocol.InvokeResponse{
				Name:   "test.sync",
				Output: json.RawMessage(`{"final": "result"}`),
			})
			return &protocol.Response{
				V: protocol.Version, Status: "ok", JobToken: req.JobToken, Result: res,
			}
		},
		"job_status": func(req *protocol.Request) *protocol.Response {
			t.Errorf("InvokeAndWait must not poll job_status under the inline model")
			return &protocol.Response{V: protocol.Version, Status: "error", JobToken: req.JobToken}
		},
	})
	ir, err := c.InvokeAndWait("test.sync", nil)
	if err != nil {
		t.Fatalf("InvokeAndWait: %v", err)
	}
	var out map[string]string
	json.Unmarshal(ir.Output, &out)
	if out["final"] != "result" {
		t.Errorf("output = %v, want final=result", out)
	}
}

// TestEnvelopeIdentityStamped verifies the client stamps a per-exchange id and
// an RFC3339 ts_sent on every outgoing request.
func TestEnvelopeIdentityStamped(t *testing.T) {
	c := fakeWire(t, brokerHandlers{
		"invoke": func(req *protocol.Request) *protocol.Response {
			if req.ID == "" {
				t.Error("request missing envelope id")
			}
			if _, err := time.Parse(time.RFC3339, req.TsSent); err != nil {
				t.Errorf("ts_sent %q not RFC3339: %v", req.TsSent, err)
			}
			out, _ := json.Marshal(map[string]string{"ok": "1"})
			return &protocol.Response{V: protocol.Version, Status: "ok", ID: req.ID, JobToken: req.JobToken, Result: out}
		},
	})
	if _, err := c.Invoke("test.cap", nil); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
}
