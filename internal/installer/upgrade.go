// SPDX-License-Identifier: Apache-2.0

package installer

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/solutionsunity/suctl/internal/privilege"
	"github.com/solutionsunity/suctl/internal/theme"
	"github.com/solutionsunity/suctl/internal/version"
	"github.com/solutionsunity/suctl/sdk/paths"
)

// Upgrade executes `suctl upgrade`. Requires root.
//
// Resolves the latest published release (or an explicit SUCTL_VERSION pin),
// compares it against the running build, and — if newer — downloads the matching
// archive, verifies its sha256, swaps the installed binary in place (Unix
// inode-swap / Windows self-rename dance, see replaceBinary), and refreshes the
// system modules. The module setup/upgrade hooks are NOT run here: they fire on
// the operator's next `suctl` invocation via the core's startup drift check.
func Upgrade(args []string) {
	fs := flag.NewFlagSet("suctl upgrade", flag.ExitOnError)
	yes := fs.Bool("y", false, "skip the confirmation prompt")
	fs.Parse(args) //nolint:errcheck

	if !privilege.IsAdmin() {
		die("must run with administrative privilege — re-run with " + privilege.EscalationHint())
	}

	header("upgrade")

	repo := os.Getenv("SUCTL_REPO")
	if repo == "" {
		repo = "solutionsunity/suctl"
	}

	// Resolve target tag: honour an explicit pin, else the latest release.
	tag := os.Getenv("SUCTL_VERSION")
	if tag == "" {
		var err error
		tag, err = resolveLatestTag(repo)
		must(err, "resolve the latest version")
	}

	current := version.Version
	cmp, err := compareSemver(tag, current)
	must(err, "compare versions")
	if cmp <= 0 {
		fmt.Printf("  %s  already on the latest version (%s).\n",
			theme.Success.Render("up to date"), theme.Code.Render(current))
		return
	}

	fmt.Printf("  %s  %s → %s\n", theme.Dim.Render("update available"),
		theme.Code.Render(current), theme.Code.Render(tag))

	if !*yes && !confirm("  proceed with upgrade? [y/N] ") {
		fmt.Printf("  %s\n", theme.Dim.Render("cancelled."))
		return
	}

	goos, goarch := runtime.GOOS, runtime.GOARCH
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	name := fmt.Sprintf("suctl-%s-%s-%s.%s", tag, goos, goarch, ext)
	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, tag, name)

	tmp, err := os.MkdirTemp("", "suctl-upgrade-")
	must(err, "create temp directory")
	defer os.RemoveAll(tmp) //nolint:errcheck

	archive := filepath.Join(tmp, name)
	must(download(url, archive), "download "+name)
	must(download(url+".sha256", archive+".sha256"), "download checksum")
	must(verifySHA256(archive, archive+".sha256"), "verify checksum")
	step("checksum ok")

	must(extractArchive(archive, tmp), "extract "+name)
	srcDir := filepath.Join(tmp, fmt.Sprintf("suctl-%s-%s-%s", tag, goos, goarch))

	// 1. Swap the binary (handles the running-process file lock per-OS).
	srcBin := filepath.Join(srcDir, filepath.Base(paths.SuctlBin))
	if _, err := os.Stat(srcBin); err != nil {
		die("extracted archive is missing the suctl binary")
	}
	must(replaceBinary(srcBin, paths.SuctlBin), "replace suctl binary")
	step("%s", paths.SuctlBin)

	// 2. Refresh system modules from the archive (none on some platforms).
	modulesDir := filepath.Join(srcDir, "modules")
	entries, err := os.ReadDir(modulesDir)
	if err != nil && !os.IsNotExist(err) {
		die("read modules directory: " + err.Error())
	}
	for _, de := range entries {
		if !de.IsDir() {
			continue
		}
		dst := filepath.Join(paths.SystemModulePath, de.Name())
		must(copyDir(filepath.Join(modulesDir, de.Name()), dst), "update "+de.Name())
		step("%s/", dst)
	}

	fmt.Println()
	fmt.Printf("  %s  upgraded to %s. run %s to apply module changes.\n",
		theme.Success.Render("done."), theme.Code.Render(tag), theme.Code.Render("suctl"))
}

// confirm prints prompt and returns true only for an explicit y/yes answer.
func confirm(prompt string) bool {
	fmt.Print(prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// compareSemver compares two "vMAJOR.MINOR.PATCH[-pre]" versions, returning
// -1 if a<b, 0 if equal, +1 if a>b. A version carrying a pre-release suffix
// ranks below the same core without one (1.0.0-rc1 < 1.0.0).
func compareSemver(a, b string) (int, error) {
	ca, pa, err := parseSemver(a)
	if err != nil {
		return 0, err
	}
	cb, pb, err := parseSemver(b)
	if err != nil {
		return 0, err
	}
	for i := 0; i < 3; i++ {
		if ca[i] != cb[i] {
			if ca[i] < cb[i] {
				return -1, nil
			}
			return 1, nil
		}
	}
	return comparePre(pa, pb), nil
}

// parseSemver splits "vMAJOR.MINOR.PATCH[-pre]" into its three numeric core
// components and the (possibly empty) pre-release label.
func parseSemver(v string) ([3]int, string, error) {
	var core [3]int
	s := strings.TrimPrefix(strings.TrimSpace(v), "v")
	pre := ""
	if i := strings.IndexByte(s, '-'); i >= 0 {
		pre, s = s[i+1:], s[:i]
	}
	parts := strings.SplitN(s, ".", 3)
	if len(parts) != 3 {
		return core, "", fmt.Errorf("not a semantic version: %q", v)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return core, "", fmt.Errorf("not a semantic version: %q", v)
		}
		core[i] = n
	}
	return core, pre, nil
}

// comparePre orders pre-release labels: an empty label (a final release) ranks
// above any non-empty one; two non-empty labels compare lexically.
func comparePre(a, b string) int {
	if a == b {
		return 0
	}
	if a == "" {
		return 1
	}
	if b == "" {
		return -1
	}
	if a < b {
		return -1
	}
	return 1
}
