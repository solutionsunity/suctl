// SPDX-License-Identifier: Apache-2.0

// Package hooks executes module lifecycle hook declarations.
//
// A hook is either an exec script (run as a subprocess from the module
// directory) or a capability invocation (called over the module's live broker
// wire). Core calls Run for declared events; undeclared hooks are silently
// no-ops.
package hooks

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/solutionsunity/suctl/internal/logpipe"
	"github.com/solutionsunity/suctl/sdk/manifest"
	"github.com/solutionsunity/suctl/sdk/protocol"
)

// DefaultTimeout is the fallback per-hook deadline used when both the hook
// declaration's timeout_seconds and New's defaultTimeout are non-positive.
// Core overrides the per-Runner default from suctl.conf (Gate D).
const DefaultTimeout = 30 * time.Second

// Runner executes hook declarations for one module.
type Runner struct {
	shortName      string
	moduleDir      string
	confDir        string
	defaultTimeout time.Duration // fallback when a hook sets no timeout_seconds
}

// New returns a Runner for the given module. defaultTimeout is the fallback
// applied to a hook that declares no timeout_seconds; a non-positive value
// falls back to DefaultTimeout.
func New(shortName, moduleDir, confDir string, defaultTimeout time.Duration) *Runner {
	if defaultTimeout <= 0 {
		defaultTimeout = DefaultTimeout
	}
	return &Runner{shortName: shortName, moduleDir: moduleDir, confDir: confDir, defaultTimeout: defaultTimeout}
}

// Run executes the hook declared for event. decl may be nil — Run is a no-op
// in that case. client is required for capability-form hooks; pass nil for
// events that fire before the module process is running.
//
// Returns an error when a blocking hook fails. Non-blocking semantics are the
// caller's responsibility — callers that want non-blocking behaviour ignore the
// returned error or fire Run in a goroutine.
func (r *Runner) Run(event string, decl *manifest.HookDecl, client protocol.Invoker) error {
	if decl == nil {
		return nil
	}
	timeout := time.Duration(decl.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = r.defaultTimeout
	}
	if decl.Exec != "" {
		return r.runExec(event, decl.Exec, timeout)
	}
	if decl.Capability != "" {
		if client == nil {
			return fmt.Errorf("hooks: %s: capability hook %q requires a running module process",
				event, decl.Capability)
		}
		return r.runCapability(event, decl.Capability, client, timeout)
	}
	return fmt.Errorf("hooks: %s: hook declaration has neither exec nor capability", event)
}

// RunHook looks up event in m.Hooks and runs it, returning nil when the
// manifest is nil or the hook is not declared. Use this in place of the
// "if decl, ok := m.Hooks[event]; ok { runner.Run(event, decl, client) }"
// pattern across the activation/deactivation paths.
func (r *Runner) RunHook(m *manifest.Manifest, event string, client protocol.Invoker) error {
	if m == nil {
		return nil
	}
	return r.Run(event, m.Hooks[event], client)
}

// RunHookAsync fires the named hook in a goroutine, ignoring its return
// value. Use for non-blocking lifecycle events (on-crash, on-activate-fail,
// on-health-fail, on-health-recover). No-op when the hook is not declared.
func (r *Runner) RunHookAsync(m *manifest.Manifest, event string, client protocol.Invoker) {
	if m == nil || m.Hooks[event] == nil {
		return
	}
	go r.Run(event, m.Hooks[event], client) //nolint:errcheck — caller opted in to non-blocking
}

// RunRequirementMissing fires the on-requirement-missing hook for a single
// failing requirement. reqType is one of "capability", "binary", "path",
// "socket", "config"; reqValue is the specific name or path that is missing.
//
// Only the exec form is allowed here — the module
// process is not running yet. Returns nil if the hook exits 0 (caller should
// re-check the requirement), or an error on non-zero exit or timeout.
func (r *Runner) RunRequirementMissing(decl *manifest.HookDecl, reqType, reqValue string) error {
	if decl == nil {
		return fmt.Errorf("hooks: on-requirement-missing: nil hook declaration")
	}
	if decl.Capability != "" {
		return fmt.Errorf("hooks: on-requirement-missing: capability form not allowed before process start")
	}
	timeout := time.Duration(decl.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = r.defaultTimeout
	}
	extraEnv := []string{
		"SUCTL_REQ_TYPE=" + reqType,
		"SUCTL_REQ_VALUE=" + reqValue,
	}
	return r.runExecWithEnv("on-requirement-missing", decl.Exec, timeout, extraEnv)
}

// runExec runs the declared script as a subprocess with the correct environment
// and working directory. Returns an error on non-zero exit or timeout.
func (r *Runner) runExec(event, script string, timeout time.Duration) error {
	return r.runExecWithEnv(event, script, timeout, nil)
}

// runExecWithEnv is the implementation of runExec, accepting optional
// additional environment variables appended after the standard set.
// Hook stdout and stderr are forwarded line-by-line to the structured log
// so they never leak into the TUI terminal.
func (r *Runner) runExecWithEnv(event, script string, timeout time.Duration, extraEnv []string) error {
	scriptPath := filepath.Join(r.moduleDir, script)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, scriptPath)
	cmd.Dir = r.moduleDir
	cmd.Env = append(os.Environ(),
		"SUCTL_MODULE="+r.shortName,
		"SUCTL_EVENT="+event,
		"SUCTL_MODULE_DIR="+r.moduleDir,
		"SUCTL_CONF_DIR="+r.confDir,
	)
	cmd.Env = append(cmd.Env, extraEnv...)

	// Pipe hook output into the structured log via logpipe — never to
	// os.Stdout/Stderr, which the TUI owns.
	outW, err := logpipe.Pipe("hook output", "module", r.shortName, "event", event, "stream", "stdout")
	if err != nil {
		return fmt.Errorf("hooks: %s: stdout pipe: %w", event, err)
	}
	errW, err := logpipe.Pipe("hook output", "module", r.shortName, "event", event, "stream", "stderr")
	if err != nil {
		outW.Close()
		return fmt.Errorf("hooks: %s: stderr pipe: %w", event, err)
	}
	cmd.Stdout = outW
	cmd.Stderr = errW

	if err := cmd.Start(); err != nil {
		outW.Close()
		errW.Close()
		if ctx.Err() != nil {
			return fmt.Errorf("hooks: %s: exec hook timed out after %v", event, timeout)
		}
		return fmt.Errorf("hooks: %s: exec hook failed to start: %w", event, err)
	}
	// Parent closes its write ends — child is now the sole writer.
	outW.Close()
	errW.Close()

	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("hooks: %s: exec hook timed out after %v", event, timeout)
		}
		return fmt.Errorf("hooks: %s: exec hook failed: %w", event, err)
	}
	return nil
}



// runCapability invokes the declared capability over the module's live broker
// wire. The capability must be declared by the same module. The timeout is
// enforced as a hard deadline on the full round-trip — a hung capability hook
// cannot block indefinitely.
func (r *Runner) runCapability(event, capName string, client protocol.Invoker, timeout time.Duration) error {
	token := protocol.NewJobToken()
	// Use a simple args map — hook capabilities take no external args.
	resp, err := client.InvokeWithTimeout(token, capName, map[string]interface{}{}, timeout)
	if err != nil {
		return fmt.Errorf("hooks: %s: capability hook %q failed: %w", event, capName, err)
	}
	if resp.Status == "error" && resp.Error != nil {
		return fmt.Errorf("hooks: %s: capability hook %q returned error: %s",
			event, capName, resp.Error.Error())
	}
	return nil
}
