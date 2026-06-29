// SPDX-License-Identifier: Apache-2.0

// Package logpipe provides a single primitive for safely capturing subprocess
// output into the structured log.
//
// The TUI (Bubble Tea) owns the terminal: any subprocess that inherits
// os.Stdout or os.Stderr writes directly to the screen, corrupting the UI.
// logpipe.Pipe() creates an os.Pipe, starts a background goroutine that
// forwards every line to slog.Info, and returns only the write end — the
// caller assigns it to cmd.Stdout or cmd.Stderr, then closes it after
// cmd.Start() so the forwarder detects EOF when the child exits.
//
// Usage:
//
//	outW, err := logpipe.Pipe("module output", "module", name, "stream", "stdout")
//	errW, err := logpipe.Pipe("module output", "module", name, "stream", "stderr")
//	cmd.Stdout = outW
//	cmd.Stderr = errW
//	cmd.Start()
//	outW.Close()
//	errW.Close()
package logpipe

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
)

// Pipe creates an os.Pipe, starts a background goroutine that forwards each
// line from the read end to slog.Info with msg and attrs, and returns the
// write end for assignment to cmd.Stdout or cmd.Stderr.
//
// The caller must call Close() on the returned *os.File after cmd.Start()
// so that the child process is the sole writer; the forwarder then detects
// EOF on child exit and terminates cleanly.
//
// On pipe creation failure the error is wrapped and returned; no goroutine
// is started and the caller need not close anything.
func Pipe(msg string, attrs ...any) (*os.File, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("logpipe: %w", err)
	}
	go forward(r, msg, attrs)
	return w, nil
}

// forward reads lines from r until EOF and logs each one via slog.Info.
// It closes r on exit. Runs as a goroutine per Pipe call.
func forward(r *os.File, msg string, attrs []any) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		slog.Info(msg, append(attrs, "line", sc.Text())...)
	}
	r.Close()
}
