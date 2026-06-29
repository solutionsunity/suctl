// SPDX-License-Identifier: Apache-2.0

// Package paths is the canonical, OS-aware filesystem-layout resolver for suctl.
//
// It is the single source of truth for every suctl path, shared by the core
// broker and external modules so the two never drift. The per-OS base
// directories live in paths_unix.go and paths_windows.go (Unix follows the FHS:
// /etc, /var, /run, /usr; Windows anchors the same layout under %ProgramData%);
// this file derives the full layout from those bases exactly once. Because the
// Windows bases are computed from the environment, these are package vars, not
// consts — consumers that mirror them must declare vars too.
package paths

import (
	"encoding/json"
	"path/filepath"
)

var (
	// ConfigDir is the base directory for suctl configuration.
	ConfigDir = configBase

	// ServicesDir holds one .conf file per registered service.
	ServicesDir = filepath.Join(configBase, "services.d")

	// NginxDir holds per-domain location include files (.location) for A04.
	NginxDir = filepath.Join(configBase, "nginx")

	// ModuleConfDir is the .d directory where each module drops its own
	// operator-editable config file.
	ModuleConfDir = filepath.Join(configBase, "conf.d")

	// ConfigFile is the canonical path of the main suctl configuration file.
	ConfigFile = filepath.Join(configBase, "suctl.conf")

	// LogDir is the shared log directory for all suctl components.
	LogDir = logBase

	// ModuleStateDir is where suctl writes per-module activation flag files.
	ModuleStateDir = filepath.Join(stateBase, "modules")

	// RunDir is the parent directory for all runtime state owned by core.
	RunDir = runBase

	// WebrootSuspended is where nginx serves the suspension page from.
	WebrootSuspended = filepath.Join(webrootBase, "suspended")

	// WebrootMaintenance is where nginx serves the maintenance page from.
	WebrootMaintenance = filepath.Join(webrootBase, "maintenance")

	// SystemModulePath is the install location for all suctl-shipped modules.
	SystemModulePath = systemModuleBase

	// ThirdPartyModulePath is the location for operator-installed modules.
	ThirdPartyModulePath = thirdPartyModuleBase

	// BinDir is the directory the suctl binary is installed into by `suctl install`.
	BinDir = binBase

	// SuctlBin is the full path of the installed suctl binary (with the
	// platform executable suffix).
	SuctlBin = filepath.Join(binBase, "suctl"+exeSuffix)
)

// PurgeDirs returns the operator-data directories removed by
// `suctl uninstall --purge`, resolved per-OS (the single source of truth).
func PurgeDirs() []string { return purgeDirs() }

// All returns all canonical paths as a map for easy JSON serialization.
func All() map[string]string {
	return map[string]string{
		"config_dir":              ConfigDir,
		"config_file":             ConfigFile,
		"services_d":              ServicesDir,
		"nginx_dir":               NginxDir,
		"module_conf_d":           ModuleConfDir,
		"log_dir":                 LogDir,
		"module_state_dir":        ModuleStateDir,
		"run_dir":                 RunDir,
		"webroot_suspended":       WebrootSuspended,
		"webroot_maintenance":     WebrootMaintenance,
		"system_module_path":      SystemModulePath,
		"third_party_module_path": ThirdPartyModulePath,
		"bin_dir":                 BinDir,
		"suctl_bin":               SuctlBin,
	}
}

// ToJSON returns the JSON representation of all canonical paths.
func ToJSON() ([]byte, error) {
	return json.MarshalIndent(All(), "", "  ")
}
