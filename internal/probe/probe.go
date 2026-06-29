// SPDX-License-Identifier: Apache-2.0

package probe

import (
	"os"

	"github.com/solutionsunity/suctl/sdk/paths"
)

// Run tests runtime connectivity and directory health; returns human-readable
// warnings. An empty return means everything is available.
//
// Call after logging.Init() — callers should log each warning with slog.Warn
// and surface them to the operator where appropriate (e.g. REPL startup banner).
//
// Run never returns an error: individual failures become warnings so the
// binary starts and the operator sees a clear message rather than an abort.
func Run() []string {
	var warns []string

	// System module path — all suctl-shipped modules live here.
	// Installed by `suctl install`; missing means the installation is broken.
	if _, err := os.Stat(paths.SystemModulePath); err != nil {
		warns = append(warns, "system module path missing — run `sudo suctl install`: "+paths.SystemModulePath)
	}

	// systemd-backed connectivity (D-Bus, journald) is the only OS-specific
	// probe; runtimeWarnings supplies it per platform (linux vs. stub).
	warns = append(warns, runtimeWarnings()...)

	return warns
}
