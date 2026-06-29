// SPDX-License-Identifier: Apache-2.0

package theme

import "github.com/charmbracelet/lipgloss"

// Dark is suctl's dark scheme. Background, text, and accent hex values are
// taken verbatim from suctl-brand-sheet.html (the authoritative brand
// palette). The *Dim colours are alpha-blended overlays pre-mixed onto
// the base background so terminals without true alpha render identically
// to a browser.
var Dark = newDark()

func newDark() Theme {
	// Backgrounds — brand palette (bg..bg4). bg5 extends one step lighter
	// for the FieldFocus surface; brand stops at bg4.
	bg := lipgloss.Color("#09090b")
	bg2 := lipgloss.Color("#0d0d10")
	bg3 := lipgloss.Color("#111116")
	bg4 := lipgloss.Color("#16161c")
	bg5 := lipgloss.Color("#1c1c24")

	// Accents — brand palette. Red is overridden brighter than brand
	// (#e06c75) because the brand value renders muddy on most terminals.
	blue := lipgloss.Color("#5b9bd5")
	green := lipgloss.Color("#4ec9a0")
	amber := lipgloss.Color("#e5a830")
	red := lipgloss.Color("#ef5350")
	purple := lipgloss.Color("#9d7fd4")
	cyan := lipgloss.Color("#56b6c2")

	// Text — brand palette. text3 is intentionally close to text2 (the
	// brand spec values them at #8a92a4 / #9aa3b5) so dim chrome stays
	// legible; the previous #4e5668 was too dark and disappeared.
	text1 := lipgloss.Color("#e8eaf0")
	text2 := lipgloss.Color("#9aa3b5")
	text3 := lipgloss.Color("#8a92a4")

	// Overlay approximations — translucent accent values pre-blended onto the
	// base background (#09090b).
	blueDim := lipgloss.Color("#151f29")   // rgba(91,155,213,0.15)
	blueGlow := lipgloss.Color("#10151b")  // rgba(91,155,213,0.08)
	greenDim := lipgloss.Color("#11201d")  // rgba(78,201,160,0.12)
	amberDim := lipgloss.Color("#231c0f")  // rgba(229,168,48,0.12)
	redDim := lipgloss.Color("#241113")    // rgba(239,83,80,0.12)
	purpleDim := lipgloss.Color("#1a1723") // rgba(157,127,212,0.12)

	return Theme{
		Bg: bg, Bg2: bg2, Bg3: bg3, Bg4: bg4, Bg5: bg5,
		Blue: blue, Green: green, Amber: amber, Red: red, Purple: purple, Cyan: cyan,
		Text1: text1, Text2: text2, Text3: text3,
		BlueDim: blueDim, BlueGlow: blueGlow,
		GreenDim: greenDim, AmberDim: amberDim, RedDim: redDim, PurpleDim: purpleDim,

		Title:    lipgloss.NewStyle().Bold(true).Foreground(blue),
		Body:     lipgloss.NewStyle().Foreground(text1),
		Dim:      lipgloss.NewStyle().Foreground(text3),
		Warn:     lipgloss.NewStyle().Foreground(amber),
		Error:    lipgloss.NewStyle().Bold(true).Foreground(red),
		Success:  lipgloss.NewStyle().Bold(true).Foreground(green),
		Code:     lipgloss.NewStyle().Foreground(amber),
		Selected: lipgloss.NewStyle().Bold(true).Foreground(amber),

		Accent: lipgloss.NewStyle().Foreground(blue),
		// RowBar is the active-row fill — a neutral one-step lift (bg4) so
		// the row is distinguishable without fighting the amber gutter glyph.
		// The amber ▎ carries the warm pre-attentive signal; the background
		// only needs to extend the "cursor zone" to the right edge.
		RowBar:     lipgloss.NewStyle().Background(bg4),
		FieldFocus: lipgloss.NewStyle().Background(amber).Foreground(bg),

		BtnSelectedDanger: lipgloss.NewStyle().Background(red).Foreground(bg),
		// Idle safe buttons use blue (identity/action). Focused buttons are amber (warm).
		BtnIdleSafe:   lipgloss.NewStyle().Foreground(blue),
		BtnIdleDanger: lipgloss.NewStyle().Foreground(red),
	}
}
