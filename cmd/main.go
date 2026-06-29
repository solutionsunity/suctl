// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"

	"github.com/solutionsunity/suctl/internal/activation"
	"github.com/solutionsunity/suctl/internal/bootstrap"
	"github.com/solutionsunity/suctl/internal/config"
	"github.com/solutionsunity/suctl/internal/conformance"
	"github.com/solutionsunity/suctl/internal/installer"
	"github.com/solutionsunity/suctl/internal/logging"
	"github.com/solutionsunity/suctl/internal/privilege"
	"github.com/solutionsunity/suctl/internal/probe"
	"github.com/solutionsunity/suctl/internal/qc"
	"github.com/solutionsunity/suctl/internal/repl"
	"github.com/solutionsunity/suctl/internal/startup"
	"github.com/solutionsunity/suctl/internal/stores"
	"github.com/solutionsunity/suctl/internal/system"
	"github.com/solutionsunity/suctl/internal/theme"
	"github.com/solutionsunity/suctl/internal/version"
	"github.com/solutionsunity/suctl/sdk/lifecycle"
)

func main() {
	// Best-effort cleanup of a leftover binary from a prior self-replace
	// (Windows suctl.exe.old once the old process has exited). No-op on Unix.
	installer.SweepStaleBinary()

	// version — print and exit before any startup work; reporting the build
	// version needs no bootstrap, config, logging, or module scan.
	if len(os.Args) >= 2 && os.Args[1] == "version" {
		fmt.Println("suctl " + version.Version)
		return
	}

	// Startup sequence:
	//   1. bootstrap — create directories, write static pages (idempotent)
	//   2. config    — read the suctl config file once; held for the process lifetime
	//   3. logging   — open the log file (directory guaranteed by bootstrap)
	//   4. probe     — test runtime systemd connectivity; log and surface warnings
	//   5. discover  — scan module_paths; build module index + pending capability surface
	//   6. mode      — dispatch to REPL, install, or uninstall
	bootstrap.Run()
	cfg := config.Load()

	// Logging always writes to the log file only. In REPL mode we must not
	// tee to stdout: those lines print to the normal screen buffer before
	// Bubble Tea switches to alt-screen, then reappear when the TUI exits
	// and alt-screen is restored — the "leaking log" the operator sees.
	// The log file is always available; operators can tail it for live output.
	logging.Init(false)

	warns := probe.Run()
	for _, w := range warns {
		slog.Warn(w)
	}

	// -------------------------------------------------------------------------
	// Phases 1–3a — build both stores. stores.Build owns the modules-store
	// discovery pipeline (Scan + MarkMissing + EvaluateRequirements +
	// EvaluateConfigRequirements) and creates the empty messages store; the
	// activation list it folds in is read first.
	// -------------------------------------------------------------------------
	activatedNames, err := activation.List(activation.Dir)
	if err != nil {
		slog.Warn("could not read activation state", "error", err)
	}
	st, buildWarns, err := stores.Build(cfg.ModulePaths, activatedNames, bootstrap.ModuleConfDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "suctl: module scan:", err)
		os.Exit(1)
	}
	for _, w := range buildWarns {
		slog.Warn(w)
		warns = append(warns, w)
	}

	// -------------------------------------------------------------------------
	// install / uninstall — parse subcommand before anything blocking
	// -------------------------------------------------------------------------
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "install":
			installer.Run(os.Args[2:])
			return
		case "uninstall":
			installer.Uninstall(os.Args[2:])
			return
		case "upgrade":
			installer.Upgrade(os.Args[2:])
			return
		case "qc":
			qc.Run(os.Args[2:])
			return
		case "bist":
			conformance.Run(os.Args[2:])
			return
		case "diag":
			if len(os.Args) >= 3 {
				switch os.Args[2] {
				case "dump-inventory":
					repl.DumpInventory(system.SurfaceConfig(), system.BuildSurveyResponse(st.Modules))
				case "dump-focus":
					if len(os.Args) < 4 {
						fmt.Fprintln(os.Stderr, "error: dump-focus requires a module name")
						os.Exit(1)
					}
					resp, err := system.BuildFocusResponse(os.Args[3], st.Modules)
					if err != nil {
						fmt.Fprintf(os.Stderr, "error: %v\n", err)
						os.Exit(1)
					}
					repl.DumpFocus(resp)
				}
			}
			fmt.Fprintf(os.Stderr, "suctl: unknown diag command %v (available: dump-inventory, dump-focus <mod>)\n", os.Args[2:])
			os.Exit(1)
		default:
			fmt.Fprintf(os.Stderr, "suctl: unknown command %q\n", os.Args[1])
			os.Exit(1)
		}
	}

	// -------------------------------------------------------------------------
	// Identity / install gate. The first question is "am I the installed
	// binary?" — only that binary boots the REPL below. A downloaded release
	// copy is not the running suctl: it either offers to install (when nothing
	// is installed) or directs the operator to the installed `suctl`.
	// -------------------------------------------------------------------------
	if !installer.IsInstalledBinary() {
		if installer.IsInstalled() {
			promptUseInstalled()
		} else {
			promptInstall()
		}
	}

	// -------------------------------------------------------------------------
	// Shared graceful-shutdown state.
	// stopFn is set once modules are running (from the boot goroutine). The
	// signal handler reads it under the mutex so there is no race between
	// startup completion and an early SIGTERM.
	// -------------------------------------------------------------------------
	var (
		stopMu sync.Mutex
		stopFn = func() {} // no-op until modules are running
	)
	setStop := func(fn func()) {
		if fn == nil {
			return
		}
		stopMu.Lock()
		stopFn = fn
		stopMu.Unlock()
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, lifecycle.StopSignals()...)
	go func() {
		sig := <-sigCh
		slog.Info("received signal, shutting down", "signal", sig)
		stopMu.Lock()
		fn := stopFn
		stopMu.Unlock()
		fn()
		os.Exit(0)
	}()

	// -------------------------------------------------------------------------
	// Interactive REPL — boot spinner then seamless REPL transition.
	// -------------------------------------------------------------------------
	bootFn := func() repl.BootResult {
		rt, err := startup.Run(st, activatedNames, cfg)
		if err != nil {
			// CORE BIST failed before any module was activated — surface the
			// error through BootResult so the TUI quits and reports cleanly.
			return repl.BootResult{Err: err}
		}
		setStop(rt.Stop) // register for signal handler immediately

		return repl.BootResult{
			Warns:  rt.Warns,
			StopFn: rt.Stop,
			Face:   rt.Surface,
		}
	}
	fn := repl.RunWithBoot(warns, bootFn)
	if fn != nil {
		fn()
	}
}

// promptInstall asks the operator whether to install suctl now.
// Called when the system binary is absent — i.e. suctl has never been installed
// or was uninstalled. The prompt runs before the TUI so plain terminal I/O is safe.
func promptInstall() {
	fmt.Println()
	fmt.Println("  " + theme.Title.Render("suctl") + "  " + theme.Dim.Render("— not installed on this system."))
	fmt.Println()
	fmt.Println("  " + theme.Dim.Render("Run") + "  " + theme.Code.Render("suctl install") + "  " + theme.Dim.Render("with "+privilege.EscalationHint()+" to install it."))
	fmt.Println()
	fmt.Print("  Install now? [Y/n] ")

	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))

	if answer == "" || answer == "y" || answer == "yes" {
		if !privilege.IsAdmin() {
			fmt.Fprintln(os.Stderr, "\n  "+theme.Error.Render("error:")+" installation requires administrative privilege — re-run with "+privilege.EscalationHint()+": "+theme.Code.Render("suctl install"))
			os.Exit(1)
		}
		installer.Run([]string{})
		os.Exit(0)
	}
	// Operator declined — exit cleanly.
	fmt.Println()
	fmt.Println("  " + theme.Dim.Render("Run") + "  " + theme.Code.Render("suctl install") + "  " + theme.Dim.Render("with "+privilege.EscalationHint()+" when ready."))
	fmt.Println()
	os.Exit(0)
}

// promptUseInstalled informs the operator that suctl is already installed and
// directs them to the installed binary. Called when the running process is a
// release copy (not paths.SuctlBin) while an installed binary already exists —
// this copy exists only to install, not to run.
func promptUseInstalled() {
	fmt.Println()
	fmt.Println("  " + theme.Title.Render("suctl") + "  " + theme.Dim.Render("— already installed on this system."))
	fmt.Println()
	fmt.Println("  " + theme.Dim.Render("Run") + "  " + theme.Code.Render("suctl") + "  " + theme.Dim.Render("to start it, or") + "  " + theme.Code.Render("suctl install") + "  " + theme.Dim.Render("with "+privilege.EscalationHint()+" to reinstall."))
	fmt.Println()
	os.Exit(0)
}
