// SPDX-License-Identifier: Apache-2.0

// Package protocol — cascade.go is the single source of truth for the
// CONFIRMATION_REQUIRED wire contract used by system.module.activate
// Any caller of system.module.* — REPL today, gRPC / HTTP
// tomorrow — uses these types and helpers; the system module
// handlers produce them. Keeping this in sdk/protocol guarantees that
// the contract cannot drift between sender and receiver.
package protocol

import (
	"encoding/json"
	"errors"
)

// CascadeDetail is the payload of a CONFIRMATION_REQUIRED ErrorDetail
// returned by system.module.activate when the target module needs
// additional ready-but-inactive providers to come online. The caller
// re-invokes system.module.activate with params[ConfirmParam]=true to
// approve the full set.
type CascadeDetail struct {
	// Target is the module the operator originally asked to activate.
	Target string `json:"target"`
	// Providers is the depth-first ordered list of provider modules
	// that must be activated before Target. Order is the activation
	// order — provider[0] must be active before provider[1], etc.
	Providers []string `json:"providers"`
}

// ConfirmParam is the request-params key that callers set to true on a
// system.module.activate re-invocation to approve the cascade returned
// by an earlier CONFIRMATION_REQUIRED response.
const ConfirmParam = "confirm"

// NewCascadeError builds the *ErrorDetail returned by system.module.activate
// when a cascade is required. Senders should use this rather than building
// the ErrorDetail by hand so the detail payload shape is owned in one place.
func NewCascadeError(message string, detail CascadeDetail) *ErrorDetail {
	b, _ := json.Marshal(detail)
	return &ErrorDetail{
		Code:    CodeConfirmationRequired,
		Message: message,
		Detail:  b,
	}
}

// AsCascade unwraps err looking for a CONFIRMATION_REQUIRED *ErrorDetail
// and decodes its Detail into a CascadeDetail. Returns (nil, false) when
// err does not represent a cascade-required response.
//
// This is the canonical extraction point for any caller of
// system.module.activate. REPL, future CLI activate commands, and
// gRPC adapters all use this single helper rather than
// reimplementing the unwrap-and-decode pattern.
func AsCascade(err error) (*CascadeDetail, bool) {
	if err == nil {
		return nil, false
	}
	var detail *ErrorDetail
	if !errors.As(err, &detail) {
		return nil, false
	}
	if detail.Code != CodeConfirmationRequired || len(detail.Detail) == 0 {
		return nil, false
	}
	var c CascadeDetail
	if err := json.Unmarshal(detail.Detail, &c); err != nil {
		return nil, false
	}
	if c.Target == "" {
		return nil, false
	}
	return &c, true
}
