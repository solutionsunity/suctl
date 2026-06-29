// SPDX-License-Identifier: Apache-2.0

// Package surface provides the typed response shapes for a module's
// face-agnostic surface.
//
// Modules return these structs from their declared survey and focus
// capabilities. The contract carries only semantic tokens, column ids, and
// pre-formatted values — zero face-bound styling — so any face (REPL,
// suctl-server, API) renders the same response in its own idiom. Using these
// types instead of raw map[string]interface{} gives compile-time safety and
// documents the contract expected by every renderer.
package surface

// Column is a single cell value with an optional color hint.
// Color values: "ok" (green), "warn" (yellow), "err" (red), "alert" (red badge —
// bold red on red-tinted background, for unavailable/critical states), "blue",
// "ghost" (dim), or nil for the default terminal color.
type Column struct {
	Value interface{} `json:"value"`
	Color interface{} `json:"color,omitempty"`
}

// Action is a capability button shown either inline on a survey row or in
// the focus overlay. Destructive actions are routed through a confirm prompt.
// Button colour is owned by the face (destructive vs safe), not the module —
// there is intentionally no Color field.
type Action struct {
	Capability  string `json:"capability"`
	Label       string `json:"label"`
	Destructive bool   `json:"destructive,omitempty"`
}

// Subject is one row in a survey table.
type Subject struct {
	// ID is the opaque identifier passed back to focus/action capabilities
	// as the "subject" parameter.
	ID   string `json:"id"`
	Name string `json:"name"`
	// Columns maps column key → cell. Keys must match those declared in the
	// module's surface.json columns list.
	Columns       map[string]Column `json:"columns"`
	InlineActions []Action          `json:"inline_actions,omitempty"`
	// Facets lists every facet value that applies to this row. Core uses these
	// tags to filter subjects client-side when the operator activates facet chips.
	// The module declares all applicable values regardless of which facets the
	// operator has currently selected — selection logic lives entirely in core.
	Facets []string `json:"facets,omitempty"`
}

// SurveyResponse is what a module returns from its survey capability.
type SurveyResponse struct {
	Total         int       `json:"total"`
	StatusSummary string    `json:"status_summary,omitempty"`
	Subjects      []Subject `json:"subjects"`
}

// --------------------------------------------------------------------------
// Focus types
// --------------------------------------------------------------------------

// Field is one labeled row inside a focus section.
type Field struct {
	Label     string      `json:"label"`
	Value     interface{} `json:"value"`
	Color     interface{} `json:"color,omitempty"`
	FullWidth bool        `json:"full_width,omitempty"`
}

// Section is a titled group of fields in the focus overlay.
type Section struct {
	Title  string  `json:"title"`
	Fields []Field `json:"fields"`
}

// FocusResponse is what a module returns from its focus capability.
type FocusResponse struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Sections []Section `json:"sections"`
	Actions  []Action  `json:"actions,omitempty"`
}

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------



// Col is a shorthand constructor for a Column.
func Col(value interface{}, color interface{}) Column {
	return Column{Value: value, Color: color}
}
