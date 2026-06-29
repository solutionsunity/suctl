// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"os"

	"github.com/solutionsunity/suctl/sdk/manifest"
)

// HookDecl is a re-export of the manifest hook declaration.
type HookDecl = manifest.HookDecl

// Env holds the standard environment variables passed to hook scripts.
type Env struct {
	Module    string
	Event     string
	ModuleDir string
	ConfDir   string
}

// GetEnv returns the current hook environment from process environment variables.
func GetEnv() Env {
	return Env{
		Module:    os.Getenv("SUCTL_MODULE"),
		Event:     os.Getenv("SUCTL_EVENT"),
		ModuleDir: os.Getenv("SUCTL_MODULE_DIR"),
		ConfDir:   os.Getenv("SUCTL_CONF_DIR"),
	}
}

// RequirementEnv holds the additional variables passed to on-requirement-missing.
type RequirementEnv struct {
	Env
	Type  string
	Value string
}

// GetRequirementEnv returns the current requirement hook environment.
func GetRequirementEnv() RequirementEnv {
	return RequirementEnv{
		Env:   GetEnv(),
		Type:  os.Getenv("SUCTL_REQ_TYPE"),
		Value: os.Getenv("SUCTL_REQ_VALUE"),
	}
}
