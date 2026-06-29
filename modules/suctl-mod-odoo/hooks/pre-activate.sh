#!/usr/bin/env bash
# pre-activate hook for suctl-mod-odoo
#
# Runs ONLY on true first activation (or re-activation after explicit
# deactivation). It wires suctl-odoo-service into Odoo's systemd unit:
#   1. Installs a systemd drop-in that:
#        - declares RuntimeDirectory=suctl/suctl-mod-odoo so systemd creates
#          and owns /run/suctl/suctl-mod-odoo/ as the Odoo service user on
#          every service start — including after OS reboots. No tmpfiles.d needed.
#        - adds ExecStartPost to launch suctl-odoo-service after Odoo starts.
#   2. Restarts Odoo so the Odoo-side socket comes up before suctl-mod-odoo
#      starts accepting invoke calls.
#
# Why RuntimeDirectory instead of tmpfiles.d: RuntimeDirectory= is declared on
# the service unit that owns the directory. systemd creates it with the correct
# user, group, and mode before every service start — no separate boot-time job,
# no timing race, no on-start reconciliation hook needed.
#
# Why glue into Odoo's service instead of calling odoo-bin: Odoo operations need
# the live service's virtualenv, ORM, database connections, and module registry.
# An external odoo-bin call spawns a parallel Odoo with different state.
#
# Environment variables provided by suctl:
#   SUCTL_MODULE_DIR — absolute path to this module's directory
#   SUCTL_EVENT      — "pre-activate"
#
# Exit 0  → suctl proceeds with activation.
# Exit !0 → suctl marks activation failed.
set -euo pipefail

ODOO_UNIT="${ODOO_UNIT:-odoo.service}"
DROPIN_DIR="/etc/systemd/system/${ODOO_UNIT}.d"
DROPIN_FILE="${DROPIN_DIR}/suctl-mod-odoo.conf"
SERVICE_BIN="${SUCTL_MODULE_DIR}/suctl-odoo-service"

echo "[pre-activate] wiring suctl-odoo-service into ${ODOO_UNIT}"

if [ ! -f /etc/odoo/odoo.conf ]; then
    echo "[pre-activate] ERROR: /etc/odoo/odoo.conf not found — is Odoo installed?" >&2
    exit 1
fi

if [ ! -x "${SERVICE_BIN}" ]; then
    echo "[pre-activate] ERROR: ${SERVICE_BIN} is not executable" >&2
    exit 1
fi

# Write the drop-in (idempotent — same content each time).
# RuntimeDirectory= instructs systemd to create /run/suctl/suctl-mod-odoo/
# owned by the Odoo service user before every service start, eliminating the
# need for tmpfiles.d and any per-boot reconciliation hook.
install -d -m 0755 "${DROPIN_DIR}"
cat > "${DROPIN_FILE}" <<EOF
# Managed by suctl-mod-odoo. Do not edit manually.
[Service]
RuntimeDirectory=suctl/suctl-mod-odoo
RuntimeDirectoryMode=0700
ExecStartPost=${SERVICE_BIN}
EOF
echo "[pre-activate] drop-in written: ${DROPIN_FILE}"

# Reload systemd and restart Odoo to bring the Odoo-side socket online.
systemctl daemon-reload
echo "[pre-activate] restarting ${ODOO_UNIT} ..."
systemctl restart "${ODOO_UNIT}"
echo "[pre-activate] ${ODOO_UNIT} restarted — suctl-odoo-service socket should be up"
