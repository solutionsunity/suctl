// SPDX-License-Identifier: Apache-2.0

// Package version exposes the single build version for all suctl consumers
// (the `version` command, the REPL header, and any future readers).
package version

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var raw string

// Version is the canonical build version, read at compile time from the VERSION
// file in this package — the single source of truth (file-as-truth). No ldflags
// or stamping: `go build`/`go install` embed the file's contents directly, and
// the release tooling (the Makefile guard and the GitHub Action) reads that same
// file. To cut a release: bump VERSION, then create the matching git tag.
var Version = strings.TrimSpace(raw)
