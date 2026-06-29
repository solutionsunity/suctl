// SPDX-License-Identifier: Apache-2.0

// Package channel is the broker-wire transport seam — the single, audited home
// of all OS-specific logic for the private, address-less wire between core and a
// core-managed module.
//
// Trust model. Instead of a shared, address-based broker socket that any
// same-uid process can dial, core hands each module a private, pre-connected
// channel and keeps the other end. The wire has no name, no path, and no
// listen/accept — there is nothing for a sibling to dial — so possession of the
// inherited end IS the module's identity. The kernel, not a records table,
// guarantees who is on the wire.
//
// Two-layer split. This package owns the OS-specific BYTES (create the pair,
// pass exactly the right handle(s) to the child, leak nothing); the OS-agnostic
// framing/dispatch (mux) lives above it in core's broker, the conformance
// harness, and the module's brokerclient. Those three call sites all drive the
// wire through this one seam, so the trust-critical inheritance logic exists in
// one place that cannot drift.
//
// Shape. Spawn creates a Pair: the parent keeps Local (an io.ReadWriteCloser it
// reads/writes the module's broker traffic on) and hands the child its end via
// Attach (exec inheritance + the env naming it). After the child has started the
// parent calls CloseRemote (the child inherited its own copy). On the module
// side, Inherit recovers the wire from the inherited end(s).
//
// On Unix the wire is one full-duplex socketpair, so the child's end is a single
// inherited fd (SUCTL_BROKER_FD=3). Windows has no nameless duplex inheritable
// primitive, so two half-duplex anonymous pipes are bonded into one logical
// duplex wire and the child inherits a read/write handle pair
// (SUCTL_BROKER_FD_R / SUCTL_BROKER_FD_W). The trust path is identical to the
// socketpair; only the bytes differ, and that difference is contained here.
package channel
