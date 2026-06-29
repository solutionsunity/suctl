# SPDX-License-Identifier: Apache-2.0
"""Tests for user commands: _user_set_active and cmd_user_reset_password."""

from unittest.mock import MagicMock, patch

import suctl_odoo
from conftest import FakeUser, make_env


# ── helper ────────────────────────────────────────────────────────────────────

def _run_set_active(users, login, active):
    env = make_env(users=users)
    with patch('suctl_odoo._cursor', return_value=MagicMock()), \
         patch('suctl_odoo._env', return_value=env):
        return suctl_odoo._user_set_active('testdb', login, active)


# ── _user_set_active ──────────────────────────────────────────────────────────

def test_activate_user_not_found():
    result = _run_set_active([], 'ghost@example.com', True)
    assert not result['ok']
    assert result['error']['code'] == 'ERR_NOT_FOUND'


def test_deactivate_user_not_found():
    result = _run_set_active([], 'ghost@example.com', False)
    assert not result['ok']
    assert result['error']['code'] == 'ERR_NOT_FOUND'


def test_deactivate_admin_is_blocked():
    users = [FakeUser(id=1, login='admin', name='Administrator')]
    result = _run_set_active(users, 'admin', False)
    assert not result['ok']
    assert result['error']['code'] == 'ERR_INVALID_REQUEST'
    assert 'administrator' in result['error']['message'].lower()


def test_activate_admin_is_allowed():
    """Only deactivation of id=1 is blocked; activation is fine."""
    users = [FakeUser(id=1, login='admin', name='Administrator', active=False)]
    result = _run_set_active(users, 'admin', True)
    assert result['ok']
    assert users[0].active is True


def test_activate_sets_active_true():
    users = [FakeUser(id=2, login='user@example.com', name='Alice', active=False)]
    result = _run_set_active(users, 'user@example.com', True)
    assert result['ok']
    assert result['data'] == {'login': 'user@example.com', 'active': True}
    assert users[0].active is True


def test_deactivate_sets_active_false():
    users = [FakeUser(id=2, login='user@example.com', name='Alice', active=True)]
    result = _run_set_active(users, 'user@example.com', False)
    assert result['ok']
    assert result['data'] == {'login': 'user@example.com', 'active': False}
    assert users[0].active is False


# ── cmd_user_reset_password ───────────────────────────────────────────────────

def _run_reset(users, login, password):
    env = make_env(users=users)
    with patch('suctl_odoo._cursor', return_value=MagicMock()), \
         patch('suctl_odoo._env', return_value=env):
        return suctl_odoo.cmd_user_reset_password(
            {'db': 'testdb', 'login': login, 'password': password}
        )


def test_reset_password_user_not_found():
    result = _run_reset([], 'ghost@example.com', 'newpass')
    assert not result['ok']
    assert result['error']['code'] == 'ERR_NOT_FOUND'


def test_reset_password_happy_path():
    users = [FakeUser(id=2, login='user@example.com', name='Alice')]
    result = _run_reset(users, 'user@example.com', 'newpass123')
    assert result['ok']
    assert result['data']['login'] == 'user@example.com'
    assert users[0].password == 'newpass123'


def test_reset_password_missing_db():
    resp = suctl_odoo._dispatch({'cmd': 'user.reset_password', 'params': {'login': 'x', 'password': 'y'}})
    assert not resp['ok']
    assert resp['error']['code'] == 'ERR_INVALID_REQUEST'


def test_reset_password_missing_login():
    resp = suctl_odoo._dispatch({'cmd': 'user.reset_password', 'params': {'db': 'testdb', 'password': 'y'}})
    assert not resp['ok']
    assert resp['error']['code'] == 'ERR_INVALID_REQUEST'


# ── cmd_user_list ─────────────────────────────────────────────────────────────

def test_user_list_returns_all_internal_users():
    users = [
        FakeUser(id=2, login='alice@example.com', name='Alice'),
        FakeUser(id=3, login='bob@example.com',   name='Bob', active=False),
    ]
    env = make_env(users=users)
    with patch('suctl_odoo._cursor', return_value=MagicMock()), \
         patch('suctl_odoo._env', return_value=env):
        result = suctl_odoo.cmd_user_list({'db': 'testdb'})

    assert result['ok']
    assert len(result['data']) == 2
    logins = {r['login'] for r in result['data']}
    assert logins == {'alice@example.com', 'bob@example.com'}
