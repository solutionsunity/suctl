# SPDX-License-Identifier: Apache-2.0
"""Layer 2 — integration tests against a real suctl-odoo socket.

Requires suctl-odoo to be running (i.e. odoo.service is up with the drop-in active).

Run as the odoo user (the socket is mode 0660, group odoo):

    sudo -u odoo pytest -m integration

or from the installer directory:

    sudo -u odoo python3 -m pytest tests/test_integration.py
"""

import json
import socket

import pytest

SOCKET_PATH = '/run/suctl-odoo/suctl-odoo.sock'


# ── helpers ───────────────────────────────────────────────────────────────────

def _connect():
    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    s.settimeout(10)
    s.connect(SOCKET_PATH)
    return s


def _cmd(cmd, params=None) -> dict:
    return _raw(json.dumps({'cmd': cmd, 'params': params or {}}).encode() + b'\n')


def _raw(data: bytes) -> dict:
    with _connect() as s:
        s.sendall(data)
        buf = b''
        while b'\n' not in buf:
            buf += s.recv(4096)
    return json.loads(buf.split(b'\n')[0])


# ── tests ─────────────────────────────────────────────────────────────────────

@pytest.mark.integration
def test_socket_is_reachable():
    """suctl-odoo is up and accepts connections."""
    with _connect():
        pass  # connection itself is the assertion


@pytest.mark.integration
def test_db_list_returns_ok():
    resp = _cmd('db.list')
    assert resp['ok'] is True
    assert isinstance(resp['data'], list)


@pytest.mark.integration
def test_bad_json_returns_invalid_request():
    resp = _raw(b'not { valid json\n')
    assert resp['ok'] is False
    assert resp['error']['code'] == 'ERR_INVALID_REQUEST'


@pytest.mark.integration
def test_missing_cmd_returns_invalid_request():
    resp = _raw(json.dumps({'params': {}}).encode() + b'\n')
    assert resp['ok'] is False
    assert resp['error']['code'] == 'ERR_INVALID_REQUEST'


@pytest.mark.integration
def test_unknown_cmd_returns_invalid_request():
    resp = _cmd('no.such.command')
    assert resp['ok'] is False
    assert resp['error']['code'] == 'ERR_INVALID_REQUEST'


@pytest.mark.integration
def test_response_is_newline_terminated():
    """CT02: every response ends with \\n."""
    with _connect() as s:
        s.sendall(json.dumps({'cmd': 'db.list'}).encode() + b'\n')
        buf = b''
        while b'\n' not in buf:
            buf += s.recv(4096)
    assert buf.endswith(b'\n')
