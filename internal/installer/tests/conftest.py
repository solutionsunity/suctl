# SPDX-License-Identifier: Apache-2.0
"""Shared fixtures and fake ORM primitives for suctl-odoo unit tests."""

import json
import os
import socket
import sys
from dataclasses import dataclass

import pytest

# suctl_odoo.py lives one level up; add it to the path so `import suctl_odoo` works.
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import suctl_odoo  # noqa: E402


# ── Fake Odoo record types ────────────────────────────────────────────────────

@dataclass
class FakeModule:
    name: str
    state: str
    installed_version: str = '16.0.1.0.0'
    latest_version:    str = '16.0.1.0.0'


@dataclass
class FakeUser:
    id:     int
    login:  str
    name:   str
    active: bool = True


@dataclass
class FakeDataRecord:
    module:   str
    noupdate: bool = True


# ── Fake ORM primitives ───────────────────────────────────────────────────────

class FakeRecordset:
    """Minimal stand-in for an Odoo recordset. Domain filters are ignored —
    tests control exactly which records are present."""

    def __init__(self, items=None):
        self._items = list(items or [])

    def __len__(self):   return len(self._items)
    def __bool__(self):  return bool(self._items)
    def __iter__(self):  return iter(self._items)

    @property
    def id(self):
        return self._items[0].id if self._items else None

    def mapped(self, field):
        return [getattr(item, field) for item in self._items]

    def filtered(self, pred):
        return FakeRecordset([item for item in self._items if pred(item)])

    def write(self, vals):
        for item in self._items:
            for k, v in vals.items():
                setattr(item, k, v)
        return True


class FakeModel:
    """Mock for an Odoo model. search() returns all records; domain is ignored."""

    def __init__(self, records):
        self._records = list(records)

    def search(self, _domain):
        return FakeRecordset(self._records)

    def search_read(self, _domain, fields, order=None):
        return [{f: getattr(r, f, None) for f in fields} for r in self._records]

    def with_context(self, **_kwargs):
        return self


class FakeEnv:
    def __init__(self, models: dict):
        self._models = models

    def __getitem__(self, model):
        return self._models[model]


def make_env(modules=None, users=None, data_records=None):
    return FakeEnv({
        'ir.module.module': FakeModel(modules or []),
        'ir.model.data':    FakeModel(data_records or []),
        'res.users':        FakeModel(users or []),
    })


# ── Fixtures ──────────────────────────────────────────────────────────────────

@pytest.fixture
def sock_pair():
    """A connected Unix socket pair. Auto-closed after the test."""
    client, server = socket.socketpair(socket.AF_UNIX)
    yield client, server
    client.close()
    server.close()


# ── Protocol helpers (also used by test_integration) ─────────────────────────

def send_cmd(sock, cmd, params=None):
    """Send a JSON command over a socket and return the parsed response."""
    sock.sendall(json.dumps({'cmd': cmd, 'params': params or {}}).encode() + b'\n')
    return _recv(sock)


def send_raw(sock, data: bytes) -> dict:
    sock.sendall(data)
    return _recv(sock)


def _recv(sock) -> dict:
    buf = b''
    while b'\n' not in buf:
        buf += sock.recv(4096)
    return json.loads(buf.split(b'\n')[0])
