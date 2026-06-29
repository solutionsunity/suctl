// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package privilege

import (
	"os"
	"testing"
)

// TestIsAdmin_MatchesEuid: on Unix the privilege seam is exactly "effective uid
// is root". The check must agree with the kernel's view for whatever user runs
// the tests (root in CI/containers, non-root on a dev host) — both directions
// are exercised depending on the environment.
func TestIsAdmin_MatchesEuid(t *testing.T) {
	want := os.Geteuid() == 0
	if got := IsAdmin(); got != want {
		t.Fatalf("IsAdmin()=%v, want %v (euid=%d)", got, want, os.Geteuid())
	}
}

// TestEscalationHint_Unix: the Unix re-run hint is "sudo", which callers embed in
// the operator-facing message when IsAdmin() is false.
func TestEscalationHint_Unix(t *testing.T) {
	if got := EscalationHint(); got != "sudo" {
		t.Fatalf("EscalationHint()=%q, want %q", got, "sudo")
	}
}
