// SPDX-License-Identifier: Apache-2.0

// Package logging initialises the process-wide slog logger for suctl.
//
// All structured log output uses log/slog so any package can call
// slog.Info / slog.Warn / slog.Error without importing this package.
// Call Init() once at startup (after bootstrap.Run()) — after that every
// slog call writes one human-readable line to the log file (paths.LogDir,
// resolved per-OS). If that file cannot be opened the logger falls back to
// stderr, so logging never blocks startup on any platform:
//
//	2026-05-16 14:23:14 UTC  INFO   suctl  dispatch  cap=odoo op=db.list
//
// In REPL mode pass teeStdout=true so operators can also observe log output
// on the terminal before the Bubble Tea program takes full control.
// Pass teeStdout=false to write only to the log file with no terminal echo.
package logging

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/solutionsunity/suctl/sdk/logging"
	"github.com/solutionsunity/suctl/sdk/paths"
)

// LogFile is the suctl log destination, resolved per-OS by sdk/paths (the single
// source of truth for all suctl paths).
var LogFile = filepath.Join(paths.LogDir, "suctl.log")

// logWriter holds the current destination to allow DisableStdout() to switch it.
var logWriter io.Writer = os.Stderr

// Init sets the process-wide slog default to a plain-text handler.
func Init(teeStdout bool) {
	f, err := os.OpenFile(LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	var out io.Writer = os.Stderr
	if err == nil {
		logWriter = f
		if teeStdout {
			out = io.MultiWriter(f, os.Stdout)
		} else {
			out = f
		}
	}

	logging.SetDefault(logging.Options{
		Writer:    out,
		Level:     slog.LevelInfo,
		Component: "suctl",
	})

	if err != nil {
		slog.Warn("log file unavailable", "path", LogFile, "err", err)
	} else {
		slog.Info("suctl starting", "log", LogFile)
	}
}

// DisableStdout switches the logger to write to the log file only.
func DisableStdout() {
	logging.SetDefault(logging.Options{
		Writer:    logWriter,
		Level:     slog.LevelInfo,
		Component: "suctl",
	})
}
