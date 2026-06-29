// SPDX-License-Identifier: Apache-2.0

// Package protocol declares the wire types used by suctl modules to speak
// the module protocol.
//
// This package is the single source of truth for the wire contract.
package protocol

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"
)

// Version is the protocol version string sent in every envelope.
const Version = "1"

// uuidv4 returns a random UUID v4 string.
func uuidv4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// NewJobToken returns a random UUID v4 string for use as a protocol job_token —
// the job bucket id, carried only on job-bearing verbs.
func NewJobToken() string { return uuidv4() }

// NewID returns a random UUID v4 string for use as a per-exchange envelope id
// Every envelope carries one; the responder echoes it.
func NewID() string { return uuidv4() }

// Timestamp returns the current time as an RFC3339 string with explicit offset,
// for use as an envelope ts_sent.
func Timestamp() string { return time.Now().Format(time.RFC3339) }

// contextKey is the unexported type for protocol context keys, isolating them
// from keys defined in other packages.
type contextKey string

// jobTokenKey is the canonical key under which the job token rides on a
// context.Context. It lives here — the lowest package both module ends
// import — so modserver (stamping the inbound token) and brokerclient
// (propagating it to a sub-call) share one key and a cross-module hop reuses
// the originator's job bucket instead of minting a fresh one.
const jobTokenKey contextKey = "job_token"

// WithJobToken returns a child context carrying token as the job token.
func WithJobToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, jobTokenKey, token)
}

// JobToken returns the job token carried on ctx, or "" if none is present.
func JobToken(ctx context.Context) string {
	if token, ok := ctx.Value(jobTokenKey).(string); ok {
		return token
	}
	return ""
}

// Request is the envelope sent to a module socket or the broker socket.
//
// Every envelope carries id (per-exchange UUID, echoed in the response) and
// ts_sent (origin time, RFC3339 with offset). job_token is the job bucket id,
// present only on job-bearing verbs (invoke, job_status, job_update).
type Request struct {
	V        string      `json:"v"`
	ID       string      `json:"id"`
	TsSent   string      `json:"ts_sent"`
	Cmd      string      `json:"cmd"`
	JobToken string      `json:"job_token,omitempty"`
	Params   interface{} `json:"params"`
}

// Response is the envelope returned by a module or the broker. id echoes the
// request id; ts_sent is the responder's origin time. ts_received is
// stamped by each receiver at ingest and never travels on the wire.
type Response struct {
	V        string          `json:"v"`
	ID       string          `json:"id,omitempty"`
	TsSent   string          `json:"ts_sent,omitempty"`
	Status   string          `json:"status"`
	JobToken string          `json:"job_token,omitempty"`
	Result   json.RawMessage `json:"result,omitempty"`
	Error    *ErrorDetail    `json:"error,omitempty"`
}

// ErrorDetail is the structured error object inside a Response.
type ErrorDetail struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Detail  json.RawMessage `json:"detail,omitempty"`
}

func (e *ErrorDetail) Error() string {
	if e == nil {
		return ""
	}
	return e.Code + ": " + e.Message
}

// InvokeRequest is the params object for an invoke command.
type InvokeRequest struct {
	Name string      `json:"name"`
	Args interface{} `json:"args"`
}

// InvokeResponse is the result payload returned by a synchronous invoke.
type InvokeResponse struct {
	Name     string          `json:"name"`
	Output   json.RawMessage `json:"output,omitempty"`
	Accepted bool            `json:"accepted,omitempty"`
}

// Error codes.
const (
	CodeUnknownCommand        = "UNKNOWN_COMMAND"
	CodeUnknownCallable       = "UNKNOWN_CALLABLE"
	CodeInvalidParams         = "INVALID_PARAMS"
	CodeCallableFailed        = "CALLABLE_FAILED"
	CodeHandshakeFailed       = "HANDSHAKE_FAILED"
	CodeJobNotFound           = "JOB_NOT_FOUND"
	CodeCapabilityNotActive   = "CAPABILITY_NOT_ACTIVE"
	CodeCapabilityNotDeclared = "CAPABILITY_NOT_DECLARED"
	CodeInternalError         = "INTERNAL_ERROR"
	CodeConfirmationRequired  = "CONFIRMATION_REQUIRED"
)

// --------------------------------------------------------------------------
// Command-specific result shapes
// --------------------------------------------------------------------------

// HealthResult is the result payload returned by the health command.
type HealthResult struct {
	Status string `json:"status"`
	Uptime int    `json:"uptime_seconds"`
}

// HandshakeResult is the result payload returned by the handshake command.
type HandshakeResult struct {
	Manifest json.RawMessage `json:"manifest"`
}

// JobStatusResponse is the result payload for job_status.
type JobStatusResponse struct {
	JobToken   string          `json:"job_token"`
	State      string          `json:"state"` // running | done | failed
	StartedAt  string          `json:"started_at,omitempty"`
	FinishedAt string          `json:"finished_at,omitempty"`
	Output     json.RawMessage `json:"output,omitempty"`
	Error      *ErrorDetail    `json:"error,omitempty"`
}

// JobUpdateParams is the params object for a job_update command.
type JobUpdateParams struct {
	State    string          `json:"state,omitempty"`
	Message  string          `json:"message,omitempty"`
	Progress int             `json:"progress,omitempty"`
	Output   json.RawMessage `json:"output,omitempty"`
	Error    *ErrorDetail    `json:"error,omitempty"`
}

// --------------------------------------------------------------------------
// Result helpers — common return patterns for module dispatch handlers.
// --------------------------------------------------------------------------

// OKResult marshals v to JSON and returns it as a raw capability output.
// Use this in module dispatch handlers as the success return value.
func OKResult(v interface{}) (json.RawMessage, *ErrorDetail) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, &ErrorDetail{Code: CodeInternalError, Message: err.Error()}
	}
	return b, nil
}

// FailResult returns a (nil, *ErrorDetail) pair — shorthand for the common
// error return pattern in module dispatch handlers.
func FailResult(code, message string) (json.RawMessage, *ErrorDetail) {
	return nil, &ErrorDetail{Code: code, Message: message}
}
