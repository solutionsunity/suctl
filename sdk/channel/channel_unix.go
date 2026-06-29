// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package channel

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// Pair is a private, address-less, pre-connected bidirectional wire between a
// parent (core or the BIST harness) and one child module process. The parent
// keeps Local (a net.Conn it reads/writes the module's broker traffic on) and
// hands remote to the child as an inherited fd at spawn. Possession of remote is
// the module's identity.
//
// On Unix this is genuinely one full-duplex kernel object (a socketpair); Local
// is typed io.ReadWriteCloser only so shared mux code reads the same on both
// platforms (Windows bonds two half-duplex pipes — see channel_windows.go).
type Pair struct {
	// Local is the parent's end of the wire.
	Local io.ReadWriteCloser
	// remote is the child's end, passed via exec.Cmd.ExtraFiles and closed by
	// the parent after the child has started (the child inherited its own copy).
	remote *os.File
}

// Spawn creates a socketpair and wraps each end for use as a broker wire. Both
// raw fds are marked close-on-exec so neither end leaks into unrelated child
// processes spawned later; exec re-enables inheritance for the remote end it
// actually passes to its target. The caller owns both ends and must close them
// (Local via Close, remote via CloseRemote).
func Spawn() (*Pair, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, fmt.Errorf("channel: socketpair: %w", err)
	}
	// Guard against fd leaks across concurrent module spawns: a later exec.Cmd
	// only passes its own ExtraFiles, but only CLOEXEC guarantees these ends are
	// not inherited by those unrelated children.
	syscall.CloseOnExec(fds[0])
	syscall.CloseOnExec(fds[1])

	localFile := os.NewFile(uintptr(fds[0]), "broker-wire-local")
	remoteFile := os.NewFile(uintptr(fds[1]), "broker-wire-remote")

	// net.FileConn dups localFile's fd into the runtime poller; close our copy.
	localConn, err := net.FileConn(localFile)
	localFile.Close() //nolint:errcheck
	if err != nil {
		remoteFile.Close() //nolint:errcheck
		return nil, fmt.Errorf("channel: wrap local end: %w", err)
	}

	return &Pair{Local: localConn, remote: remoteFile}, nil
}

// Attach hands the child its end of the wire and returns the env naming it. The
// child's only ExtraFile maps to fd 3, advertised as SUCTL_BROKER_FD. Cannot
// fail on Unix; the error return mirrors the Windows seam so shared code is
// uniform.
func (p *Pair) Attach(cmd *exec.Cmd) ([]string, error) {
	cmd.ExtraFiles = append(cmd.ExtraFiles, p.remote)
	return []string{"SUCTL_BROKER_FD=3"}, nil
}

// CloseRemote closes the parent's copy of the child end after spawn; the child
// inherited its own. Safe on the error path before start too.
func (p *Pair) CloseRemote() {
	if p.remote != nil {
		p.remote.Close() //nolint:errcheck
	}
}

// Inherit recovers the broker wire on the module side from the single inherited
// fd named by SUCTL_BROKER_FD, or reports false when none was inherited (the
// caller then fails: there is no shared socket to dial). net.FileConn dups the
// fd into the runtime poller; the temporary *os.File is closed immediately.
func Inherit() (io.ReadWriteCloser, bool) {
	fdStr := os.Getenv("SUCTL_BROKER_FD")
	if fdStr == "" {
		return nil, false
	}
	fd, err := strconv.Atoi(fdStr)
	if err != nil {
		return nil, false
	}
	f := os.NewFile(uintptr(fd), "broker-wire")
	conn, err := net.FileConn(f)
	f.Close() //nolint:errcheck
	if err != nil {
		return nil, false
	}
	return conn, true
}
