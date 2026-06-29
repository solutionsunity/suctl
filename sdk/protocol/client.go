// SPDX-License-Identifier: Apache-2.0

package protocol

import (
	"time"
)

// Invoker is the face→core invoke surface: issue one invoke and get a response.
// The core's in-process broker (the in-process face) satisfies it, as does the
// duplex wire mux that drives a module over its inherited socketpair. Faces and
// conformance probes depend on this interface so the same call sites work
// in-process or over the wire — there is no socket-dialing client.
type Invoker interface {
	Invoke(jobToken, callableName string, args interface{}) (*Response, error)
	InvokeWithTimeout(jobToken, callableName string, args interface{}, timeout time.Duration) (*Response, error)
}
