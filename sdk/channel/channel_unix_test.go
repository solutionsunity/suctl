// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package channel

import (
	"io"
	"net"
	"testing"
)

// TestSpawnRoundTrip verifies the two ends of a Spawn'd Pair are a single
// pre-connected pipe: bytes written on one end arrive on the other, in both
// directions. The remote end is wrapped in-process here for the assertion; in
// production it is inherited by the module process instead.
func TestSpawnRoundTrip(t *testing.T) {
	p, err := Spawn()
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer p.Local.Close()
	defer p.remote.Close()

	remoteConn, err := net.FileConn(p.remote)
	if err != nil {
		t.Fatalf("wrap remote end: %v", err)
	}
	defer remoteConn.Close()

	// local -> remote
	if _, err := p.Local.Write([]byte("ping")); err != nil {
		t.Fatalf("local write: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(remoteConn, buf); err != nil {
		t.Fatalf("remote read: %v", err)
	}
	if got := string(buf); got != "ping" {
		t.Fatalf("remote read = %q, want %q", got, "ping")
	}

	// remote -> local
	if _, err := remoteConn.Write([]byte("pong")); err != nil {
		t.Fatalf("remote write: %v", err)
	}
	if _, err := io.ReadFull(p.Local, buf); err != nil {
		t.Fatalf("local read: %v", err)
	}
	if got := string(buf); got != "pong" {
		t.Fatalf("local read = %q, want %q", got, "pong")
	}
}
