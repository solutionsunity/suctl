// SPDX-License-Identifier: Apache-2.0

// Package theme is the single source of truth for the UI vocabulary used by
// every suctl surface (REPL, installer, bootstrap prompt). It exposes a
// palette plus a small set of named role styles. Callers reference roles
// (theme.Body, theme.BtnSelectedDanger, …) rather than constructing styles
// inline, so the look is defined in exactly one place.
package theme

import (
	"github.com/charmbracelet/lipgloss"
)

// Theme is a complete UI definition — raw palette plus the named role styles
// that callers consume. Apply() installs it as the active theme.
type Theme struct {
	// ── Palette — raw colour values ─────────────────────────────────────────
	// Background layers (darkest → lightest).
	Bg, Bg2, Bg3, Bg4, Bg5 lipgloss.Color
	// Accent colours.
	Blue, Green, Amber, Red, Purple, Cyan lipgloss.Color
	// Text shades (brightest → dimmest).
	Text1, Text2, Text3 lipgloss.Color
	// Solid approximations of low-alpha overlay colours, pre-blended onto Bg.
	BlueDim, BlueGlow, GreenDim, AmberDim, RedDim, PurpleDim lipgloss.Color

	// ── Text roles ──────────────────────────────────────────────────────────
	Title    lipgloss.Style // bold accent — page/section heading
	Body     lipgloss.Style // primary body text
	Dim      lipgloss.Style // de-emphasised secondary text
	Warn     lipgloss.Style // amber emphasis
	Error    lipgloss.Style // bold red emphasis
	Success  lipgloss.Style // bold green emphasis
	Code     lipgloss.Style // inline code / command emphasis
	Selected lipgloss.Style // bold accent — selected list item

	// ── Decorations ─────────────────────────────────────────────────────────
	Accent lipgloss.Style // foreground-only accent glyph (▎ marker, █ cursor, › prefix, spinner)
	RowBar lipgloss.Style // subtle solid bg bar for the entire cursor row (tier-1 selection)
	FieldFocus lipgloss.Style // solid amber bg + base bg fg for the focused field/button within a row (tier-2 selection)

	// ── Button states ───────────────────────────────────────────────────────
	// Safe-focused buttons reuse FieldFocus (solid amber); only the
	// destructive-selected ramp needs its own role.
	BtnSelectedDanger lipgloss.Style // destructive selected button (bg + fg)
	BtnIdleSafe       lipgloss.Style // safe idle button (fg only)
	BtnIdleDanger     lipgloss.Style // destructive idle button (fg only)
}

// Status returns a foreground-only style for a semantic colour token.
// Unknown tokens fall back to Text2 (neutral body).
func (t Theme) Status(token string) lipgloss.Style {
	switch token {
	case "ok", "green":
		return lipgloss.NewStyle().Foreground(t.Green)
	case "warn", "amber":
		return lipgloss.NewStyle().Bold(true).Foreground(t.Amber)
	case "alert":
		return lipgloss.NewStyle().Bold(true).Foreground(t.Red)
	case "err", "red":
		return lipgloss.NewStyle().Bold(true).Foreground(t.Red)
	case "blue", "info":
		return lipgloss.NewStyle().Foreground(t.Blue)
	case "purple", "accent":
		return lipgloss.NewStyle().Foreground(t.Purple)
	case "cyan", "tip":
		return lipgloss.NewStyle().Foreground(t.Cyan)
	case "dim", "ghost", "muted":
		return lipgloss.NewStyle().Foreground(t.Text3)
	default:
		return lipgloss.NewStyle().Foreground(t.Text2)
	}
}

// ── Package-level conveniences ──────────────────────────────────────────────
// All callers reference these (theme.Body, theme.BtnSelectedDanger, …). They are
// re-pointed atomically by Apply(); Bubble Tea renders serially on its main
// loop so there is no read/write race in normal operation.
var (
	Title, Body, Dim, Warn, Error, Success, Code, Selected lipgloss.Style
	Accent                                                 lipgloss.Style
	RowBar, FieldFocus                                     lipgloss.Style
	BtnSelectedDanger                                      lipgloss.Style
	BtnIdleSafe, BtnIdleDanger                             lipgloss.Style
)

var current Theme

func init() { Apply(Dark) }

// Apply installs t as the active theme and re-points the package-level
// convenience styles. Safe to call before any UI renders; callers that have
// already cached individual styles will not pick up the change.
func Apply(t Theme) {
	current = t
	Title, Body, Dim = t.Title, t.Body, t.Dim
	Warn, Error, Success = t.Warn, t.Error, t.Success
	Code, Selected = t.Code, t.Selected
	Accent = t.Accent
	RowBar, FieldFocus = t.RowBar, t.FieldFocus
	BtnSelectedDanger = t.BtnSelectedDanger
	BtnIdleSafe, BtnIdleDanger = t.BtnIdleSafe, t.BtnIdleDanger
}

// Active returns the currently-applied theme. Useful when a caller needs a
// raw palette colour to combine dynamically (e.g. dynamic bg + fg pairing).
func Active() Theme { return current }

// Status is a package-level shortcut to the active theme's Status mapping.
func Status(token string) lipgloss.Style { return current.Status(token) }

// ── Moments ─────────────────────────────────────────────────────────────────
// The brand sheet (suctl-brand-sheet.html) defines three "moments" — the
// colour heartbeat of the UI: Survey (blue, "what exists?"), Focus (green,
// "what is this, right now?"), and Act (amber, "what can I do?"). Each page
// declares its moment; chrome elements (title accents, separators, spinners)
// tint to the moment colour so the operator gets a peripheral cue of the
// current UI mode without reading text.

type Moment int

const (
	MomentSurvey Moment = iota // blue  — HOME, SURVEY
	MomentFocus                // green — FOCUS
	MomentAct                  // amber — CONFIRM, CASCADE CONFIRM, RUNNING
)

// MomentColor returns the raw palette colour for a moment.
func (t Theme) MomentColor(m Moment) lipgloss.Color {
	switch m {
	case MomentFocus:
		return t.Green
	case MomentAct:
		return t.Amber
	default:
		return t.Blue
	}
}

// MomentAccent returns a foreground-only accent style for a moment.
// Use for separators, gutters, spinners, and inline glyphs.
func (t Theme) MomentAccent(m Moment) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.MomentColor(m))
}

// MomentTitle returns a bold page-heading style for a moment. The wordmark
// (" suctl ") always stays blue — this is for in-page headings only.
func (t Theme) MomentTitle(m Moment) lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(t.MomentColor(m))
}

// Package-level shortcuts mirroring Status() above.
func MomentColor(m Moment) lipgloss.Color  { return current.MomentColor(m) }
func MomentAccent(m Moment) lipgloss.Style { return current.MomentAccent(m) }
func MomentTitle(m Moment) lipgloss.Style  { return current.MomentTitle(m) }
