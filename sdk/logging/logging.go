// SPDX-License-Identifier: Apache-2.0

// Package logging provides a standardized structured logger for suctl.
//
// It wraps log/slog with a human-readable plain-text handler that matches
// the suctl log file convention:
//
//	2026-05-16 14:23:14 UTC  INFO   component  message  key=val key=val
//
// Module authors call InitModule once at startup (via modserver.Serve or
// directly) to get the same format and log-file convention as all other
// suctl components.
package logging

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/solutionsunity/suctl/sdk/paths"
)

// Options configures the logger.
type Options struct {
	// Writer is the destination for log output (e.g., os.Stderr, or a file).
	Writer io.Writer
	// Level is the minimum importance level to log.
	Level slog.Level
	// Component is the name of the component (e.g., "suctl", "mod-nginx").
	Component string
}

// New returns a new slog.Logger using the suctl plain-text handler.
func New(opts Options) *slog.Logger {
	if opts.Writer == nil {
		opts.Writer = io.Discard
	}
	return slog.New(&plainHandler{
		w:         opts.Writer,
		level:     opts.Level,
		component: opts.Component,
	})
}

// SetDefault configures the global slog logger with the given options.
func SetDefault(opts Options) {
	slog.SetDefault(New(opts))
}

// InitModule sets the process-wide slog default for a module process.
// It opens /var/log/suctl/<name>.log for appending; on failure it falls back
// to stderr so the supervisor's pipe still captures output. Call once at
// startup — modserver.Serve calls it automatically when Config.Name is set.
func InitModule(name string) {
	logFile := filepath.Join(paths.LogDir, name+".log")
	var w io.Writer = os.Stderr
	if f, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
		w = f
	}
	SetDefault(Options{
		Writer:    w,
		Level:     slog.LevelInfo,
		Component: name,
	})
}

type plainHandler struct {
	mu        sync.Mutex
	w         io.Writer
	level     slog.Level
	component string
	preAttrs  []slog.Attr
}

func (h *plainHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level
}

func (h *plainHandler) Handle(_ context.Context, r slog.Record) error {
	ts := r.Time.UTC().Format("2006-01-02 15:04:05 UTC")

	var extra strings.Builder
	for _, a := range h.preAttrs {
		fmt.Fprintf(&extra, "  %s=%v", a.Key, a.Value.Any())
	}
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&extra, "  %s=%v", a.Key, a.Value.Any())
		return true
	})

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "%s  %-5s  %s  %s%s\n",
		ts, r.Level.String(), h.component, r.Message, extra.String())

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.w.Write(buf.Bytes())
	return err
}

func (h *plainHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &plainHandler{
		w:         h.w,
		level:     h.level,
		component: h.component,
		preAttrs:  append(append([]slog.Attr{}, h.preAttrs...), attrs...),
	}
}

func (h *plainHandler) WithGroup(_ string) slog.Handler { return h }
