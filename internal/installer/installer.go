// SPDX-License-Identifier: Apache-2.0

package installer

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/solutionsunity/suctl/internal/activation"
	"github.com/solutionsunity/suctl/internal/privilege"
	"github.com/solutionsunity/suctl/internal/theme"
	"github.com/solutionsunity/suctl/sdk/manifest"
	"github.com/solutionsunity/suctl/sdk/paths"
)

// Install locations are single-sourced from sdk/paths (resolved per-OS):
//   - paths.SuctlBin          — the installed suctl binary
//   - paths.SystemModulePath  — the system-shipped modules directory
//   - paths.PurgeDirs()       — operator-data roots removed by --purge

// IsInstalled reports whether suctl has been installed on this system.
// The canonical signal is the presence of the system binary at paths.SuctlBin —
// that file is created exclusively by Run() and removed by Uninstall().
func IsInstalled() bool {
	_, err := os.Stat(paths.SuctlBin)
	return err == nil
}

// IsInstalledBinary reports whether the currently running executable IS the
// installed binary at paths.SuctlBin (as opposed to a downloaded release copy).
// This is the identity question asked before IsInstalled: only the installed
// binary boots suctl; a release copy exists solely to install or to redirect the
// operator to the installed binary. Both paths are resolved through symlinks so a
// symlinked launch still matches. Returns false when suctl is not installed.
func IsInstalledBinary() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	return samePath(exe, paths.SuctlBin)
}

// samePath reports whether a and b reference the same file. Both are resolved
// through symlinks first (which also canonicalises casing on case-insensitive
// filesystems); when a path cannot be resolved — e.g. paths.SuctlBin when suctl
// is not installed — it falls back to the cleaned form, which will not match.
func samePath(a, b string) bool {
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		ra = filepath.Clean(a)
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		rb = filepath.Clean(b)
	}
	return ra == rb
}

// Run executes `suctl install`. Requires root.
//
// Installs the suctl binary to its system path, then copies all suctl-shipped
// modules into the system module directory. Module-specific system wiring is
// handled by each module's own lifecycle hooks — not by core install.
//
// bootstrap.Run() has already been called by main() before this runs, so all
// suctl directories (resolved per-OS by sdk/paths) are guaranteed to exist.
func Run(args []string) {
	if !privilege.IsAdmin() {
		die("must run with administrative privilege — re-run with " + privilege.EscalationHint())
	}

	header("install")

	// Locate the running binary so we can find the modules/ directory next to it.
	exe, err := os.Executable()
	must(err, "locate current executable")
	execDir := filepath.Dir(exe)

	// 1. Install the suctl binary to its system path.
	data, err := os.ReadFile(exe)
	must(err, "read current executable")
	must(os.MkdirAll(paths.BinDir, 0755), "create binary directory")
	must(os.WriteFile(paths.SuctlBin, data, 0755), "write suctl binary")
	step("%s", paths.SuctlBin)

	// Ensure the install directory is on PATH so `suctl` is runnable from any
	// shell. No-op on Unix (/usr/local/bin is already on PATH); on Windows this
	// adds %ProgramData%\suctl\bin to the machine PATH.
	changed, err := registerPath()
	must(err, "add "+paths.BinDir+" to PATH")
	if changed {
		step("added %s to PATH (open a new terminal to use suctl)", paths.BinDir)
	}

	// 2. Install whatever suctl-shipped modules sit next to the binary into the
	//    system module directory. Every subdirectory under modules/ is a module;
	//    the set is decided once at build time (Makefile), so this path copies
	//    whatever is present and is identical on every OS. A platform that ships
	//    zero modules (no modules/ dir) installs the binary alone — not an error.
	modulesDir := filepath.Join(execDir, "modules")
	entries, err := os.ReadDir(modulesDir)
	if err != nil && !os.IsNotExist(err) {
		die("read modules directory: " + err.Error())
	}
	must(os.MkdirAll(paths.SystemModulePath, 0755), "create system module directory")
	installed := 0
	var active []string
	for _, de := range entries {
		if !de.IsDir() {
			continue
		}
		src := filepath.Join(modulesDir, de.Name())
		dst := filepath.Join(paths.SystemModulePath, de.Name())
		must(copyDir(src, dst), "install "+de.Name())
		step("%s/", dst)
		installed++

		// install only copies files — it never drives lifecycle (that belongs to
		// the running core, a different process). When a module being overwritten
		// is already activated, note it so the operator knows a restart is needed
		// for any change to take effect; the running core reconciles the changed
		// content on its next boot (D72). Keyed on flag presence alone — install
		// does not compute checksums or assert that anything actually changed.
		short := manifest.ShortNameFromDir(de.Name())
		if ok, _ := activation.IsActivated(paths.ModuleStateDir, short); ok {
			active = append(active, short)
		}
	}
	if installed == 0 {
		step("no modules bundled — binary installed without modules")
	}

	fmt.Println()
	fmt.Printf("  %s  run %s to start.\n",
		theme.Success.Render("installed."),
		theme.Code.Render("suctl"),
	)
	for _, short := range active {
		fmt.Printf("  %s  module %s is active — restart suctl to apply any changes.\n",
			theme.Dim.Render("•"),
			theme.Code.Render(short),
		)
	}
}

// Uninstall executes `suctl uninstall`. Requires root.
//
// Base (no flags): removes the suctl binary and the system modules directory.
// Configuration, logs, and operator state are left intact.
//
// --purge: additionally removes all suctl state directories and configuration.
func Uninstall(args []string) {
	fs := flag.NewFlagSet("suctl uninstall", flag.ExitOnError)
	purge := fs.Bool("purge", false, "also remove config, state dirs, and logs")
	fs.Parse(args) //nolint:errcheck

	if !privilege.IsAdmin() {
		die("must run with administrative privilege — re-run with " + privilege.EscalationHint())
	}

	header("uninstall")

	removeDir(paths.SystemModulePath)
	removeFile(paths.SuctlBin)

	// Reverse the PATH entry added by install. No-op on Unix; best-effort on
	// Windows — a stale entry pointing at a removed dir is harmless, so a
	// failure here is reported but does not abort the uninstall.
	if changed, err := unregisterPath(); err != nil {
		fmt.Fprintf(os.Stderr, "  %s  remove %s from PATH: %v\n", theme.Error.Render("✗"), paths.BinDir, err)
	} else if changed {
		step("removed %s from PATH", paths.BinDir)
	}

	if !*purge {
		fmt.Println()
		fmt.Printf("  %s  run with %s to also remove config, state dirs, and logs.\n",
			theme.Success.Render("removed."),
			theme.Code.Render("--purge"),
		)
		return
	}

	for _, dir := range paths.PurgeDirs() {
		removeDir(dir)
	}

	fmt.Println()
	fmt.Printf("  %s\n", theme.Success.Render("fully removed."))
}

// ── helpers ───────────────────────────────────────────────────────────────────

func header(mode string) {
	fmt.Println()
	fmt.Println("  " + theme.Title.Render("suctl") + "  " + theme.Dim.Render("— "+mode))
	fmt.Println()
}

func step(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Printf("  %s  %s\n", theme.Success.Render("✓"), theme.Dim.Render(msg))
}

func die(msg string) {
	fmt.Fprintf(os.Stderr, "\n  %s  %s\n\n", theme.Error.Render("✗"), msg)
	os.Exit(1)
}

func must(err error, msg string) {
	if err != nil {
		die(msg + ": " + err.Error())
	}
}

// removeFile removes a single file. Missing files are silently ignored — idempotent.
func removeFile(path string) bool {
	if err := os.Remove(path); err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "  %s  remove %s: %v\n", theme.Error.Render("✗"), path, err)
		}
		return false
	}
	step("%s", path)
	return true
}

// removeDir removes a directory tree. Missing directories are silently ignored.
func removeDir(path string) {
	if err := os.RemoveAll(path); err != nil {
		fmt.Fprintf(os.Stderr, "  %s  remove %s: %v\n", theme.Error.Render("✗"), path, err)
		return
	}
	step("%s/", path)
}

// copyDir copies all files from src into dst recursively, creating dst if needed.
// Existing files in dst are overwritten. Permissions are preserved.
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return copyFile(path, target, info.Mode())
	})
}

// copyFile copies a single file from src to dst with the given permissions.
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
