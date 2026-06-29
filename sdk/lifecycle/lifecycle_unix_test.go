// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package lifecycle

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"testing"
	"time"
)

// helperEnv guards the child half (TestStopHelper) so it only blocks-on-signal
// when re-exec'd by TestStopGraceful, and is an immediate no-op under plain
// `go test`.
const helperEnv = "SUCTL_LIFECYCLE_HELPER"

// TestStopGraceful proves the parent-side contract core's supervisor relies on:
// after Configure at spawn, Stop asks a running child to exit gracefully and the
// child exits cleanly (code 0) within the grace period. The child is this test
// binary re-exec'd in helper mode; a stdout "ready" handshake removes the race
// between Stop and the child installing its signal handler.
func TestStopGraceful(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestStopHelper")
	cmd.Env = append(os.Environ(), helperEnv+"=1")
	Configure(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}

	ready := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if strings.TrimSpace(sc.Text()) == "ready" {
				close(ready)
				return
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("helper never reported ready")
	}

	if err := Stop(cmd.Process); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("child did not exit cleanly on Stop: %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("child did not exit within grace period after Stop")
	}
}

// TestStopHelper is the child half of TestStopGraceful: when re-exec'd with
// helperEnv set it blocks on StopSignals, reports readiness, and exits 0 on the
// first signal — mirroring a real module's modserver wait loop. It returns
// immediately (no-op) under normal test runs.
func TestStopHelper(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		return
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, StopSignals()...)
	fmt.Println("ready")
	<-ch
	os.Exit(0)
}
