# SPDX-License-Identifier: Apache-2.0
"""Tests for _handle: CT02 protocol framing.

No Odoo imports needed — all error paths are caught before reaching any handler.
"""

import json
import socket

import suctl_odoo


# ── helpers ───────────────────────────────────────────────────────────────────

def _handle_bytes(data: bytes):
    """Push `data` through _handle via a socketpair; return parsed response or None."""
    client, server = socket.socketpair(socket.AF_UNIX)
    try:
        client.sendall(data)
        client.shutdown(socket.SHUT_WR)
        suctl_odoo._handle(server)
        server.close()

        buf = b''
        while True:
            chunk = client.recv(4096)
            if not chunk:
                break
            buf += chunk

        return json.loads(buf.split(b'\n')[0]) if buf else None
    finally:
        client.close()


# ── tests ─────────────────────────────────────────────────────────────────────

def test_bad_json_returns_invalid_request():
    resp = _handle_bytes(b'not { valid } json\n')
    assert resp['ok'] is False
    assert resp['error']['code'] == 'ERR_INVALID_REQUEST'


def test_missing_cmd_field_returns_invalid_request():
    resp = _handle_bytes(json.dumps({'params': {}}).encode() + b'\n')
    assert resp['ok'] is False
    assert resp['error']['code'] == 'ERR_INVALID_REQUEST'


def test_unknown_cmd_returns_invalid_request():
    resp = _handle_bytes(json.dumps({'cmd': 'no.such.thing'}).encode() + b'\n')
    assert resp['ok'] is False
    assert resp['error']['code'] == 'ERR_INVALID_REQUEST'


def test_empty_connection_returns_cleanly():
    """Peer closes without sending anything — _handle must not raise."""
    client, server = socket.socketpair(socket.AF_UNIX)
    client.close()
    suctl_odoo._handle(server)   # must not raise
    server.close()


def test_response_is_newline_terminated():
    """Every response must end with \\n per CT02."""
    client, server = socket.socketpair(socket.AF_UNIX)
    client.sendall(json.dumps({'cmd': 'no.such'}).encode() + b'\n')
    client.shutdown(socket.SHUT_WR)
    suctl_odoo._handle(server)
    server.close()

    buf = b''
    while True:
        chunk = client.recv(4096)
        if not chunk:
            break
        buf += chunk
    client.close()

    assert buf.endswith(b'\n')


def test_only_first_line_is_consumed():
    """Extra bytes after \\n are part of the next request — ignored here."""
    first  = json.dumps({'cmd': 'no.such'}).encode() + b'\n'
    second = b'leftover bytes that belong to the next request'
    resp = _handle_bytes(first + second)
    assert resp is not None
    assert resp['error']['code'] == 'ERR_INVALID_REQUEST'
