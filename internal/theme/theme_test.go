// SPDX-License-Identifier: Apache-2.0

package theme_test

import (
	"fmt"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/solutionsunity/suctl/internal/theme"
)

// fgOf returns a stable string key for the foreground colour of a style.
// lipgloss.Color satisfies TerminalColor; Sprintf gives a comparable string.
func fgOf(s lipgloss.Style) string { return fmt.Sprintf("%v", s.GetForeground()) }

// colorKey returns the same stable key for a raw palette colour.
func colorKey(c lipgloss.Color) string { return fmt.Sprintf("%v", c) }

// Helper: apply a theme and return the active theme value.
func withTheme(t *testing.T, th theme.Theme, fn func()) {
	t.Helper()
	prev := theme.Active()
	theme.Apply(th)
	defer theme.Apply(prev)
	fn()
}

// ── Status token resolution ───────────────────────────────────────────────────

func TestStatusTokens_Dark(t *testing.T) {
	withTheme(t, theme.Dark, func() {
		th := theme.Active()

		cases := []struct {
			token    string
			wantFg   lipgloss.Color
		}{
			{"ok", th.Green},
			{"green", th.Green},
			{"warn", th.Amber},
			{"amber", th.Amber},
			{"err", th.Red},
			{"red", th.Red},
			{"blue", th.Blue},
			{"info", th.Blue},
			{"purple", th.Purple},
			{"accent", th.Purple},
			{"cyan", th.Cyan},
			{"tip", th.Cyan},
			{"dim", th.Text3},
			{"ghost", th.Text3},
			{"muted", th.Text3},
			{"", th.Text2},          // unknown → neutral body
			{"unknown", th.Text2},   // unknown → neutral body
		}

		for _, tc := range cases {
			got := th.Status(tc.token)
			if fgOf(got) != colorKey(tc.wantFg) {
				t.Errorf("Status(%q): foreground mismatch: got %v, want %v",
					tc.token, fgOf(got), colorKey(tc.wantFg))
			}
		}
	})
}

// ── Package-level convenience vars ───────────────────────────────────────────

func TestApply_UpdatesPackageVars(t *testing.T) {
	withTheme(t, theme.Dark, func() {
		// After applying dark, theme.Title should reflect the dark value.
		if fgOf(theme.Title) != fgOf(theme.Dark.Title) {
			t.Error("Apply(Dark): theme.Title not updated to dark value")
		}
	})
}
