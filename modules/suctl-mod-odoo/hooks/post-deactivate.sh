#!/usr/bin/env bash
# post-deactivate hook for suctl-mod-odoo
#
# Runs after suctl has stopped the module process. It removes the systemd
# drop-in then restarts Odoo so RuntimeDirectory= and ExecStartPost= are gone
# and Odoo returns to its normal lifecycle.
#
# The socket directory (/run/suctl/suctl-mod-odoo/) is managed via
# RuntimeDirectory= in the drop-in — removing the drop-in and restarting Odoo
# is sufficient; systemd will not recreate the directory on the next start.
#
# Environment variables provided by suctl:
#   SUCTL_MODULE_DIR  — absolute path to this module's directory
#   SUCTL_MODULE      — "suctl-mod-odoo"
#   SUCTL_EVENT       — "post-deactivate"
#
# Exit 0  → cleanup complete.
# Exit !0 → suctl logs the failure; module is still deactivated.
set -euo pipefail

ODOO_UNIT="${ODOO_UNIT:-odoo.service}"
DROPIN_FILE="/etc/systemd/system/${ODOO_UNIT}.d/suctl-mod-odoo.conf"

echo "[post-deactivate] removing systemd drop-in for ${ODOO_UNIT}"

if [ -f "${DROPIN_FILE}" ]; then
    rm -f "${DROPIN_FILE}"
    echo "[post-deactivate] removed: ${DROPIN_FILE}"
else
    echo "[post-deactivate] drop-in already absent — nothing to remove"
fi

systemctl daemon-reload
echo "[post-deactivate] restarting ${ODOO_UNIT} ..."
systemctl restart "${ODOO_UNIT}"
echo "[post-deactivate] ${ODOO_UNIT} restarted — suctl-odoo-service unwired"
