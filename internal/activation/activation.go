// SPDX-License-Identifier: Apache-2.0

// Package activation manages module activation state.
//
// Activation state lives in /var/lib/suctl/modules/ as flag files — one file
// per activated module named {module-short-name}.flag. The file's presence
// signals "activated"; its content records the checksum of the installed
// module directory as it was at the last successful activation, so a boot can
// tell whether the code behind an activation changed since (D72). A flag may be
// empty (legacy, or activated before the checksum was first recorded). The
// representation is independently auditable by advanced operators.
//
// The operator's interface to activation state is suctl itself. These files
// are not documented as a public API — they are implementation detail, just
// as /var/lib/dpkg/ is for apt.
package activation

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/solutionsunity/suctl/sdk/paths"
)

// Dir is the directory where activation flag files are stored, resolved per-OS
// by sdk/paths (the single source of truth for all suctl paths).
var Dir = paths.ModuleStateDir

// flagPath returns the absolute path of the flag file for a module short name.
func flagPath(stateDir, shortName string) (string, error) {
	if shortName == "" {
		return "", errors.New("activation: module short name must not be empty")
	}
	return filepath.Join(stateDir, shortName+".flag"), nil
}

// IsActivated reports whether the module with the given short name has a flag
// file in stateDir. A missing file means not activated — not an error.
func IsActivated(stateDir, shortName string) (bool, error) {
	path, err := flagPath(stateDir, shortName)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("activation: stat %s: %w", path, err)
}

// Activate creates the flag file for the module. If the file already exists
// the call is a no-op (idempotent). The stateDir is created if needed.
func Activate(stateDir, shortName string) error {
	path, err := flagPath(stateDir, shortName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return fmt.Errorf("activation: mkdir %s: %w", stateDir, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil // already activated — idempotent
		}
		return fmt.Errorf("activation: create flag %s: %w", path, err)
	}
	return f.Close()
}

// coreNames lists names that cannot be deactivated via the activation package.
// System capabilities are core — not modules — and have no activation flag.
// Note: name "system" must match system.ShortName.
var coreNames = map[string]bool{
	"system": true,
}

// Deactivate removes the flag file for the module. If the file does not exist
// the call is a no-op (idempotent). Returns an error for core names that
// must not be managed through the activation flag mechanism.
func Deactivate(stateDir, shortName string) error {
	if coreNames[shortName] {
		return fmt.Errorf("activation: %q is a core capability, not a module — it cannot be deactivated", shortName)
	}
	path, err := flagPath(stateDir, shortName)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("activation: remove flag %s: %w", path, err)
}

// List returns the short names of all currently activated modules in stateDir.
// A missing or empty directory returns an empty slice — not an error.
func List(stateDir string) ([]string, error) {
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("activation: readdir %s: %w", stateDir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if filepath.Ext(name) == ".flag" {
			names = append(names, name[:len(name)-len(".flag")])
		}
	}
	return names, nil
}

// Checksum computes a content fingerprint of an installed module directory: a
// SHA-256 over every regular file (and symlink) in the tree, fed in sorted
// relative-path order as path + mode bits + content (symlink: path + mode +
// target). It is the honest "did the installed code change" signal — independent
// of the manifest version, which an author may forget to bump (D72). mtime is
// excluded (not content; copying rewrites it). The same pure function is the one
// both first activation and boot use, so they can never disagree.
func Checksum(moduleDir string) (string, error) {
	var files []string
	err := filepath.Walk(moduleDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("activation: checksum walk %s: %w", moduleDir, err)
	}
	sort.Strings(files)

	h := sha256.New()
	for _, path := range files {
		rel, err := filepath.Rel(moduleDir, path)
		if err != nil {
			return "", fmt.Errorf("activation: checksum rel %s: %w", path, err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return "", fmt.Errorf("activation: checksum lstat %s: %w", path, err)
		}
		fmt.Fprintf(h, "%s\x00%o\x00", rel, info.Mode())
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return "", fmt.Errorf("activation: checksum readlink %s: %w", path, err)
			}
			io.WriteString(h, target) //nolint:errcheck
		} else {
			f, err := os.Open(path)
			if err != nil {
				return "", fmt.Errorf("activation: checksum open %s: %w", path, err)
			}
			if _, err := io.Copy(h, f); err != nil {
				f.Close() //nolint:errcheck
				return "", fmt.Errorf("activation: checksum read %s: %w", path, err)
			}
			f.Close() //nolint:errcheck
		}
		h.Write([]byte{0}) //nolint:errcheck
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// GetChecksum returns the checksum recorded in a module's flag file. A missing
// flag or an empty flag (legacy, or never recorded) returns "" with no error —
// the caller treats an empty stored checksum as "unknown history" and does not
// infer an upgrade from it.
func GetChecksum(stateDir, shortName string) (string, error) {
	path, err := flagPath(stateDir, shortName)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("activation: read flag %s: %w", path, err)
	}
	return strings.TrimSpace(string(b)), nil
}

// SetChecksum records the checksum as the content of a module's flag file,
// creating the flag (and stateDir) if needed. Writing the checksum is what marks
// "this exact installed content was successfully activated"; it is rewritten only
// after a successful activation so a failed upgrade leaves the prior value in
// place and the next boot retries (D72).
func SetChecksum(stateDir, shortName, checksum string) error {
	path, err := flagPath(stateDir, shortName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return fmt.Errorf("activation: mkdir %s: %w", stateDir, err)
	}
	if err := os.WriteFile(path, []byte(checksum+"\n"), 0644); err != nil {
		return fmt.Errorf("activation: write flag %s: %w", path, err)
	}
	return nil
}
