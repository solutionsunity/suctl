// SPDX-License-Identifier: Apache-2.0

package startup

import (
	"strings"
	"testing"

	"github.com/solutionsunity/suctl/internal/gate"
	"github.com/solutionsunity/suctl/internal/messages"
	"github.com/solutionsunity/suctl/internal/module"
	"github.com/solutionsunity/suctl/sdk/manifest"
	"github.com/solutionsunity/suctl/sdk/protocol"
)

// busyMessages returns a messages store holding one running job rooted at module
// under token: an open (uncompleted) originating invoke with its Started fact
// stamped. gate.Busy reads its running set to find the module's footprint held.
func busyMessages(t *testing.T, module, token string) *messages.Store {
	t.Helper()
	msgs := messages.New()
	msgs.Open(protocol.Request{ID: token + "-req", Cmd: "invoke", JobToken: token}, module, module+".cap", "")
	msgs.Start(token)
	return msgs
}

// TestDeactivate_RefusesBusyModule proves the lifecycle ops-gate: when a running
// job's footprint still covers a module, Coordinator.Deactivate returns an error
// and tears nothing down — the module is left fully intact for the running job.
func TestDeactivate_RefusesBusyModule(t *testing.T) {
	entry := module.NewRecord(module.StateActive, nil)
	store := module.NewStore()
	store.Put("foo", entry)

	rt := &Runtime{Store: store, Messages: busyMessages(t, "foo", "job-1")}

	if err := rt.lc().Deactivate("foo", entry); err == nil {
		t.Fatal("Deactivate: expected refusal for a busy module, got nil")
	}
	if entry.State() != module.StateActive {
		t.Errorf("entry.State = %q; want StateActive (no teardown on refusal)", entry.State())
	}
	if _, busy := gate.Busy("foo", rt.Messages.Running(), store); !busy {
		t.Error("foo's running job vanished on a refused deactivation")
	}
}

// TestDeactivate_ProceedsWhenIdle proves the happy path past the ops-gate: with
// no reservation held, Deactivate runs to completion, resets the module to
// StateReady, and reports no error. The footprint Unregister on the gate is
// exercised on this path (asserted behaviourally in the gate package).
func TestDeactivate_ProceedsWhenIdle(t *testing.T) {
	entry := module.NewRecord(module.StateActive, &manifest.Manifest{})
	store := module.NewStore()
	store.Put("foo", entry)
	rt := &Runtime{
		Store:    store,
		Messages: messages.New(),
	}

	if err := rt.lc().Deactivate("foo", entry); err != nil {
		t.Fatalf("Deactivate: unexpected error for an idle module: %v", err)
	}
	if entry.State() != module.StateReady {
		t.Errorf("entry.State = %q; want StateReady", entry.State())
	}
}

// TestDeactivateModule_BusyModuleStaysActive proves the operator-driven hot-unload:
// a module whose flag was just dropped but is still running is deactivated
// directly; when the gate refuses, the module stays active and the refusal
// surfaces as a single warning the home page can show. The operator retries once
// the job frees it.
func TestDeactivateModule_BusyModuleStaysActive(t *testing.T) {
	entry := module.NewRecord(module.StateActive, nil)
	store := module.NewStore()
	store.Put("foo", entry)

	rt := &Runtime{Store: store, Messages: busyMessages(t, "foo", "job-1")}

	// Flag dropped on disk, still running in memory (StateActive).
	rt.DeactivateModule("foo")

	if entry.State() != module.StateActive {
		t.Errorf("entry.State = %q; want StateActive (deactivation refused)", entry.State())
	}
	if len(rt.Warns) != 1 {
		t.Fatalf("Warns = %v; want exactly one refusal warning", rt.Warns)
	}
	if !strings.Contains(rt.Warns[0], "busy") {
		t.Errorf("warning = %q; want it to mention the module is busy", rt.Warns[0])
	}
}

// TestDeactivateModule_FailedResetsToReady proves the recovery path through
// the direct hot-unload: deactivating a failed module (its flag just dropped)
// clears the failed verdict back to ready so the operator can re-activate it.
func TestDeactivateModule_FailedResetsToReady(t *testing.T) {
	const reason = "health checks failing; 5 restart attempts exhausted"
	entry := module.NewRecord(module.StateFailed, nil)
	entry.SetStatus(module.StateFailed, reason)
	store := module.NewStore()
	store.Put("foo", entry)
	rt := &Runtime{Store: store, Messages: messages.New()}

	rt.DeactivateModule("foo")

	if entry.State() != module.StateReady {
		t.Errorf("State = %q; want StateReady", entry.State())
	}
	if entry.Reason() != "" {
		t.Errorf("Reason = %q; want empty", entry.Reason())
	}
	if len(rt.Warns) != 0 {
		t.Errorf("unexpected warnings: %v", rt.Warns)
	}
}

// TestActivateModule_FailedIsTerminal proves a failed module is not re-activated
// by the direct hot-load: the failed verdict is terminal until the operator
// recovers it via deactivate, so ActivateModule must leave it untouched.
func TestActivateModule_FailedIsTerminal(t *testing.T) {
	const reason = "health checks failing; 5 restart attempts exhausted"
	entry := module.NewRecord(module.StateFailed, nil)
	entry.SetStatus(module.StateFailed, reason)
	store := module.NewStore()
	store.Put("foo", entry)
	rt := &Runtime{Store: store, Messages: messages.New()}

	rt.ActivateModule("foo")

	if entry.State() != module.StateFailed {
		t.Errorf("State = %q; want StateFailed (terminal)", entry.State())
	}
	if entry.Reason() != reason {
		t.Errorf("Reason = %q; want %q", entry.Reason(), reason)
	}
	if len(rt.Warns) != 0 {
		t.Errorf("unexpected warnings: %v", rt.Warns)
	}
}
