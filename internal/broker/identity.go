// SPDX-License-Identifier: Apache-2.0

// Package broker — identity.go defines caller identity under the
// possession model.
//
// Identity is the wire. Core hands each module one end of a private,
// address-less socketpair at spawn and keeps the other; a request is
// attributed to the module that holds the far end of the wire it arrived
// on. There is no PID, no SO_PEERCRED, no registry — possession of the
// wire is the proof. Requests that arrive on the shared face channel
// carry no module name: they are the originating face.
package broker

import "github.com/solutionsunity/suctl/internal/module"

// CallerIdentity describes who initiated a broker request — the module that
// owns the wire the request arrived on, or "" for the originating face. The
// canonical type lives in the modules store; this alias keeps the broker's call
// sites unchanged.
type CallerIdentity = module.CallerIdentity

// InProcessHandler implements a capability inside core. Aliased to the modules
// store type so handlers register on the store and dispatch through the broker
// with one shared signature.
type InProcessHandler = module.InProcessHandler
