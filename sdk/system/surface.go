// SPDX-License-Identifier: Apache-2.0

package system

import (
	_ "embed"
	"fmt"

	"github.com/solutionsunity/suctl/sdk/manifest"
)

// Subject identifiers for the core surfaces declared in surface.json. Faces and
// core BIST address a core surface by its subject, never by ordinal position.
const (
	SubjectModule  = "module"
	SubjectJob     = "job"
	SubjectMessage = "message"
)

//go:embed surface.json
var coreSurfaceJSON []byte

// coreSurfaces is the parsed, ordered set of control-plane surfaces (module,
// job, message). It is the single source of truth for the core's own presentation,
// consumed by every face (REPL today, gRPC / HTTP tomorrow) and by core BIST —
// the analogue of a module's surface.json, but owned by core and compiled in.
// It is decoded once at init from the embedded surface.json; a decode failure is
// a build-time invariant violation, so init panics rather than ship a binary
// whose own surfaces are malformed.
var coreSurfaces []manifest.SurfaceConfig

func init() {
	s, err := manifest.ParseSurface(coreSurfaceJSON)
	if err != nil {
		panic(fmt.Sprintf("sdk/system: embedded surface.json invalid: %v", err))
	}
	coreSurfaces = s
}

// Surfaces returns a copy of the ordered core surfaces declared in surface.json.
// The copy keeps the embedded source of truth immutable to callers.
func Surfaces() []manifest.SurfaceConfig {
	return append([]manifest.SurfaceConfig(nil), coreSurfaces...)
}

// Surface returns a copy of the core surface for subject, or nil if none is
// declared. Callers address core surfaces by subject (SubjectModule, SubjectJob,
// SubjectMessage).
func Surface(subject string) *manifest.SurfaceConfig {
	for i := range coreSurfaces {
		if coreSurfaces[i].Subject == subject {
			s := coreSurfaces[i]
			return &s
		}
	}
	return nil
}
