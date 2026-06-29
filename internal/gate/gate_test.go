// SPDX-License-Identifier: Apache-2.0

package gate_test

import (
	"testing"

	"github.com/solutionsunity/suctl/internal/gate"
	"github.com/solutionsunity/suctl/internal/messages"
	"github.com/solutionsunity/suctl/internal/module"
	"github.com/solutionsunity/suctl/sdk/manifest"
)

// fpStore builds a modules store whose footprints match deps: each key is a
// module short name and its value lists the modules it reaches via
// requires.capabilities. Every named module (key or member) gets an active
// record that provides its own "<name>.cap" capability, so module.Footprint
// resolves each declared dependency back to its provider.
func fpStore(deps map[string][]string) *module.Store {
	store := module.NewStore()
	ensure := func(name string) *module.Record {
		if r, ok := store.Get(name); ok {
			return r
		}
		r := module.NewRecord(module.StateActive, &manifest.Manifest{
			Capabilities: []manifest.Capability{{Name: name + ".cap"}},
		})
		store.Put(name, r)
		return r
	}
	for name, members := range deps {
		r := ensure(name)
		for _, m := range members {
			ensure(m)
			r.Manifest.Requires.Capabilities = append(r.Manifest.Requires.Capabilities, m+".cap")
		}
	}
	return store
}

// set is a small footprint literal helper.
func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// TestAdmissible: a footprint disjoint from busy is admissible; any overlap is
// not; an empty footprint or empty busy set always admits.
func TestAdmissible(t *testing.T) {
	busy := set("nginx", "certbot")

	if !gate.Admissible(set("os"), busy) {
		t.Error("disjoint footprint should be admissible")
	}
	if gate.Admissible(set("certbot"), busy) {
		t.Error("overlapping footprint must not be admissible")
	}
	if gate.Admissible(set("nginx", "os"), busy) {
		t.Error("partial overlap must not be admissible")
	}
	if !gate.Admissible(set(), busy) {
		t.Error("empty footprint is always admissible")
	}
	if !gate.Admissible(set("nginx"), set()) {
		t.Error("empty busy set admits anything")
	}
}

// TestBusy: a module is busy when it falls inside any running job's footprint —
// directly (the job's own module) or transitively (a module the job reaches).
func TestBusy(t *testing.T) {
	store := fpStore(map[string][]string{"nginx": {"certbot"}, "os": nil})
	running := []messages.Job{{Token: "t1", Module: "nginx"}} // footprint {nginx, certbot}

	if tok, busy := gate.Busy("nginx", running, store); !busy || tok != "t1" {
		t.Errorf("nginx busy = %q,%v; want t1,true (the running job's own module)", tok, busy)
	}
	if tok, busy := gate.Busy("certbot", running, store); !busy || tok != "t1" {
		t.Errorf("certbot busy = %q,%v; want t1,true (inside nginx's footprint)", tok, busy)
	}
	if _, busy := gate.Busy("os", running, store); busy {
		t.Error("os is outside every running footprint; must not be busy")
	}
	if _, busy := gate.Busy("nginx", nil, store); busy {
		t.Error("no running jobs ⇒ nothing is busy")
	}
}
