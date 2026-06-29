# SPDX-License-Identifier: Apache-2.0
"""Tests for the cascade read model: tier assignment, effective resolution,
same-tier conflict vs cross-tier override, provenance rendering, and the
end-to-end parser over a throwaway /etc/fail2ban tree."""

import os

from conftest import f2bmod as f2b


# ── _file_tier / _short ───────────────────────────────────────────────────────

def test_file_tier_precedence():
    assert f2b._file_tier('/etc/fail2ban/jail.conf') == 0
    assert f2b._file_tier('/etc/fail2ban/jail.d/sshd.conf') == 1
    assert f2b._file_tier('/etc/fail2ban/jail.local') == 2
    assert f2b._file_tier('/etc/fail2ban/jail.d/sshd.local') == 3


def test_short_renders_jail_d_prefix(etc):
    assert f2b._short(os.path.join(f2b.JAIL_D, 'sshd.local')) == 'jail.d/sshd.local'
    assert f2b._short(os.path.join(f2b.ETC, 'jail.conf')) == 'jail.conf'


# ── pure resolution over synthetic records (tier, file, param, value) ─────────

JC = '/etc/fail2ban/jail.conf'
AAA = '/etc/fail2ban/jail.d/aaa.local'
BBB = '/etc/fail2ban/jail.d/bbb.local'


def test_param_layers_in_precedence_order():
    recs = [(0, JC, 'enabled', 'false'), (3, AAA, 'enabled', 'true')]
    assert f2b._param_layers(recs, 'enabled') == [(0, JC, 'false'), (3, AAA, 'true')]
    assert f2b._param_layers(recs, 'bantime') == []


def test_effective_is_highest_precedence():
    recs = [(0, JC, 'enabled', 'false'), (3, AAA, 'enabled', 'true')]
    assert f2b._effective(recs, 'enabled') == ('true', AAA)
    assert f2b._effective(recs, 'bantime') == (None, None)


def test_param_conflict_same_tier_only():
    same_tier = [(3, AAA, 'maxretry', '5'), (3, BBB, 'maxretry', '10')]
    assert f2b._param_conflict(same_tier, 'maxretry') is True
    cross_tier = [(0, JC, 'maxretry', '3'), (3, AAA, 'maxretry', '5')]
    assert f2b._param_conflict(cross_tier, 'maxretry') is False
    agree = [(3, AAA, 'maxretry', '5'), (3, BBB, 'maxretry', '5')]
    assert f2b._param_conflict(agree, 'maxretry') is False


def test_jail_conflict_and_overridden():
    conflict = [(3, AAA, 'maxretry', '5'), (3, BBB, 'maxretry', '10')]
    assert f2b._jail_conflict(conflict) is True
    overridden = [(0, JC, 'enabled', 'false'), (3, AAA, 'enabled', 'true')]
    assert f2b._jail_conflict(overridden) is False
    assert f2b._jail_overridden(overridden) is True
    single = [(0, JC, 'bantime', '600')]
    assert f2b._jail_overridden(single) is False


def test_provenance_fields_colors(etc):
    jc = os.path.join(f2b.ETC, 'jail.conf')
    aaa = os.path.join(f2b.JAIL_D, 'aaa.local')
    bbb = os.path.join(f2b.JAIL_D, 'bbb.local')
    recs = [
        (0, jc, 'enabled', 'false'), (3, aaa, 'enabled', 'true'),   # cross-tier → warn
        (3, aaa, 'maxretry', '5'), (3, bbb, 'maxretry', '10'),       # same-tier → err
        (0, jc, 'bantime', '600'),                                   # single → no color
    ]
    by_label = {f['label']: f for f in f2b._provenance_fields(recs)}
    assert by_label['enabled']['color'] == 'warn'
    assert 'shadows' in by_label['enabled']['value']
    assert by_label['maxretry']['color'] == 'err'
    assert '[same-tier conflict]' in by_label['maxretry']['value']
    assert 'color' not in by_label['bantime']


# ── end-to-end parser over a real (temp) cascade ──────────────────────────────

def test_cascade_jails_resolves_across_files(etc):
    etc('jail.conf', '[DEFAULT]\nbantime = 999\n'
                     '[sshd]\nenabled = false\nmaxretry = 3\nbantime = 600\n')
    etc('jail.d/aaa.local', '[sshd]\nenabled = true\nmaxretry = 5\n')
    etc('jail.d/bbb.local', '[sshd]\nmaxretry = 10\n')

    jails = f2b._cascade_jails()
    assert 'DEFAULT' not in jails
    recs = jails['sshd']

    assert f2b._effective(recs, 'enabled')[0] == 'true'
    assert f2b._effective(recs, 'enabled')[1].endswith('jail.d/aaa.local')
    assert f2b._effective(recs, 'bantime') == ('600', os.path.join(f2b.ETC, 'jail.conf'))
    # aaa.local then bbb.local are both top tier; filename order makes bbb win.
    assert f2b._effective(recs, 'maxretry')[0] == '10'
    assert f2b._param_conflict(recs, 'maxretry') is True
    assert f2b._param_conflict(recs, 'bantime') is False
