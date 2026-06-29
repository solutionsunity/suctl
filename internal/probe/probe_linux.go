// SPDX-License-Identifier: Apache-2.0

//go:build linux

package probe

import (
	"context"

	"github.com/coreos/go-systemd/v22/dbus"
	"github.com/coreos/go-systemd/v22/sdjournal"
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

	// Journal access — required by sys.log.* operations (journald source).
	if j, err := sdjournal.NewJournal(); err != nil {
		warns = append(warns, "systemd journal unavailable — sys.log operations will fail: "+err.Error())
	} else {
		j.Close()
	}

	return warns
}
