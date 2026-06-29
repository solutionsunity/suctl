#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""
suctl-odoo — Unix socket bridge: suctl → Odoo Python layer.

Runs as ExecStartPost inside Odoo's systemd unit. Inherits the odoo user,
virtualenv, and ODOO_RC. Single-threaded: one connection, one command, one
response, repeat.

Socket : /run/suctl-odoo.sock  (mode 0660, owner odoo:odoo)
Protocol: newline-delimited JSON — see docs/contracts/CT02-socket-protocol.md
Errors  : ERR_* codes          — see docs/contracts/CT03-error-codes.md
"""

import datetime
import json
import logging
import os
import signal
import socket
import sys

SOCKET_PATH = '/run/suctl-odoo/suctl-odoo.sock'
SOCKET_MODE = 0o660
LOG_FILE    = '/var/log/suctl/suctl-odoo.log'

log = logging.getLogger('suctl-odoo')


# ── Logging ───────────────────────────────────────────────────────────────────

class _PlainFormatter(logging.Formatter):
    """Write one human-readable line per record matching Linux log convention:

    2026-05-16 14:23:14 UTC  INFO   suctl-odoo  config loaded from /etc/odoo/odoo.conf

    The format is intentionally identical to the Go side so both log files
    read consistently.  Structured fields can be appended as key=val pairs
    in future without changing the file format contract.
    """

    def format(self, record):
        record.message = record.getMessage()
        ts = datetime.datetime.fromtimestamp(record.created, tz=datetime.timezone.utc)
        line = (
            f"{ts.strftime('%Y-%m-%d %H:%M:%S UTC')}  "
            f"{record.levelname:<5}  suctl-odoo  {record.message}"
        )
        if record.exc_info:
            line += '\n' + self.formatException(record.exc_info)
        return line


def _setup_logging():
    """Configure plain-text logging to file (primary) and stderr (secondary)."""
    root = logging.getLogger()
    root.setLevel(logging.INFO)
    fmt = _PlainFormatter()

    # Odoo's bootstrap calls logging.captureWarnings(True) which routes all
    # Python DeprecationWarnings through the 'py.warnings' logger.  Those
    # messages are noisy and not actionable by the operator, so we raise the
    # threshold on that logger to ERROR.
    logging.getLogger('py.warnings').setLevel(logging.ERROR)

    # Primary: log file.  Gracefully skip if the directory doesn't exist yet.
    try:
        fh = logging.FileHandler(LOG_FILE)
        fh.setFormatter(fmt)
        root.addHandler(fh)
    except OSError as exc:
        pass  # will warn on stderr below

    # Secondary: stderr (captured by journald when running under systemd).
    sh = logging.StreamHandler(sys.stderr)
    sh.setFormatter(fmt)
    root.addHandler(sh)


# ── Odoo bootstrap ────────────────────────────────────────────────────────────

def _bootstrap():
    import odoo.tools.config as _cfg
    conf = (
        os.environ.get('ODOO_RC')
        or os.environ.get('OPENERP_SERVER')
        or '/etc/odoo/odoo.conf'
    )
    _cfg.parse_config(['--config', conf])
    log.info('config loaded from %s', conf)


# ── Protocol helpers ──────────────────────────────────────────────────────────

def _ok(data):
    return {'ok': True, 'data': data}


def _err(code, message):
    return {'ok': False, 'error': {'code': code, 'message': message}}


def _require(params, key):
    val = params.get(key)
    if val is None:
        raise ValueError(f'missing required param: {key}')
    return val


def _require_list(params, key):
    val = _require(params, key)
    if not isinstance(val, list) or not val:
        raise ValueError(f'{key} must be a non-empty list')
    return val


# ── Odoo environment helpers ──────────────────────────────────────────────────

def _cursor(db):
    from odoo import registry
    return registry(db).cursor()


def _env(cr):
    from odoo import api, SUPERUSER_ID
    return api.Environment(cr, SUPERUSER_ID, {})


# ── Module commands ───────────────────────────────────────────────────────────

def cmd_module_list(params):
    db = _require(params, 'db')
    with _cursor(db) as cr:
        rows = _env(cr)['ir.module.module'].search_read(
            [],
            ['name', 'shortdesc', 'state', 'installed_version', 'latest_version'],
            order='name asc',
        )
    return _ok(rows)


def _module_transition(db, names, to_state, *, requires_installed, result_key):
    """Validate module states, write the transition, trigger registry update.

    requires_installed=True  → modules must currently be installed (upgrade/uninstall)
    requires_installed=False → modules must not yet be installed (install)
    """
    from odoo.modules.registry import Registry

    with _cursor(db) as cr:
        env  = _env(cr)
        mods = env['ir.module.module'].search([('name', 'in', names)])

        missing = set(names) - set(mods.mapped('name'))
        if missing:
            return _err('ERR_MODULE_NOT_FOUND',
                        f'not in addons path: {sorted(missing)}')

        if requires_installed:
            wrong, msg = mods.filtered(lambda m: m.state != 'installed'), 'not installed'
        else:
            wrong, msg = mods.filtered(lambda m: m.state == 'installed'), 'already installed'

        if wrong:
            return _err('ERR_MODULE_CONFLICT',
                        f'{msg}: {sorted(wrong.mapped("name"))}')

        mods.write({'state': to_state})

    Registry.new(db, update_module=True)
    return _ok({result_key: names})


def cmd_module_install(params):
    db, names = _require(params, 'db'), _require_list(params, 'names')
    return _module_transition(db, names, 'to install',
                              requires_installed=False, result_key='installed')


def cmd_module_upgrade(params):
    db, names = _require(params, 'db'), _require_list(params, 'names')
    return _module_transition(db, names, 'to upgrade',
                              requires_installed=True, result_key='upgraded')


def cmd_module_upgrade_all(params):
    """Upgrade every installed module in the database.

    Equivalent to ``odoo -u all``.  No ``names`` param — the full list
    of installed modules is retrieved from the database automatically.
    """
    from odoo.modules.registry import Registry

    db = _require(params, 'db')
    with _cursor(db) as cr:
        env      = _env(cr)
        installed = env['ir.module.module'].search([('state', '=', 'installed')])
        if not installed:
            return _ok({'upgraded': 0, 'names': []})
        names = installed.mapped('name')
        installed.write({'state': 'to upgrade'})

    Registry.new(db, update_module=True)
    return _ok({'upgraded': len(names), 'names': names})


def cmd_module_uninstall(params):
    db, names = _require(params, 'db'), _require_list(params, 'names')
    return _module_transition(db, names, 'to remove',
                              requires_installed=True, result_key='uninstalled')


def cmd_module_detect_changes(params):
    """Return installed modules whose manifest version differs from the DB record."""
    db = _require(params, 'db')
    with _cursor(db) as cr:
        rows = _env(cr)['ir.module.module'].search_read(
            [('state', '=', 'installed')],
            ['name', 'installed_version', 'latest_version'],
        )
    changes = [
        r for r in rows
        if r['installed_version'] != r['latest_version']
    ]
    return _ok({'changes': changes})


def cmd_module_force_update_data(params):
    """Re-apply data records for a module, bypassing noupdate=1 guards."""
    db   = _require(params, 'db')
    name = _require(params, 'name')

    from odoo.modules.registry import Registry

    with _cursor(db) as cr:
        env = _env(cr)
        mod = env['ir.module.module'].search(
            [('name', '=', name), ('state', '=', 'installed')]
        )
        if not mod:
            return _err('ERR_MODULE_NOT_FOUND',
                        f'module {name!r} is not installed')

        locked = env['ir.model.data'].search(
            [('module', '=', name), ('noupdate', '=', True)]
        )
        count = len(locked)
        locked.write({'noupdate': False})
        mod.write({'state': 'to upgrade'})
        # cursor commits on exit; Registry.new() sees the committed state

    Registry.new(db, update_module=True)
    return _ok({'name': name, 'unlocked_records': count})


# ── Database commands ─────────────────────────────────────────────────────────

def cmd_db_list(_params):
    from odoo.service.db import list_dbs
    return _ok(list_dbs(force=True))


def cmd_db_create(params):
    name = _require(params, 'name')
    from odoo.service.db import exp_create_database
    exp_create_database(name, False, 'en_US')
    return _ok({'name': name})


def cmd_db_duplicate(params):
    source = _require(params, 'source')
    target = _require(params, 'target')
    from odoo.service.db import exp_duplicate_database
    exp_duplicate_database(source, target)
    return _ok({'source': source, 'target': target})


# ── User commands ─────────────────────────────────────────────────────────────

def cmd_user_list(params):
    db = _require(params, 'db')
    with _cursor(db) as cr:
        rows = _env(cr)['res.users'].with_context(active_test=False).search_read(
            [('share', '=', False)],
            ['login', 'name', 'active'],
            order='name asc',
        )
    return _ok(rows)


def cmd_user_reset_password(params):
    db       = _require(params, 'db')
    login    = _require(params, 'login')
    password = _require(params, 'password')
    with _cursor(db) as cr:
        env  = _env(cr)
        user = env['res.users'].with_context(active_test=False).search(
            [('login', '=', login)]
        )
        if not user:
            return _err('ERR_NOT_FOUND', f'user {login!r} not found')
        user.write({'password': password})
    return _ok({'login': login})


def _user_set_active(db, login, active):
    with _cursor(db) as cr:
        env  = _env(cr)
        user = env['res.users'].with_context(active_test=False).search(
            [('login', '=', login)]
        )
        if not user:
            return _err('ERR_NOT_FOUND', f'user {login!r} not found')
        if not active and user.id == 1:
            return _err('ERR_INVALID_REQUEST',
                        'cannot deactivate the administrator account')
        user.write({'active': active})
    return _ok({'login': login, 'active': active})


def cmd_user_activate(params):
    return _user_set_active(_require(params, 'db'), _require(params, 'login'), True)


def cmd_user_deactivate(params):
    return _user_set_active(_require(params, 'db'), _require(params, 'login'), False)


# ── Dispatch ──────────────────────────────────────────────────────────────────

_COMMANDS = {
    'module.list':              cmd_module_list,
    'module.install':           cmd_module_install,
    'module.upgrade':           cmd_module_upgrade,
    'module.upgrade_all':       cmd_module_upgrade_all,
    'module.uninstall':         cmd_module_uninstall,
    'module.detect_changes':    cmd_module_detect_changes,
    'module.force_update_data': cmd_module_force_update_data,
    'db.list':                  cmd_db_list,
    'db.create':                cmd_db_create,
    'db.duplicate':             cmd_db_duplicate,
    'user.list':                cmd_user_list,
    'user.reset_password':      cmd_user_reset_password,
    'user.activate':            cmd_user_activate,
    'user.deactivate':          cmd_user_deactivate,
}


def _dispatch(request):
    cmd    = request.get('cmd')
    params = request.get('params') or {}

    if not cmd:
        log.warning('missing cmd field in request')
        return _err('ERR_INVALID_REQUEST', 'missing cmd')

    handler = _COMMANDS.get(cmd)
    if not handler:
        log.warning('unknown command %r', cmd)
        return _err('ERR_INVALID_REQUEST', f'unknown command: {cmd!r}')

    log.info('cmd: %s', cmd)
    try:
        result = handler(params)
    except ValueError as exc:
        log.warning('invalid request: cmd=%r err=%s', cmd, exc)
        return _err('ERR_INVALID_REQUEST', str(exc))
    except Exception as exc:
        log.exception('command %r failed', cmd)
        # Surface DB-unreachable as a distinct code so suctl can report it cleanly.
        name = type(exc).__name__
        if 'OperationalError' in name or 'InterfaceError' in name:
            return _err('ERR_ODOO_NOT_READY', 'database not reachable')
        return _err('ERR_INTERNAL', str(exc))

    if result.get('ok'):
        log.info('ok: %s', cmd)
    else:
        code = (result.get('error') or {}).get('code', 'ERR_UNKNOWN')
        log.warning('fail: cmd=%s code=%s', cmd, code)
    return result


# ── Connection handler ────────────────────────────────────────────────────────

def _handle(conn):
    """Read one newline-terminated JSON request; write one JSON response."""
    buf = b''
    try:
        while b'\n' not in buf:
            chunk = conn.recv(4096)
            if not chunk:
                return
            buf += chunk

        line = buf.split(b'\n', 1)[0]

        try:
            request = json.loads(line)
        except json.JSONDecodeError as exc:
            response = _err('ERR_INVALID_REQUEST', f'JSON parse error: {exc}')
        else:
            response = _dispatch(request)

        conn.sendall(json.dumps(response).encode() + b'\n')

    except OSError:
        pass  # connection reset by peer — normal on suctl-side timeout


# ── Entry point ───────────────────────────────────────────────────────────────

def main():
    # Must call before fork so child inherits open file handle.
    _setup_logging()

    _bootstrap()

    # Remove a stale socket left by a previous unclean shutdown.
    try:
        os.unlink(SOCKET_PATH)
    except FileNotFoundError:
        pass

    sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    sock.bind(SOCKET_PATH)
    os.chmod(SOCKET_PATH, SOCKET_MODE)
    sock.listen(5)

    # Daemonize: fork so the parent exits immediately and systemd considers
    # ExecStartPost complete (success).  The child stays alive as the socket
    # server.  We fork *after* binding so any permission / address errors
    # surface before we hand control back to systemd.
    _pid = os.fork()
    if _pid > 0:
        sys.exit(0)   # parent — tell systemd we're done
    os.setsid()       # child — detach from the controlling terminal

    log.info('listening on %s', SOCKET_PATH)

    def _shutdown(signum, _frame):
        log.info('signal %d — shutting down', signum)
        sock.close()
        sys.exit(0)

    signal.signal(signal.SIGTERM, _shutdown)
    signal.signal(signal.SIGINT,  _shutdown)

    while True:
        try:
            conn, _ = sock.accept()
        except OSError:
            break  # socket closed by signal handler
        try:
            _handle(conn)
        finally:
            conn.close()


if __name__ == '__main__':
    main()
