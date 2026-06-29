// SPDX-License-Identifier: Apache-2.0

package conformance

import (
	"fmt"
	"os"

	sdkconf "github.com/solutionsunity/suctl/sdk/conformance"
)

// Run executes the BIST suite for a module binary.
// core and repl BIST run automatically at REPL startup — there is no
// operator-facing CLI target for them.
func Run(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: suctl bist module <module-binary> [module-dir]")
		os.Exit(1)
	}

	switch args[0] {
	case "module":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: suctl bist module <module-binary> [module-dir]")
			os.Exit(1)
		}
		modDir := ""
		if len(args) >= 3 {
			modDir = args[2]
		}
		fmt.Printf("\nsuctl bist: module @ %s\n\n", args[1])
		report, shutdownPassed, err := sdkconf.ProbeModuleBinary(sdkconf.ModuleHarnessOptions{
			BinaryPath: args[1],
			ModuleDir:  modDir,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		report.PrintReport()
		total, passed, failed := report.Stats()

		// Handshake and shutdown are implicit in the harness.
		total += 2
		passed += 1 // handshake passed if we got a report
		if shutdownPassed {
			passed++
			fmt.Printf("  %-52sPASS\n", "graceful shutdown")
		} else {
			failed++
			fmt.Printf("  %-52sFAIL\n", "graceful shutdown")
		}

		fmt.Printf("\n  %d checks · %d fail · %d pass\n", total, failed, passed)
		if failed > 0 {
			os.Exit(1)
		}

	default:
		fmt.Fprintf(os.Stderr, "suctl bist: unknown target %q (available: module)\n", args[0])
		os.Exit(1)
	}
}
