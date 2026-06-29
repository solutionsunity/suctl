// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package paths

// Unix base directories follow the Filesystem Hierarchy Standard. These are the
// roots from which paths.go derives the full layout; the Windows counterpart
// (paths_windows.go) anchors the same layout under %ProgramData%.
//
// This file also covers macOS (darwin): it currently reuses the Unix layout
// (/etc, /var, /usr/local) rather than the native /Library locations. A dedicated
// darwin layout can be split out later without touching the derived names.
const (
	configBase           = "/etc/suctl"
	logBase              = "/var/log/suctl"
	stateBase            = "/var/lib/suctl"
	runBase              = "/run/suctl"
	webrootBase          = "/var/www/suctl"
	binBase              = "/usr/local/bin"
	systemModuleBase     = "/usr/lib/suctl/modules"
	thirdPartyModuleBase = "/usr/local/lib/suctl/modules"

	// exeSuffix is the platform executable extension (none on Unix).
	exeSuffix = ""
)

// purgeDirs returns the operator-data roots removed by `suctl uninstall --purge`.
// On Unix these are the separate FHS trees; the binary and system modules are
// removed by the base uninstall, not here.
func purgeDirs() []string {
	return []string{configBase, stateBase, logBase, webrootBase}
}
