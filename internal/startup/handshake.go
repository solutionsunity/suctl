// SPDX-License-Identifier: Apache-2.0

package startup

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/solutionsunity/suctl/internal/wire"
	"github.com/solutionsunity/suctl/sdk/manifest"
	"github.com/solutionsunity/suctl/sdk/protocol"
)

// handshake sends the handshake command over the module's inherited broker wire
// and validates the returned manifest against the on-disk one. The wire is
// connected from spawn, so a single round trip bounded by timeout suffices: it
// returns as soon as the module installs its dispatcher and answers, or times
// out if the process never comes up. Identity binding is not done here: the
// module is identified by possession of its inherited wire, bound at spawn.
func handshake(mux *wire.Mux, onDisk *manifest.Manifest, timeout time.Duration) error {
	resp, err := mux.RoundTrip(&protocol.Request{V: protocol.Version, Cmd: "handshake", Params: struct{}{}}, timeout)
	if err != nil {
		return fmt.Errorf("handshake: %w", err)
	}
	if resp.Status != "ok" {
		if resp.Error != nil {
			return fmt.Errorf("handshake rejected: %w", resp.Error)
		}
		return fmt.Errorf("handshake rejected: status %q", resp.Status)
	}
	// Decode manifest from handshake result.
	var result protocol.HandshakeResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return fmt.Errorf("handshake: decode result: %w", err)
	}
	var live manifest.Manifest
	if err := json.Unmarshal(result.Manifest, &live); err != nil {
		return fmt.Errorf("handshake: decode live manifest: %w", err)
	}
	// Validate key fields match on-disk manifest. Identity is the module's
	// directory, not a self-declared field — only the protocol is checked
	// here.
	if live.Protocol != onDisk.Protocol {
		return fmt.Errorf("handshake: manifest mismatch: protocol %q vs %q", live.Protocol, onDisk.Protocol)
	}
	return nil
}
