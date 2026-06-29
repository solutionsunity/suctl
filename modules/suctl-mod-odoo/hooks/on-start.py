#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# on-start hook for suctl-mod-odoo — per-boot stamp reconciliation (D67).
#
# Fires every time suctl starts for an already-activated module whose installed
# content is unchanged since activation. suctl runs intermittently, so an Odoo
# operator may have installed or removed modules while suctl-mod-odoo was down.
# This hook asks suctl-odoo-service to reconcile its checksum stamps against disk
# and every database: baseline a stamp for any installed module missing one,
# prune stamps for modules no longer installed, leave valid stamps untouched.
#
# Best-effort by contract. Reconcile needs Odoo's bridge socket, whose lifecycle
# Odoo itself owns (ExecStartPost). If Odoo is still coming up or is down, the
# hook waits briefly, then exits 0 regardless — a transient Odoo-down must never
# abort module activation. Degraded service is reported by health separately; the
# next start (or an explicit odoo.server.reconcile) catches up.
#
# This hook runs BEFORE the module process exists, so it cannot use the module's
# broker wire. It speaks the bridge protocol (newline-delimited JSON) directly,
# the same wire suctl-mod-odoo uses at runtime. The work is pure Python (socket,
# JSON, retry loop) — no shell wrapper, unlike the systemctl-driven sibling hooks.
#
# Environment provided by suctl: SUCTL_MODULE, SUCTL_EVENT, SUCTL_MODULE_DIR,
# SUCTL_CONF_DIR. Always exits 0.
import json
import os
import socket
import sys
import time

SOCK = "/run/suctl/suctl-mod-odoo/odoo.sock"
WAIT_SECONDS = float(os.environ.get("SUCTL_ODOO_RECONCILE_WAIT", "15"))
RECV_TIMEOUT = 120  # covers a reconcile that walks many modules across dbs


def fail(msg):
    # Soft failure — log and exit 0 so activation is never aborted.
    print(f"[on-start] {msg} — skipping reconcile (retries next start)")
    sys.exit(0)


def main():
    print("[on-start] reconciling suctl checksum stamps")
    deadline = time.monotonic() + WAIT_SECONDS
    request = json.dumps({"name": "odoo.server.reconcile", "args": {}}).encode() + b"\n"

    while True:
        s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        s.settimeout(RECV_TIMEOUT)
        try:
            s.connect(SOCK)
        except OSError:
            s.close()
            if time.monotonic() >= deadline:
                fail("odoo bridge socket not up")
            time.sleep(1)
            continue
        try:
            s.sendall(request)
            buf = b""
            while b"\n" not in buf:
                chunk = s.recv(4096)
                if not chunk:
                    fail("odoo bridge closed without response")
                buf += chunk
            resp = json.loads(buf.split(b"\n", 1)[0])
        except (OSError, json.JSONDecodeError) as exc:
            fail(f"reconcile call failed: {exc}")
        finally:
            s.close()
        if resp.get("ok"):
            dbs = (resp.get("data") or {}).get("databases") or {}
            print(f"[on-start] reconcile ok across {len(dbs)} database(s)")
        else:
            err = resp.get("error") or {}
            print(f"[on-start] reconcile reported error: {err.get('message', 'unknown')} — continuing")
        break

    print("[on-start] done")
    sys.exit(0)


if __name__ == "__main__":
    main()
