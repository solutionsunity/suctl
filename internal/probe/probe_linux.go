// SPDX-License-Identifier: Apache-2.0

//go:build linux

package probe

import (
	"context"
	"os"

	"github.com/coreos/go-systemd/v22/dbus"
)

// runtimeWarnings probes the systemd runtime that suctl's svc and sys.log
// operations depend on. It is Linux-only because both facilities (D-Bus system
// bus, journald via libsystemd/CGo) exist only under systemd.
func runtimeWarnings() []string {
	var warns []string

	// D-Bus connection — required by every svc operation.
	if conn, err := dbus.NewSystemConnectionContext(context.Background()); err != nil {
		warns = append(warns, "systemd D-Bus unavailable — svc operations will fail: "+err.Error())
	} else {
		conn.Close()
	}

	// Journal access — required by sys.log.* operations (journald source). Probe
	// the journald socket directly to avoid a libsystemd/CGo dependency in core.
	if _, err := os.Stat("/run/systemd/journal/socket"); err != nil {
		warns = append(warns, "systemd journal unavailable — sys.log operations will fail: "+err.Error())
	}

	return warns
}
