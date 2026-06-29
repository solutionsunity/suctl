// SPDX-License-Identifier: Apache-2.0

// Package system provides typed response shapes for system module
// capabilities. Anything a face (REPL today, gRPC / HTTP tomorrow)
// needs to know about the module index is described here —
// REPL and external clients use the same types, so the wire contract
// has exactly one source of truth.
package system

import "encoding/json"

// State value strings carried in InventoryEntry.State. These mirror
// internal/module.State constants without forcing wire consumers to
// import that package.
const (
	StateReady       = "ready"
	StateActive      = "active"
	StateUnavailable = "unavailable"
	StateMissing     = "missing"
	StateFailed      = "failed"
)

// InventoryEntry is the wire-shape view of one module known to core.
// It carries everything any face needs to render module presence,
// state, and REPL configuration without reaching into core's in-process
// index. Faces never address modules directly: they reach them only
// through the core, which holds each module's inherited wire.
type InventoryEntry struct {
	// ShortName is the unique short name of the module.
	ShortName string `json:"shortname"`
	// Description is the manifest-declared one-liner.
	Description string `json:"description"`
	// State is one of StateReady / StateActive / StateUnavailable /
	// StateMissing / StateFailed.
	State string `json:"state"`
	// Reason carries the human-readable explanation when State is
	// unavailable, missing, or failed; empty otherwise.
	Reason string `json:"reason,omitempty"`
	// SurfaceConfig carries the module's parsed surface.json as raw JSON so
	// the wire contract does not pin a specific manifest version. Faces
	// decode it into their own SurfaceConfig type. Empty when the module
	// has no surface.json.
	SurfaceConfig json.RawMessage `json:"surface_config,omitempty"`
}

// InventoryResponse is the result of the system.module.inventory
// capability. Entries are in lexicographic order by ShortName.
// ActiveCount and ReadyCount are precomputed so faces can render
// header / status displays without iterating Entries.
type InventoryResponse struct {
	Entries     []InventoryEntry `json:"entries"`
	ActiveCount int              `json:"active_count"`
	ReadyCount  int              `json:"ready_count"`
}
