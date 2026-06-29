// SPDX-License-Identifier: Apache-2.0

// Package bootstrap ensures all paths suctl requires exist before any mode
// of the binary starts.  It is safe to call multiple times (idempotent) and
// must be the first call in cmd/main.go, before mode dispatch.
//
// bootstrap may import the config package but must never import logging.
// logging.Init() runs after bootstrap so the log directory is guaranteed to
// exist when the log file is opened.  config has no logging dependency, so
// this import chain is safe.
package bootstrap

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/solutionsunity/suctl/internal/config"
	"github.com/solutionsunity/suctl/sdk/paths"
)

// These mirror sdk/paths (the single source of truth for all suctl paths),
// which resolves each location per-OS. They are vars, not consts, because the
// Windows bases are computed from the environment at init.
var (
	// ConfigDir is the base directory for suctl configuration.
	ConfigDir = paths.ConfigDir

	// ServicesDir holds one .conf file per registered service.
	ServicesDir = paths.ServicesDir

	// NginxDir holds per-domain location include files (.location) for A04.
	// These files are the ownership markers for suctl-managed nginx domains.
	NginxDir = paths.NginxDir

	// ModuleConfDir is the .d directory where each module drops its own
	// operator-editable config file.
	ModuleConfDir = paths.ModuleConfDir

	// LogDir is the shared log directory for all suctl components.
	// It is owned by odoo:odoo so suctl-odoo (running as the odoo user)
	// can also write its own log file there.
	LogDir = paths.LogDir

	// ModuleStateDir is where suctl writes per-module activation flag files.
	// One empty file per activated module: {ModuleStateDir}/{module-name}.flag
	// Written by suctl in response to operator action; not edited directly.
	ModuleStateDir = paths.ModuleStateDir

	// RunDir is the parent directory for core-owned runtime state.
	RunDir = paths.RunDir

	// WebrootSuspended is where nginx serves the suspension page from.
	WebrootSuspended = paths.WebrootSuspended

	// WebrootMaintenance is where nginx serves the maintenance page from.
	WebrootMaintenance = paths.WebrootMaintenance

	// SystemModulePath is the install location for all suctl-shipped modules.
	// Must match module.SystemModulePath — duplicated here to avoid an import cycle.
	// `suctl install` copies all repo modules here; this path MUST exist at runtime.
	SystemModulePath = paths.SystemModulePath

	// ThirdPartyModulePath is the default location for operator-installed modules
	// not shipped with suctl. Must match module.DefaultThirdPartyPath.
	// suctl creates this directory on startup if it does not already exist.
	ThirdPartyModulePath = paths.ThirdPartyModulePath
)

// Run creates all directories and static files suctl requires at startup.
//
// suctl runs with the elevated privileges required to manage system paths, so it
// can create them without restriction.  Any error is silently ignored —
// a subsequent call to logging.Init() will surface problems with the log
// directory when it tries to open the log file.
//
// Static HTML pages are written only when they do not already exist, so
// operators can safely customise them without their changes being overwritten.
func Run() {
	// /etc/suctl/services.d/ — root:root 0755
	os.MkdirAll(ServicesDir, 0755) //nolint:errcheck

	// /etc/suctl/nginx/ — per-domain location files and metadata
	os.MkdirAll(NginxDir, 0755) //nolint:errcheck

	// /etc/suctl/conf.d/ — one config file per module, operator-editable
	os.MkdirAll(ModuleConfDir, 0755) //nolint:errcheck

	// /var/lib/suctl/modules/ — activation flag files written by suctl
	os.MkdirAll(ModuleStateDir, 0755) //nolint:errcheck

	// /run/suctl/ — parent directory for core-owned runtime state.
	os.MkdirAll(RunDir, 0755) //nolint:errcheck

	// /usr/local/lib/suctl/modules/ — third-party module directory.
	// Created on first run if absent; not an error if the operator has not
	// placed any third-party modules there yet.
	os.MkdirAll(ThirdPartyModulePath, 0755) //nolint:errcheck

	// LogDir — must be writable by the odoo user so suctl-odoo can create
	// suctl-odoo.log alongside suctl.log. Ownership is applied through the
	// OS-specific seam (chownToUser): a real chown on Unix, a no-op on Windows
	// where directories inherit their parent ACL.
	if os.MkdirAll(LogDir, 0750) == nil {
		chownToUser(LogDir, "odoo")
	}

	// Write paths.json for non-Go consumers (Dxx).
	if data, err := paths.ToJSON(); err == nil {
		os.WriteFile(filepath.Join(ConfigDir, "paths.json"), data, 0644) //nolint:errcheck
	}

	// Static webroot directories and HTML pages.
	// Pages are rendered once from embedded templates; never overwritten
	// if already present so operator customisations are preserved.
	cfg := config.Load()
	ensurePage(WebrootSuspended+"/index.html", renderPage(suspendedHTML, cfg))
	ensurePage(WebrootMaintenance+"/index.html", renderPage(maintenanceHTML, cfg))
}

// renderPage replaces {{LOGO_URL}} and {{CONTACT_URL}} in a template string.
func renderPage(tmpl string, cfg *config.Config) string {
	r := strings.NewReplacer(
		"{{LOGO_URL}}", cfg.LogoURL,
		"{{CONTACT_URL}}", cfg.ContactURL,
	)
	return r.Replace(tmpl)
}

// ensurePage writes content to path only if path does not already exist.
// The parent directory is created if needed.
func ensurePage(path, content string) {
	if _, err := os.Stat(path); err == nil {
		return // file already exists — leave operator customisation intact
	}
	dir := filepath.Dir(path)
	if os.MkdirAll(dir, 0755) != nil {
		return
	}
	os.WriteFile(path, []byte(content), 0644) //nolint:errcheck
}
