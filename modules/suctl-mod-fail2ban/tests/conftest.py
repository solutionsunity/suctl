# SPDX-License-Identifier: Apache-2.0
"""Shared fixtures for suctl-mod-fail2ban unit tests.

The entrypoint is hyphenated and has no .py suffix, so it cannot be imported by
name. Load it once via SourceFileLoader and expose it as `f2bmod` (the module
itself prepends sdk/python to sys.path, so `import suctlmod` resolves in-tree)."""

import importlib.machinery
import importlib.util
import os
import sys

import pytest

_MODDIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_ENTRY = os.path.join(_MODDIR, 'suctl-mod-fail2ban')

_loader = importlib.machinery.SourceFileLoader('f2bmod', _ENTRY)
_spec = importlib.util.spec_from_loader('f2bmod', _loader)
f2bmod = importlib.util.module_from_spec(_spec)
_loader.exec_module(f2bmod)
sys.modules['f2bmod'] = f2bmod


@pytest.fixture
def f2b():
    """The loaded suctl-mod-fail2ban module."""
    return f2bmod


@pytest.fixture
def etc(tmp_path, monkeypatch):
    """A throwaway /etc/fail2ban tree. Points the module's ETC/JAIL_D at it and
    returns a writer: write(relpath, text) creates files under the temp tree."""
    jail_d = tmp_path / 'jail.d'
    jail_d.mkdir()
    monkeypatch.setattr(f2bmod, 'ETC', str(tmp_path))
    monkeypatch.setattr(f2bmod, 'JAIL_D', str(jail_d))

    def write(rel, text):
        p = tmp_path / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text)
        return str(p)

    return write
