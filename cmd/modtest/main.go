// SPDX-License-Identifier: Apache-2.0

// Command suctl-modtest runs the suctl protocol BIST against a module binary.
//
// Usage:
//
//	suctl-modtest <module-binary> [module-dir]
//
// module-binary  path to the compiled module binary (e.g. ./suctl-mod-nginx)
// module-dir     directory containing manifest.json and surface.json; defaults to
//                the directory that contains the binary
//
// Exits 0 on full pass, 1 on any BIST failure, 2 on usage/setup error.
// The report is written to stdout so it can be piped or captured in CI.
package main

import (
	"fmt"
	"os"

	sdkconf "github.com/solutionsunity/suctl/sdk/conformance"
	"github.com/solutionsunity/suctl/sdk/manifest"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: suctl-modtest <module-binary> [module-dir]")
		os.Exit(2)
	}

	binPath := os.Args[1]
	modDir := ""
	if len(os.Args) >= 3 {
		modDir = os.Args[2]
	}

	// ── Derive the module short name (directory identity) for the title ──
	name := "module"
	if modDir != "" {
		name = manifest.ShortNameFromDir(modDir)
	}
	fmt.Printf("\nsuctl-modtest: %s @ %s\n\n", name, binPath)

	// ── BIST harness ─────────────────────────────────────────────────────────
	report, shutdownPassed, err := sdkconf.ProbeModuleBinary(sdkconf.ModuleHarnessOptions{
		BinaryPath: binPath,
		ModuleDir:  modDir,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "suctl-modtest: %v\n", err)
		os.Exit(2)
	}

	fmt.Printf("  handshake                                             PASS\n")
	report.PrintReport()

	total, passed, failed := report.Stats()
	total += 2
	passed += 1 // handshake
	if shutdownPassed {
		passed++
		fmt.Printf("  %-52sPASS\n", "  graceful shutdown")
	} else {
		failed++
		fmt.Printf("  %-52sFAIL\n", "  graceful shutdown")
	}

	fmt.Printf("\n  %d checks · %d fail · %d pass\n", total, failed, passed)
	if failed == 0 {
		fmt.Printf("  module WOULD activate under suctl boot\n\n")
		os.Exit(0)
	}
	fmt.Printf("  module WOULD NOT activate under suctl boot\n\n")
	os.Exit(1)
}
