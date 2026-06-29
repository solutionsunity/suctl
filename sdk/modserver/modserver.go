// SPDX-License-Identifier: Apache-2.0

// Package modserver provides the standard request dispatcher for suctl modules.
//
// A core-managed module does not listen on a socket. It inherits one
// bidirectional broker wire (SUCTL_BROKER_FD) on which it both makes outbound
// calls (via brokerclient) and receives core's inbound requests. modserver
// builds the dispatch function for those inbound requests (handshake, health,
// invoke) and installs it on that shared wire via brokerclient.SetRequestHandler,
// then blocks until a termination signal arrives.
package modserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"os/signal"
	"time"

	"github.com/solutionsunity/suctl/sdk/brokerclient"
	"github.com/solutionsunity/suctl/sdk/health"
	"github.com/solutionsunity/suctl/sdk/lifecycle"
	"github.com/solutionsunity/suctl/sdk/logging"
	"github.com/solutionsunity/suctl/sdk/manifest"
	"github.com/solutionsunity/suctl/sdk/protocol"
)

type contextKey string

const tsReceivedKey contextKey = "ts_received"

// Handler is the function signature for a capability implementation.
type Handler func(ctx context.Context, args map[string]interface{}) (interface{}, *protocol.ErrorDetail)

// JobToken returns the job token from the context, if any. It reads the
// canonical protocol key, so a handler that propagates ctx to a
// brokerclient sub-call carries the originator's token.
func JobToken(ctx context.Context) string {
	return protocol.JobToken(ctx)
}

// TsReceived returns the RFC3339 ingest timestamp the server stamped when the
// request was received, if any. Module-owned job state can record
// it as the job's started_at.
func TsReceived(ctx context.Context) string {
	if ts, ok := ctx.Value(tsReceivedKey).(string); ok {
		return ts
	}
	return ""
}

// Config defines the server behavior.
type Config struct {
	// Name is the module's short name (e.g. "suctl-mod-nginx"). It is used as
	// the slog component label and to derive the log file path
	// (/var/log/suctl/<name>.log). Required.
	Name string
	// Manifest is the raw JSON content of the module's manifest.json.
	Manifest []byte
	// Handlers maps capability names to their implementations.
	Handlers map[string]Handler
}

// Serve initialises structured logging for the module, installs the inbound
// request dispatcher on the inherited bidirectional broker wire, and blocks
// until a termination signal is received.
// It does not listen on a socket: core's requests arrive over the same wire
// the module uses for its outbound calls (possession = identity).
func Serve(cfg Config) error {
	logging.InitModule(cfg.Name)

	s := &server{
		manifest:  cfg.Manifest,
		handlers:  cfg.Handlers,
		async:     asyncSet(cfg.Manifest),
		startTime: time.Now(),
	}

	if err := brokerclient.SetRequestHandler(s.dispatch); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), lifecycle.StopSignals()...)
	defer stop()

	<-ctx.Done()
	return nil
}

type server struct {
	manifest  []byte
	handlers  map[string]Handler
	async     map[string]bool
	startTime time.Time
}

// asyncSet parses the manifest and returns the set of capability names declared
// async. Such a capability is dispatched in the background: core receives an
// immediate accepted ack and the module pushes the terminal result via
// job_update. A parse failure yields an empty set (every capability treated
// sync) and is logged.
func asyncSet(manifestJSON []byte) map[string]bool {
	set := map[string]bool{}
	var m manifest.Manifest
	if err := json.Unmarshal(manifestJSON, &m); err != nil {
		slog.Warn("modserver: parse manifest for async flags", "err", err)
		return set
	}
	for _, c := range m.Capabilities {
		if c.Async {
			set[c.Name] = true
		}
	}
	return set
}

// healthResponse builds the result for the health command: the static
// healthy+uptime stamp. Liveness of the module process — does it still answer
// over the wire — is the only question core asks here; backend data status
// belongs to a capability's survey, not the health command.
func (s *server) healthResponse() json.RawMessage {
	r, _ := health.StandardResponse(s.startTime)
	return r
}

type invokeParams struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// dispatch handles one inbound core->module request and returns the response to
// send back over the wire. It is the duplex counterpart of the old per-connection
// handler: the read loop in brokerclient demultiplexes the request and calls this.
func (s *server) dispatch(req *protocol.Request) *protocol.Response {
	tsReceived := protocol.Timestamp()
	resp := &protocol.Response{V: protocol.Version, ID: req.ID, TsSent: protocol.Timestamp(), JobToken: req.JobToken}

	switch req.Cmd {
	case "handshake":
		resp.Status = "ok"
		resp.Result, _ = json.Marshal(map[string]json.RawMessage{"manifest": s.manifest})

	case "health":
		resp.Status = "ok"
		resp.Result = s.healthResponse()

	case "invoke":
		// Params arrives as a decoded value off the wire; re-marshal to recover
		// the invoke params shape regardless of its concrete Go type.
		rawParams, _ := json.Marshal(req.Params)
		var p invokeParams
		json.Unmarshal(rawParams, &p) //nolint:errcheck
		var args map[string]interface{}
		json.Unmarshal(p.Args, &args) //nolint:errcheck
		if args == nil {
			args = map[string]interface{}{}
		}

		handler, ok := s.handlers[p.Name]
		if !ok {
			resp.Status = "error"
			resp.Error = &protocol.ErrorDetail{Code: protocol.CodeUnknownCallable, Message: "unknown callable: " + p.Name}
			break
		}

		ctx := protocol.WithJobToken(context.Background(), req.JobToken)
		ctx = context.WithValue(ctx, tsReceivedKey, tsReceived)

		if s.async[p.Name] {
			// Async capability: acknowledge receipt now and run the work in the
			// background, pushing the terminal result over the broker wire. Sync
			// is the degenerate case — same handler, the verb is just the carrier.
			go s.runAsync(ctx, p.Name, handler, args, req.JobToken)
			r, _ := json.Marshal(protocol.InvokeResponse{Name: p.Name, Accepted: true})
			resp.Status = "ok"
			resp.Result = r
			break
		}

		output, errD := handler(ctx, args)
		if errD != nil {
			resp.Status = "error"
			resp.Error = errD
			break
		}
		r, _ := json.Marshal(protocol.InvokeResponse{Name: p.Name, Output: marshalOutput(output)})
		resp.Status = "ok"
		resp.Result = r

	default:
		resp.Status = "error"
		resp.Error = &protocol.ErrorDetail{Code: protocol.CodeUnknownCommand, Message: "unknown command: " + req.Cmd}
	}

	return resp
}

// runAsync executes an async capability's handler in the background and reports
// its lifecycle to core over the broker wire: a leading running update, then a
// terminal done (with output) or failed (with error). The handler contract is
// identical to a sync capability — modserver translates its return into the
// terminal job_update. ctx carries the job token so any cross-module sub-call
// re-enters the originator's job bucket.
func (s *server) runAsync(ctx context.Context, name string, handler Handler, args map[string]interface{}, jobToken string) {
	if jobToken != "" {
		if err := brokerclient.JobUpdate(jobToken, protocol.JobUpdateParams{State: "running", Message: name + " running"}); err != nil {
			slog.Warn("modserver: job_update running", "cap", name, "err", err)
		}
	}

	output, errD := handler(ctx, args)
	if jobToken == "" {
		return
	}

	params := protocol.JobUpdateParams{State: "done", Output: marshalOutput(output)}
	if errD != nil {
		params = protocol.JobUpdateParams{State: "failed", Error: errD}
	}
	if err := brokerclient.JobUpdate(jobToken, params); err != nil {
		slog.Warn("modserver: job_update terminal", "cap", name, "state", params.State, "err", err)
	}
}

// marshalOutput renders a handler's return value as a raw capability output,
// passing a json.RawMessage through untouched.
func marshalOutput(output interface{}) json.RawMessage {
	if r, ok := output.(json.RawMessage); ok {
		return r
	}
	raw, _ := json.Marshal(output)
	return raw
}
