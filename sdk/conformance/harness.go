// SPDX-License-Identifier: Apache-2.0

package conformance

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/solutionsunity/suctl/sdk/channel"
	"github.com/solutionsunity/suctl/sdk/lifecycle"
	"github.com/solutionsunity/suctl/sdk/manifest"
	"github.com/solutionsunity/suctl/sdk/protocol"
)

// ModuleHarnessOptions configures the BIST harness for a module binary.
type ModuleHarnessOptions struct {
	BinaryPath       string
	ModuleDir        string
	HandshakeTimeout time.Duration
	ProbeTimeout     time.Duration
	ShutdownTimeout  time.Duration
}

// ProbeModuleBinary spawns a module binary, performs handshake, runs BIST,
// and shuts it down gracefully.
func ProbeModuleBinary(opts ModuleHarnessOptions) (*Report, bool, error) {
	if opts.HandshakeTimeout == 0 {
		opts.HandshakeTimeout = 15 * time.Second
	}
	if opts.ProbeTimeout == 0 {
		opts.ProbeTimeout = 2 * time.Second
	}
	if opts.ShutdownTimeout == 0 {
		opts.ShutdownTimeout = 10 * time.Second
	}

	binPath, err := filepath.Abs(opts.BinaryPath)
	if err != nil {
		return nil, false, fmt.Errorf("resolve binary path: %w", err)
	}
	modDir := opts.ModuleDir
	if modDir == "" {
		modDir = filepath.Dir(binPath)
	}
	modDir, err = filepath.Abs(modDir)
	if err != nil {
		return nil, false, fmt.Errorf("resolve module dir: %w", err)
	}

	// We use the SDK manifest loader logic.
	m, err := manifest.LoadFromDir(modDir)
	if err != nil {
		return nil, false, fmt.Errorf("manifest: %w", err)
	}
	rc, _ := manifest.LoadSurfaceFromDir(modDir)

	confDir, _ := os.MkdirTemp("", "suctl-bist-conf-*")
	defer os.RemoveAll(confDir)

	// One bidirectional, pre-connected wire stands in for the inherited broker
	// wire: the module gets one end as SUCTL_BROKER_FD, the harness
	// keeps the other and drives it as the core would. There is no socket to
	// dial. The sdk/channel transport seam builds it (socketpair on Unix, bonded
	// anonymous pipes on Windows) and owns all OS-specific spawn wiring, so this
	// driver stays OS-agnostic.
	ch, err := channel.Spawn()
	if err != nil {
		return nil, false, err
	}

	cmd := exec.Command(binPath)
	cmd.Dir = modDir
	// Graceful-stop prerequisite (lifecycle seam): new process group on Windows
	// so the shutdown probe below can Ctrl-Break it; no-op on Unix.
	lifecycle.Configure(cmd)
	env := append(os.Environ(),
		"SUCTL_MODULE="+manifest.ShortNameFromDir(modDir),
		"SUCTL_MODULE_DIR="+modDir,
		"SUCTL_CONF_DIR="+confDir,
	)
	// Attach hands the child its end of the wire and returns the env naming it:
	// SUCTL_BROKER_FD=3 on Unix, the read/write handle pair on Windows.
	extraEnv, err := ch.Attach(cmd)
	if err != nil {
		ch.Local.Close()
		ch.CloseRemote()
		return nil, false, fmt.Errorf("attach harness wire: %w", err)
	}
	cmd.Env = append(env, extraEnv...)
	if err := cmd.Start(); err != nil {
		ch.Local.Close()
		ch.CloseRemote()
		return nil, false, fmt.Errorf("spawn module: %w", err)
	}
	ch.CloseRemote() // the child owns its end now

	mc := newModuleConn(ch.Local)
	defer mc.Close()

	// The wire is connected from spawn, so a single handshake bounded by
	// HandshakeTimeout suffices: it returns as soon as the module installs its
	// dispatcher and reads the request, or times out if the module never starts.
	hs, err := mc.roundTrip(&protocol.Request{V: protocol.Version, Cmd: "handshake", Params: struct{}{}}, opts.HandshakeTimeout)
	if err != nil {
		killModule(cmd)
		return nil, false, fmt.Errorf("handshake timed out: %w", err)
	}
	if hs.Status != "ok" {
		killModule(cmd)
		return nil, false, fmt.Errorf("handshake rejected: %s", hs.Status)
	}

	report := ProbeModule(mc, m, rc, Options{
		ProbeTimeout: opts.ProbeTimeout,
		ModuleDir:    modDir,
	})

	_ = lifecycle.Stop(cmd.Process)
	doneCh := make(chan error, 1)
	go func() { doneCh <- cmd.Wait() }()
	shutdownPassed := false
	select {
	case <-doneCh:
		shutdownPassed = true
	case <-time.After(opts.ShutdownTimeout):
		killModule(cmd)
	}

	return report, shutdownPassed, nil
}

func killModule(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
}

// --------------------------------------------------------------------------
// moduleConn — the harness's core-side wire mux
// --------------------------------------------------------------------------

// moduleConn drives a module over the core end of its inherited socketpair. It
// is the mirror of brokerclient's wire: a single read loop demultiplexes
// responses to outbound calls (matched by envelope id) from any inbound
// module->core request (refused — the probe is not a real core). Writes are
// serialized. It satisfies ModuleClient so ProbeModule drives a live module the
// same way the core's wire mux does (possession = identity).
type moduleConn struct {
	conn io.ReadWriteCloser

	writeMu sync.Mutex
	enc     *json.Encoder

	mu      sync.Mutex
	pending map[string]chan *protocol.Response
	err     error
}

// newModuleConn wraps the core end of the wire and starts its read loop.
func newModuleConn(conn io.ReadWriteCloser) *moduleConn {
	mc := &moduleConn{
		conn:    conn,
		enc:     json.NewEncoder(conn),
		pending: make(map[string]chan *protocol.Response),
	}
	go mc.readLoop()
	return mc
}

// Close tears down the wire; the read loop then fails any pending calls.
func (mc *moduleConn) Close() { _ = mc.conn.Close() }

// readLoop is the sole reader. It delivers responses to waiting calls by id and
// refuses any inbound module->core request — under BIST there is no core to
// service it, so the module's call fails fast rather than hanging the reader.
func (mc *moduleConn) readLoop() {
	dec := json.NewDecoder(mc.conn)
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			mc.fail(fmt.Errorf("conformance: wire closed: %w", err))
			return
		}
		var peek struct {
			Cmd string `json:"cmd"`
		}
		_ = json.Unmarshal(raw, &peek)
		if peek.Cmd != "" {
			var req protocol.Request
			if json.Unmarshal(raw, &req) == nil {
				mc.refuse(&req)
			}
			continue
		}
		var resp protocol.Response
		if json.Unmarshal(raw, &resp) == nil {
			mc.deliver(&resp)
		}
	}
}

// refuse replies to an inbound module->core request with UNKNOWN_COMMAND.
func (mc *moduleConn) refuse(req *protocol.Request) {
	_ = mc.write(&protocol.Response{
		V:        protocol.Version,
		ID:       req.ID,
		TsSent:   protocol.Timestamp(),
		Status:   "error",
		JobToken: req.JobToken,
		Error:    &protocol.ErrorDetail{Code: protocol.CodeUnknownCommand, Message: "conformance: no core to service module call"},
	})
}

func (mc *moduleConn) deliver(resp *protocol.Response) {
	mc.mu.Lock()
	ch, ok := mc.pending[resp.ID]
	if ok {
		delete(mc.pending, resp.ID)
	}
	mc.mu.Unlock()
	if ok {
		ch <- resp
	}
}

func (mc *moduleConn) fail(err error) {
	mc.mu.Lock()
	if mc.err == nil {
		mc.err = err
	}
	pending := mc.pending
	mc.pending = make(map[string]chan *protocol.Response)
	mc.mu.Unlock()
	for _, ch := range pending {
		ch <- nil
	}
}

func (mc *moduleConn) write(v interface{}) error {
	mc.writeMu.Lock()
	defer mc.writeMu.Unlock()
	return mc.enc.Encode(v)
}

// roundTrip registers a waiter by id, writes the request, and blocks until the
// response arrives, the wire fails, or timeout elapses.
func (mc *moduleConn) roundTrip(req *protocol.Request, timeout time.Duration) (*protocol.Response, error) {
	if req.ID == "" {
		req.ID = protocol.NewID()
	}
	req.TsSent = protocol.Timestamp()

	ch := make(chan *protocol.Response, 1)
	mc.mu.Lock()
	if mc.err != nil {
		mc.mu.Unlock()
		return nil, mc.err
	}
	mc.pending[req.ID] = ch
	mc.mu.Unlock()

	if err := mc.write(req); err != nil {
		mc.mu.Lock()
		delete(mc.pending, req.ID)
		mc.mu.Unlock()
		return nil, fmt.Errorf("conformance: encode request: %w", err)
	}

	select {
	case resp := <-ch:
		if resp == nil {
			mc.mu.Lock()
			err := mc.err
			mc.mu.Unlock()
			if err == nil {
				err = fmt.Errorf("conformance: wire closed")
			}
			return nil, err
		}
		return resp, nil
	case <-time.After(timeout):
		mc.mu.Lock()
		delete(mc.pending, req.ID)
		mc.mu.Unlock()
		return nil, fmt.Errorf("conformance: timeout after %v", timeout)
	}
}

// Handshake sends the handshake command and returns the raw response.
func (mc *moduleConn) Handshake() (*protocol.Response, error) {
	resp, err := mc.roundTrip(&protocol.Request{V: protocol.Version, Cmd: "handshake", Params: struct{}{}}, 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("handshake: %w", err)
	}
	if resp.Status == "error" && resp.Error != nil {
		return nil, fmt.Errorf("handshake: %w", resp.Error)
	}
	return resp, nil
}

// HealthWithTimeout sends the health command and returns the parsed result.
func (mc *moduleConn) HealthWithTimeout(timeout time.Duration) (*protocol.HealthResult, error) {
	resp, err := mc.roundTrip(&protocol.Request{V: protocol.Version, Cmd: "health", Params: struct{}{}}, timeout)
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

// Invoke sends an invoke command with a default deadline (Invoker).
func (mc *moduleConn) Invoke(jobToken, callableName string, args interface{}) (*protocol.Response, error) {
	return mc.InvokeWithTimeout(jobToken, callableName, args, 30*time.Second)
}

// InvokeWithTimeout sends an invoke command bounded by timeout (Invoker). A
// protocol error is returned as the wrapped *protocol.ErrorDetail so callers can
// errors.As it, matching the prior socket client's contract.
func (mc *moduleConn) InvokeWithTimeout(jobToken, callableName string, args interface{}, timeout time.Duration) (*protocol.Response, error) {
	req := &protocol.Request{
		V:        protocol.Version,
		Cmd:      "invoke",
		JobToken: jobToken,
		Params:   protocol.InvokeRequest{Name: callableName, Args: args},
	}
	resp, err := mc.roundTrip(req, timeout)
	if err != nil {
		return nil, fmt.Errorf("invoke %s: %w", callableName, err)
	}
	if resp.Status == "error" && resp.Error != nil {
		return nil, fmt.Errorf("invoke %s: %w", callableName, resp.Error)
	}
	return resp, nil
}
