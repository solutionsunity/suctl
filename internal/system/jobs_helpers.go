// SPDX-License-Identifier: Apache-2.0

// Package system — jobs_helpers.go holds the pure rendering helpers used
// by the system.jobs.* handlers. Kept separate from jobs.go so the
// capability surface stays readable.
package system

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// jobStateColor maps a job state to the theme colour token used by the
// REPL renderer. Returning interface{} matches surface.Column.
func jobStateColor(state string) interface{} {
	switch state {
	case "queued":
		return "info"
	case "running":
		return "warn"
	case "done":
		return "ok"
	case "failed":
		return "alert"
	default:
		return nil
	}
}

// callType renders a capability's declared async mode for the jobs focus view.
func callType(async bool) string {
	if async {
		return "async"
	}
	return "sync"
}

// jobsSummary builds the one-line status summary shown on the jobs page
// header. Empty buckets are omitted so short systems do not get noise.
func jobsSummary(queued, running, done, failed int) string {
	var parts []string
	if queued > 0 {
		parts = append(parts, fmt.Sprintf("%d queued", queued))
	}
	if running > 0 {
		parts = append(parts, fmt.Sprintf("%d running", running))
	}
	if done > 0 {
		parts = append(parts, fmt.Sprintf("%d done", done))
	}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", failed))
	}
	if len(parts) == 0 {
		return "no jobs"
	}
	return strings.Join(parts, " · ")
}



// relativeAge renders a duration since t as a short ago-string for the
// "started" column. Matches the granularity operators expect at a glance:
// sub-minute is seconds, sub-hour is minutes, sub-day is hours, else days.
func relativeAge(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	switch {
	case d < time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// shortToken returns the first 8 characters of a job token for compact
// display in the survey "name" column. The full token remains available
// in the focus view's identity section.
func shortToken(tok string) string {
	if len(tok) <= 8 {
		return tok
	}
	return tok[:8]
}

// formatJobOutput returns a human-readable string for the job output field.
// Job.Output is now typed json.RawMessage, so no type switch is needed.
func formatJobOutput(v json.RawMessage) string {
	if len(v) == 0 {
		return "(none)"
	}
	return prettyJSONString(string(v))
}

// prettyJSONString indents s with json.Indent when s is valid JSON, otherwise
// returns s unchanged so non-JSON outputs still render as their raw text.
func prettyJSONString(s string) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(s), "", "  "); err != nil {
		return s
	}
	return buf.String()
}
