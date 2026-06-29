// SPDX-License-Identifier: Apache-2.0

// Package repl — diag.go renders survey and focus diagnostic dumps to
// stdout without entering the Bubble Tea program. It instantiates the
// same data structures the pages use and calls the pure render helpers
// (renderSurveyRows, renderFocusBody, renderFocusActions) directly.
package repl

import (
	"fmt"
	"os"

	"github.com/solutionsunity/suctl/sdk/manifest"
	"github.com/solutionsunity/suctl/internal/theme"
	"github.com/solutionsunity/suctl/sdk/surface"
)

const diagWidth = 100

// DumpInventory renders the system inventory survey to stdout and exits.
func DumpInventory(rc *manifest.SurfaceConfig, resp surface.SurveyResponse) {
	mod := &modSt{
		ShortName:     "system",
		SurfaceConfig: rc,
		Subjects:      SubjectsToMaps(resp.Subjects),
		Total:         resp.Total,
		StatusSummary: resp.StatusSummary,
	}

	runDiag(
		"suctl inventory diagnostic dump",
		[]string{
			fmt.Sprintf("  %d modules discovered in index.", resp.Total),
			"  " + theme.Dim.Render("Verify that the 'status' column below contains visible text (e.g. 'ready' or 'unavailable')."),
		},
		renderSurveyRows(mod, rowCursor{kind: rowKindSubject}, "", nil, diagWidth-2, 0, -1),
	)
}

// runDiag prints a diagnostic dump to stdout and exits 0. title is rendered
// with theme.Title and indented; preamble lines are printed unaltered between
// the title and a blank separator; body is the rendered content block.
// Single source of truth for the dump preamble/exit pattern shared by all
// diag commands.
func runDiag(title string, preamble []string, body string) {
	fmt.Println()
	fmt.Println("  " + theme.Title.Render(title))
	for _, line := range preamble {
		fmt.Println(line)
	}
	fmt.Println()
	fmt.Println(body)
	fmt.Println()
	os.Exit(0)
}

// DumpFocus renders the focus view for a specific module to stdout and exits.
func DumpFocus(resp surface.FocusResponse) {
	st := newFocusStFromResponse(resp)
	body := renderFocusBody(st, diagWidth-2, 0, -1) + "\n" + renderFocusActions(st, diagWidth-2)
	runDiag("suctl focus diagnostic dump: "+resp.Name, nil, body)
}
