// SPDX-License-Identifier: Apache-2.0

//go:build windows

package channel

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// duplexPipe bonds two half-duplex anonymous pipes (one per direction) into one
// logical full-duplex io.ReadWriteCloser. This is the Windows stand-in for the
// single socketpair fd: Read drains the inbound pipe, Write fills the outbound
// pipe. Logically one wire; physically two handles.
type duplexPipe struct {
	r *os.File // this end's read side (the other end writes here)
	w *os.File // this end's write side (the other end reads here)
}

func (d *duplexPipe) Read(p []byte) (int, error)  { return d.r.Read(p) }
func (d *duplexPipe) Write(p []byte) (int, error) { return d.w.Write(p) }

// Close tears down both half-pipes, returning the first error so a partial
// failure is not masked.
func (d *duplexPipe) Close() error {
	rErr := d.r.Close()
	wErr := d.w.Close()
	if rErr != nil {
		return rErr
	}
	return wErr
}

// Pair mirrors the Unix shape (see channel_unix.go) but, because Windows
// anonymous pipes are half-duplex, the child's single logical end is a PAIR of
// handles: remoteR (the read side of the parent->child pipe) and remoteW (the
// write side of the child->parent pipe). The parent keeps Local (its own bonded
// duplex end); possession of remoteR+remoteW is the module's identity exactly as
// on Unix.
type Pair struct {
	// Local is the parent's end of the logical wire — reads child->parent on one
	// pipe, writes parent->child on the other.
	Local io.ReadWriteCloser
	// remoteR/remoteW are the child's ends, handed over as inheritable handles at
	// spawn and closed by the parent after the child has started (it inherited
	// its own copies).
	remoteR *os.File
	remoteW *os.File
}

// Spawn creates two anonymous pipes — one per direction — and bonds the parent's
// two ends into a single logical duplex wire. See duplexPipe and the package doc
// for why two half-duplex pipes stand in for one socketpair while keeping the
// same address-less trust model.
func Spawn() (*Pair, error) {
	// parent->child direction: parent writes p2cW, child reads p2cR.
	p2cR, p2cW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("channel: parent->child pipe: %w", err)
	}
	// child->parent direction: child writes c2pW, parent reads c2pR.
	c2pR, c2pW, err := os.Pipe()
	if err != nil {
		p2cR.Close() //nolint:errcheck
		p2cW.Close() //nolint:errcheck
		return nil, fmt.Errorf("channel: child->parent pipe: %w", err)
	}

	local := &duplexPipe{r: c2pR, w: p2cW}
	return &Pair{Local: local, remoteR: p2cR, remoteW: c2pW}, nil
}

// Attach marks the child's two pipe handles inheritable, registers them on the
// command so only this child inherits them (AdditionalInheritedHandles requires
// the handles already be marked inheritable), and returns the env telling the
// child which handle is its read end and which is its write end. This is the
// Windows counterpart of the Unix ExtraFiles + SUCTL_BROKER_FD=3. Inherited
// handle VALUES are preserved across CreateProcess, so the child reopens them
// directly from these numbers.
func (p *Pair) Attach(cmd *exec.Cmd) ([]string, error) {
	rh := p.remoteR.Fd()
	wh := p.remoteW.Fd()
	for _, h := range []uintptr{rh, wh} {
		if err := syscall.SetHandleInformation(syscall.Handle(h), syscall.HANDLE_FLAG_INHERIT, syscall.HANDLE_FLAG_INHERIT); err != nil {
			return nil, fmt.Errorf("channel: mark handle inheritable: %w", err)
		}
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.AdditionalInheritedHandles = append(
		cmd.SysProcAttr.AdditionalInheritedHandles,
		syscall.Handle(rh), syscall.Handle(wh),
	)
	return []string{
		"SUCTL_BROKER_FD_R=" + strconv.FormatUint(uint64(rh), 10),
		"SUCTL_BROKER_FD_W=" + strconv.FormatUint(uint64(wh), 10),
	}, nil
}

// CloseRemote closes the parent's copies of the child's two handles after spawn;
// the child inherited its own. Safe on the error path before start too.
func (p *Pair) CloseRemote() {
	if p.remoteR != nil {
		p.remoteR.Close() //nolint:errcheck
	}
	if p.remoteW != nil {
		p.remoteW.Close() //nolint:errcheck
	}
}

// Inherit recovers the broker wire on the module side from the two inherited
// handle values named by SUCTL_BROKER_FD_R / SUCTL_BROKER_FD_W, or reports false
// when either is absent. Inherited handle values are preserved across
// CreateProcess, so os.NewFile reopens them directly from the advertised
// numbers. Read drains the parent->child pipe; Write fills the child->parent
// pipe.
func Inherit() (io.ReadWriteCloser, bool) {
	rStr := os.Getenv("SUCTL_BROKER_FD_R")
	wStr := os.Getenv("SUCTL_BROKER_FD_W")
	if rStr == "" || wStr == "" {
		return nil, false
	}
	rh, err := strconv.ParseUint(rStr, 10, 64)
	if err != nil {
		return nil, false
	}
	wh, err := strconv.ParseUint(wStr, 10, 64)
	if err != nil {
		return nil, false
	}
	r := os.NewFile(uintptr(rh), "broker-wire-r")
	w := os.NewFile(uintptr(wh), "broker-wire-w")
	if r == nil || w == nil {
		return nil, false
	}
	return &duplexPipe{r: r, w: w}, true
}
