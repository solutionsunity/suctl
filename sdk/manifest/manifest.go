// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileName is the required name for the manifest file inside a module directory.
const FileName = "manifest.json"

// SurfaceFileName is the required name for a module's surface configuration
// file — the face-agnostic survey/focus contract.
const SurfaceFileName = "surface.json"

// SupportedProtocol is the only protocol version suctl speaks.
const SupportedProtocol = "1"

const modulePrefix = "suctl-mod-"

// HookDecl is one hook declaration inside the manifest hooks map.
type HookDecl struct {
	Exec           string `json:"exec"`
	Capability     string `json:"capability"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// Manifest is the complete, validated in-memory representation of a module's manifest.json.
type Manifest struct {
	Version      string               `json:"version"`
	Protocol     string               `json:"protocol"`
	Platform     []string             `json:"platform"`
	Author       string               `json:"author"`
	License      string               `json:"license"`
	Description  string               `json:"description"`
	Entrypoint   Entrypoint           `json:"entrypoint"`
	Requires     Requires             `json:"requires"`
	Capabilities []Capability         `json:"capabilities"`
	Hooks        map[string]*HookDecl `json:"hooks"`
}

// ShortNameFromDir derives a module's short name from its installation
// directory: the directory base name with the "suctl-mod-" prefix stripped.
// The directory is the module's identity — there is no manifest field.
func ShortNameFromDir(dir string) string {
	return strings.TrimPrefix(filepath.Base(dir), modulePrefix)
}

// Entrypoint holds the command suctl uses to start the module process.
type Entrypoint struct {
	Parts []string
}

func (e *Entrypoint) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		if s == "" {
			return errors.New("entrypoint: empty string")
		}
		e.Parts = []string{s}
		return nil
	}
	var arr []string
	if err := json.Unmarshal(data, &arr); err != nil {
		return fmt.Errorf("entrypoint: must be string or array of strings: %w", err)
	}
	if len(arr) == 0 {
		return errors.New("entrypoint: array must not be empty")
	}
	e.Parts = arr
	return nil
}

func (e Entrypoint) MarshalJSON() ([]byte, error) {
	if len(e.Parts) == 1 {
		return json.Marshal(e.Parts[0])
	}
	return json.Marshal(e.Parts)
}

// Requires declares all system-level prerequisites a module needs.
type Requires struct {
	Binaries     []string      `json:"binaries"`
	Paths        []string      `json:"paths"`
	Sockets      []string      `json:"sockets"`
	Permissions  []string      `json:"permissions"`
	Capabilities []string      `json:"capabilities"`
	Config       []ConfigEntry `json:"config"`
}

// ConfigEntry is one entry in requires.config.
type ConfigEntry struct {
	Key         string `json:"key"`
	Required    bool   `json:"required"`
	Secret      bool   `json:"secret"`
	Default     string `json:"default"`
	Description string `json:"description"`
}

// Capability is one atomic operation declared in the capabilities array.
type Capability struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Async       bool    `json:"async"`
	Params      []Param `json:"params"`
}

// Param is one parameter in a capability declaration.
type Param struct {
	Name        string      `json:"name"`
	Type        string      `json:"type"`
	Required    bool        `json:"required"`
	Description string      `json:"description"`
	Default     interface{} `json:"default"`
}

// SurfaceColumnConfig declares one column in the survey table. From names an
// optional source capability that fills this column's cell per row (data
// lineage), distinct from a view's entry door or an action's capability target.
// When absent the cell is sourced from the survey response itself (today's
// behavior); when set the face fires From scoped to the row and fills the cell
// from the returned column-map. Columns sharing a From value collapse to one
// call per row.
type SurfaceColumnConfig struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Width int    `json:"width"`
	Align string `json:"align"`
	From  string `json:"from,omitempty"`
}

// SurfaceFacetConfig declares one facet on the survey facet row.
type SurfaceFacetConfig struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// SurfaceSurveyActionConfig declares one survey-level action in surface.json.
// Survey actions operate on the currently visible subject list (scope +
// subjects[]) rather than a single subject. They are always confirmed before
// invocation because they are mass operations. Destructive controls danger
// styling on the button; the confirm step is unconditional for survey actions.
type SurfaceSurveyActionConfig struct {
	Capability  string `json:"capability"`
	Label       string `json:"label"`
	Destructive bool   `json:"destructive,omitempty"`
}

// SurfaceSurveyConfig is the survey section of surface.json.
type SurfaceSurveyConfig struct {
	Entry   string                      `json:"entry"`
	Columns []SurfaceColumnConfig       `json:"columns"`
	Facets  []SurfaceFacetConfig        `json:"facets"`
	Actions []SurfaceSurveyActionConfig `json:"actions,omitempty"`
}

// SurfaceFocusConfig is the focus section of surface.json.
type SurfaceFocusConfig struct {
	Entry string `json:"entry"`
}

// SurfaceConfig is the in-memory representation of one surface in surface.json —
// a single subject with its survey (list) and focus (detail) contract. A
// module may expose several co-equal surfaces (multi-subject); each becomes its
// own presentation. Name and Desc are optional: static/core surfaces carry
// their own display label and one-line description, where there is no enclosing
// manifest to supply them; module surfaces normally leave them empty and
// inherit the manifest's name/description.
//
// Drills nests child surfaces reached by drilling from a selected row of this
// surface. A drill child has the same shape recursively; faces pass the
// selected row's opaque id to the child survey as a scope argument. Label is
// the drill chip text on the parent's rows (defaults to the child Subject);
// it is unused on top-level surfaces. Nesting is the visibility rule:
// top-level surfaces are home roots, nested surfaces are drill-only.
type SurfaceConfig struct {
	Subject string              `json:"subject"`
	Name    string              `json:"name,omitempty"`
	Desc    string              `json:"desc,omitempty"`
	Label   string              `json:"label,omitempty"`
	Survey  SurfaceSurveyConfig `json:"survey"`
	Focus   SurfaceFocusConfig  `json:"focus"`
	Drills  []SurfaceConfig     `json:"drills,omitempty"`
}

// SurfaceFile is the canonical on-disk shape of surface.json: an ordered list
// of surfaces. The order is significant — the first surface keeps the module's
// bare short name; faces key subsequent surfaces by subject.
type SurfaceFile struct {
	Surfaces []SurfaceConfig `json:"surfaces"`
}

// ParseSurface decodes surface.json bytes into the ordered list of surfaces.
// The canonical shape is {"surfaces":[…]}. Each surface — and every nested
// drill — is validated (subject, survey entry, focus entry) and subjects must
// be unique across the whole module tree. Empty input yields (nil, nil) — a
// module without a surface is valid.
func ParseSurface(data []byte) ([]SurfaceConfig, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	var file SurfaceFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if len(file.Surfaces) == 0 {
		return nil, fmt.Errorf("no surfaces declared")
	}
	if err := validateSurfaces(file.Surfaces, make(map[string]bool)); err != nil {
		return nil, err
	}
	return file.Surfaces, nil
}

// validateSurfaces checks subject/survey/focus presence on each surface and
// recurses into nested drills. The seen map is shared across the whole tree so
// subjects are unique module-wide — this is what forbids a surface from being
// both a home root and a drill child, and removes the dangling-reference
// and cycle risks of a flat-plus-reference model.
func validateSurfaces(surfaces []SurfaceConfig, seen map[string]bool) error {
	for i := range surfaces {
		s := &surfaces[i]
		if s.Subject == "" {
			return fmt.Errorf("surface %d: subject is required", i)
		}
		if s.Survey.Entry == "" {
			return fmt.Errorf("surface %q: survey.entry is required", s.Subject)
		}
		if s.Focus.Entry == "" {
			return fmt.Errorf("surface %q: focus.entry is required", s.Subject)
		}
		if seen[s.Subject] {
			return fmt.Errorf("duplicate subject %q", s.Subject)
		}
		seen[s.Subject] = true
		if err := validateSurfaces(s.Drills, seen); err != nil {
			return err
		}
	}
	return nil
}

// LoadSurfaceFromDir reads surface.json from dir and returns its ordered
// surfaces. A directory without surface.json yields (nil, nil).
func LoadSurfaceFromDir(dir string) ([]SurfaceConfig, error) {
	path := filepath.Join(dir, SurfaceFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("surface: read %s: %w", path, err)
	}
	surfaces, err := ParseSurface(data)
	if err != nil {
		return nil, fmt.Errorf("surface: %s: %w", path, err)
	}
	return surfaces, nil
}

// LoadFromDir reads and validates manifest.json from dir.
func LoadFromDir(dir string) (*Manifest, error) {
	path := filepath.Join(dir, FileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("manifest: read %s: %w", path, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest: parse %s: %w", path, err)
	}
	if err := Validate(&m); err != nil {
		return nil, fmt.Errorf("manifest: validate %s: %w", path, err)
	}
	return &m, nil
}

// Validate checks all required fields.
func Validate(m *Manifest) error {
	var errs []string

	if m.Version == "" {
		errs = append(errs, "version: required")
	}
	if m.Protocol == "" {
		errs = append(errs, "protocol: required")
	} else if m.Protocol != SupportedProtocol {
		errs = append(errs, fmt.Sprintf("protocol: unsupported version %q (core speaks %q)", m.Protocol, SupportedProtocol))
	}
	if len(m.Platform) == 0 {
		errs = append(errs, "platform: required, must list at least one platform")
	}
	if m.Author == "" {
		errs = append(errs, "author: required")
	}
	if m.License == "" {
		errs = append(errs, "license: required")
	}
	if m.Description == "" {
		errs = append(errs, "description: required")
	}
	if len(m.Entrypoint.Parts) == 0 {
		errs = append(errs, "entrypoint: required")
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}
