// SPDX-License-Identifier: Apache-2.0

//go:build windows

package paths

import (
	"os"
	"path/filepath"
)

// Windows base directories anchor the same layout as Unix under %ProgramData%
// (machine-wide, non-roaming) — the conventional home for service state and
// configuration. There is no /usr equivalent, so shipped and third-party modules
// live under the same %ProgramData%\suctl tree, distinguished by sub-directory.
// stateBase has its own \state subtree so operator state (e.g. module activation
// flags under \state\modules) never collides with installed modules (\modules).
var (
	programData          = programDataDir()
	configBase           = filepath.Join(programData, "suctl")
	logBase              = filepath.Join(programData, "suctl", "log")
	stateBase            = filepath.Join(programData, "suctl", "state")
	runBase              = filepath.Join(programData, "suctl", "run")
	webrootBase          = filepath.Join(programData, "suctl", "www")
	binBase              = filepath.Join(programData, "suctl", "bin")
	systemModuleBase     = filepath.Join(programData, "suctl", "modules")
	thirdPartyModuleBase = filepath.Join(programData, "suctl", "modules-thirdparty")
)

// exeSuffix is the platform executable extension.
const exeSuffix = ".exe"

// purgeDirs returns the operator-data roots removed by `suctl uninstall --purge`.
// On Windows the whole layout lives under a single %ProgramData%\suctl tree, so
// removing that one root clears everything.
func purgeDirs() []string {
	return []string{filepath.Join(programData, "suctl")}
}

// programDataDir returns %ProgramData%, falling back to C:\ProgramData when the
// environment variable is unset (it is set on every supported Windows install).
func programDataDir() string {
	if d := os.Getenv("ProgramData"); d != "" {
		return d
	}
	return `C:\ProgramData`
}
