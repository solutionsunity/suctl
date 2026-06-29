// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	crossplane "github.com/nginxinc/nginx-go-crossplane"
)

// SourceBlock summarises one server { } block found inside a config file.
type SourceBlock struct {
	Primary     string    `json:"primary"`
	ServerNames []string  `json:"server_names"`
	Role        BlockRole `json:"role"`
	SSL         bool      `json:"ssl"`
	Migratable  bool      `json:"migratable"`
}

// SourceFile is one nginx config file under sites-available — the subject of
// the source surface. The default file is special: its migratable blocks can be
// lifted into their own per-domain files.
type SourceFile struct {
	Name       string        `json:"name"`
	Path       string        `json:"path"`
	Enabled    bool          `json:"enabled"`
	Managed    bool          `json:"managed"`
	IsDefault  bool          `json:"is_default"`
	Blocks     []SourceBlock `json:"blocks"`
	Migratable int           `json:"migratable"`
}

// isDefaultFile reports whether path is the nginx default-site file.
func isDefaultFile(path string) bool { return filepath.Base(path) == "default" }

// parseOpts is the shared lenient crossplane parse configuration.
func parseOpts() *crossplane.ParseOptions {
	// ParseComments keeps comments in the directive tree as "#" directives so a
	// parse→filter→Build rewrite (migration / default rebuild) re-emits them
	// instead of silently dropping operator notes and the suctl header.
	return &crossplane.ParseOptions{SkipDirectiveContextCheck: true, SkipDirectiveArgsCheck: true, ParseComments: true}
}

// blocksFromConfig extracts the server { } blocks from one parsed config file.
func blocksFromConfig(cfg crossplane.Config) []serverBlock {
	var blocks []serverBlock
	for _, dir := range cfg.Parsed {
		if dir.Directive != "server" || len(dir.Block) == 0 {
			continue
		}
		b := serverBlock{sourceFile: cfg.File}
		for _, d := range dir.Block {
			switch d.Directive {
			case "server_name":
				b.serverNames = append(b.serverNames, d.Args...)
			case "listen":
				ls := parseListen(d.Args)
				b.listens = append(b.listens, ls)
				if ls.ssl {
					b.ssl = true
				}
			case "root":
				if len(d.Args) > 0 {
					b.root = d.Args[0]
				}
			case "return":
				if len(d.Args) >= 1 && (d.Args[0] == "301" || d.Args[0] == "302") {
					b.redirect = true
					b.redirectCode = atoiSafe(d.Args[0])
					if len(d.Args) >= 2 {
						b.redirectTo = d.Args[1]
					}
				}
			}
		}
		blocks = append(blocks, b)
	}
	return blocks
}

// parseFileBlocks parses a single config file and returns its server blocks.
func parseFileBlocks(path string) ([]serverBlock, error) {
	payload, err := crossplane.Parse(path, parseOpts())
	if err != nil {
		return nil, err
	}
	var blocks []serverBlock
	for _, cfg := range payload.Config {
		blocks = append(blocks, blocksFromConfig(cfg)...)
	}
	return blocks, nil
}

// fileManaged reports whether the file carries the suctl management header.
func fileManaged(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 256)
	n, _ := f.Read(buf)
	return strings.Contains(string(buf[:n]), "managed by suctl")
}

// siteEnabled reports whether a matching entry exists in sites-enabled.
func siteEnabled(name string) bool {
	_, err := os.Lstat(filepath.Join(sitesEnabledDir, name))
	return err == nil
}

// toSourceBlock projects a parsed serverBlock into the source view shape.
// A block is migratable only when it has a real primary and lives in default.
func toSourceBlock(b serverBlock, isDefault bool) SourceBlock {
	primary, ok := blockPrimary(b.serverNames)
	role := RoleServing
	if b.redirect {
		role = RoleRedirect
	}
	return SourceBlock{
		Primary:     primary,
		ServerNames: append([]string{}, b.serverNames...),
		Role:        role,
		SSL:         b.ssl,
		Migratable:  ok && isDefault,
	}
}

// ListSources enumerates every config file under sites-available, parsing each
// for its server blocks. The default file reports how many of its blocks are
// migratable into their own per-domain files.
func ListSources() ([]*SourceFile, error) {
	entries, err := os.ReadDir(sitesAvailableDir)
	if err != nil {
		return nil, err
	}
	result := make([]*SourceFile, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		path := filepath.Join(sitesAvailableDir, name)
		sf := &SourceFile{
			Name:      name,
			Path:      path,
			IsDefault: isDefaultFile(path),
			Managed:   fileManaged(path),
			Enabled:   siteEnabled(name),
		}
		blocks, _ := parseFileBlocks(path)
		for _, b := range blocks {
			sb := toSourceBlock(b, sf.IsDefault)
			if sb.Migratable {
				sf.Migratable++
			}
			sf.Blocks = append(sf.Blocks, sb)
		}
		result = append(result, sf)
	}
	return result, nil
}

// ReadSource returns the single source file named name (e.g. "default").
func ReadSource(name string) (*SourceFile, error) {
	all, err := ListSources()
	if err != nil {
		return nil, err
	}
	for _, sf := range all {
		if sf.Name == name {
			return sf, nil
		}
	}
	return nil, fmt.Errorf("source file %q not found", name)
}

// defaultSitePath is the editable default-site file under sites-available.
func defaultSitePath() string { return filepath.Join(sitesAvailableDir, "default") }

// directiveServerNames collects the server_name args of a parsed server block.
func directiveServerNames(server *crossplane.Directive) []string {
	var names []string
	for _, d := range server.Block {
		if d.Directive == "server_name" {
			names = append(names, d.Args...)
		}
	}
	return names
}

// buildServerBlock re-emits a single parsed server block as config text,
// preserving every sub-directive (root, ssl, location, …) exactly.
func buildServerBlock(server *crossplane.Directive) (string, error) {
	var buf bytes.Buffer
	cfg := crossplane.Config{Parsed: crossplane.Directives{server}}
	if err := crossplane.Build(&buf, cfg, &crossplane.BuildOptions{Indent: 4}); err != nil {
		return "", err
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}

// MigrateResult reports the outcome of a migration: the primaries lifted into
// their own files, and those skipped because a destination file already existed
// (skip-and-report — the rest of the batch still proceeds).
type MigrateResult struct {
	Migrated []string
	Skipped  []string
}

// MigrateFromDefault lifts server blocks out of the default site into their own
// per-domain files. When all is true every migratable block moves; otherwise
// only the block whose primary server_name equals only. Targets whose
// destination file already exists are skipped and reported (the remaining
// blocks still migrate); it then rebuilds default without the migrated blocks.
func MigrateFromDefault(only string, all bool) (MigrateResult, error) {
	var res MigrateResult
	path := defaultSitePath()
	if _, err := os.Stat(path); err != nil {
		return res, fmt.Errorf("default site file not found: %s", path)
	}
	payload, err := crossplane.Parse(path, parseOpts())
	if err != nil {
		return res, fmt.Errorf("parse default: %w", err)
	}
	var parsed crossplane.Directives
	for _, cfg := range payload.Config {
		if isDefaultFile(cfg.File) {
			parsed = cfg.Parsed
			break
		}
	}
	if parsed == nil && len(payload.Config) > 0 {
		parsed = payload.Config[0].Parsed
	}

	type target struct {
		dir     *crossplane.Directive
		primary string
	}
	var targets []target
	for _, d := range parsed {
		if d.Directive != "server" || len(d.Block) == 0 {
			continue
		}
		primary, ok := blockPrimary(directiveServerNames(d))
		if !ok {
			continue // catch-all / IP-only stays in default
		}
		if !all && primary != only {
			continue
		}
		targets = append(targets, target{d, primary})
	}
	if len(targets) == 0 {
		if all {
			return res, fmt.Errorf("no migratable blocks in default")
		}
		return res, fmt.Errorf("block %q not found in default", only)
	}

	// Skip-and-report: a target whose destination already exists is recorded and
	// left in default; the remaining targets still migrate.
	var live []target
	for _, t := range targets {
		if _, err := os.Stat(sitesAvailablePath(t.primary)); err == nil {
			res.Skipped = append(res.Skipped, t.primary)
			continue
		}
		live = append(live, t)
	}
	if len(live) == 0 {
		return res, nil // everything skipped; default left untouched
	}

	if err := os.MkdirAll(sitesAvailableDir, 0755); err != nil {
		return res, err
	}
	if err := os.MkdirAll(sitesEnabledDir, 0755); err != nil {
		return res, err
	}
	migratedSet := make(map[*crossplane.Directive]bool, len(live))
	for _, t := range live {
		text, err := buildServerBlock(t.dir)
		if err != nil {
			return res, fmt.Errorf("build block %q: %w", t.primary, err)
		}
		content := "# managed by suctl — migrated from default\n" + text + "\n"
		if err := os.WriteFile(sitesAvailablePath(t.primary), []byte(content), 0644); err != nil {
			return res, err
		}
		os.Remove(sitesEnabledPath(t.primary)) //nolint:errcheck
		if err := os.Symlink(sitesAvailablePath(t.primary), sitesEnabledPath(t.primary)); err != nil {
			return res, err
		}
		migratedSet[t.dir] = true
		res.Migrated = append(res.Migrated, t.primary)
	}

	var kept crossplane.Directives
	for _, d := range parsed {
		if migratedSet[d] {
			continue
		}
		kept = append(kept, d)
	}
	var buf bytes.Buffer
	if err := crossplane.Build(&buf, crossplane.Config{File: path, Parsed: kept}, &crossplane.BuildOptions{Indent: 4}); err != nil {
		return res, fmt.Errorf("rebuild default: %w", err)
	}
	out := append(bytes.TrimRight(buf.Bytes(), "\n"), '\n')
	if err := os.WriteFile(path, out, 0644); err != nil {
		return res, fmt.Errorf("write default: %w", err)
	}
	return res, nil
}
