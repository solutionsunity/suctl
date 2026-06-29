// SPDX-License-Identifier: Apache-2.0

package qc

import (
	"fmt"
	"os"
)

// Result describes the outcome of a single QC check.
type Result string

const (
	Pass    Result = "PASS"
	Warn    Result = "WARN"
	Blocked Result = "BLOCKED"
)

// Check holds the outcome and optional message of a named check.
type Check struct {
	ID      string
	Name    string
	Result  Result
	Message string
}

// Report prints the QC checklist in the standard format.
func Report(checks []Check) {
	blockedCount := 0
	warnCount := 0

	for _, c := range checks {
		status := string(c.Result)
		if c.Message != "" {
			status = fmt.Sprintf("%-7s — %s", c.Result, c.Message)
		}
		fmt.Printf("%-36s %s\n", fmt.Sprintf("%s %s", c.ID, c.Name), status)

		if c.Result == Blocked {
			blockedCount++
		} else if c.Result == Warn {
			warnCount++
		}
	}

	fmt.Println()
	if blockedCount > 0 {
		fmt.Printf("%d BLOCKED — resolve before deploying to production\n", blockedCount)
	}
	if warnCount > 0 {
		fmt.Printf("%d WARN — investigate before next release\n", warnCount)
	}
	if blockedCount == 0 && warnCount == 0 {
		fmt.Println("All checks PASSED")
	}
}

// Run executes the full QC suite and exits with non-zero if any BLOCKED checks found.
func Run(args []string) {
	if len(args) == 0 {
		runCore()
		return
	}

	switch args[0] {
	case "core":
		runCore()
	case "module":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: suctl qc module <module-dir>")
			os.Exit(1)
		}
		runModule(args[1])
	default:
		fmt.Fprintf(os.Stderr, "suctl qc: unknown target %q (use 'core' or 'module')\n", args[0])
		os.Exit(1)
	}
}
