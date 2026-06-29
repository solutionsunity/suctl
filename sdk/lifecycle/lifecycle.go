// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"os"
	"syscall"
)

// StopSignals returns the set of OS signals that mean "graceful stop" for a
// suctl process — used by both core's wait loop and a module's modserver
// wait. It is the listening counterpart of Stop: a process blocks on these and
// begins shutdown when one arrives.
//
// The same list is correct on every platform. os.Interrupt is portable: on Unix
// it is SIGINT; on Windows the runtime synthesises it from Ctrl-C and Ctrl-Break
// (the latter is what Stop sends to a supervised child). syscall.SIGTERM is the
// conventional service-stop signal on Unix and is also delivered by the Windows
// runtime for CTRL_CLOSE/LOGOFF/SHUTDOWN, giving a top-level process a chance to
// clean up before the OS terminates it.
func StopSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}
