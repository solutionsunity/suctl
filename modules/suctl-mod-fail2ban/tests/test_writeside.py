# SPDX-License-Identifier: Apache-2.0
"""Tests for the deterministic write side: effective-enabled resolution across
the cascade, the lower-precedence override note (which never names our own
drop-in), and the drop-in writer."""

import os

from conftest import f2bmod as f2b

JC = '/etc/fail2ban/jail.conf'
JL = '/etc/fail2ban/jail.local'


# ── _effective_enabled ────────────────────────────────────────────────────────

def test_effective_enabled_true_false_none():
    assert f2b._effective_enabled([(0, JC, 'enabled', 'true')]) is True
    assert f2b._effective_enabled([(0, JC, 'enabled', 'false')]) is False
    # later (higher-precedence) record wins
    assert f2b._effective_enabled(
        [(0, JC, 'enabled', 'false'), (2, JL, 'enabled', 'true')]) is True
    # no per-jail enabled anywhere → None (only [DEFAULT] would govern)
    assert f2b._effective_enabled([(0, JC, 'maxretry', '5')]) is None


# ── _enabled_override_note ────────────────────────────────────────────────────

def test_override_note_flags_lower_tier_on_deactivate(etc):
    jc = os.path.join(f2b.ETC, 'jail.conf')
    recs = [(0, jc, 'enabled', 'true')]
    note = f2b._enabled_override_note(recs, asserted=False, jail='sshd')
    assert note is not None
    assert 'true (jail.conf)' in note
    # activating (asserted=True) agrees with the lower tier → no note
    assert f2b._enabled_override_note(recs, asserted=True, jail='sshd') is None


def test_override_note_excludes_our_own_dropin(etc):
    jl = os.path.join(f2b.ETC, 'jail.local')
    ours = f2b._dropin_path('sshd')
    recs = [(2, jl, 'enabled', 'true'), (3, ours, 'enabled', 'true')]
    note = f2b._enabled_override_note(recs, asserted=False, jail='sshd')
    assert note is not None
    assert 'jail.local' in note
    assert 'sshd.local' not in note  # our own drop-in is never reported


def test_override_note_none_when_no_clash(etc):
    assert f2b._enabled_override_note([], asserted=False, jail='sshd') is None
    recs = [(0, os.path.join(f2b.ETC, 'jail.conf'), 'maxretry', '5')]
    assert f2b._enabled_override_note(recs, asserted=False, jail='sshd') is None


# ── _write_dropin ─────────────────────────────────────────────────────────────

def test_write_dropin_marker_enabled_and_params(etc):
    settings = {'maxretry': '5', 'bantime': '600', 'findtime': None,
                'filter': '', 'ignoreip': '10.0.0.1'}
    f2b._write_dropin('sshd', settings, enabled=True)
    text = open(f2b._dropin_path('sshd')).read()
    lines = text.splitlines()
    assert lines[0] == f2b.MARKER
    assert lines[1] == '[sshd]'
    assert 'enabled = true' in lines
    assert 'maxretry = 5' in lines
    assert 'bantime = 600' in lines
    assert 'ignoreip = 10.0.0.1' in lines
    # None and '' values are skipped entirely
    assert not any(line.startswith('findtime') for line in lines)
    assert not any(line.startswith('filter') for line in lines)


def test_write_dropin_omits_enabled_when_none(etc):
    f2b._write_dropin('sshd', {'maxretry': '3'}, enabled=None)
    lines = open(f2b._dropin_path('sshd')).read().splitlines()
    assert not any(line.startswith('enabled') for line in lines)
    assert 'maxretry = 3' in lines


def test_write_dropin_false_is_explicit(etc):
    f2b._write_dropin('sshd', {}, enabled=False)
    assert 'enabled = false' in open(f2b._dropin_path('sshd')).read().splitlines()
