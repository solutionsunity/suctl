# SPDX-License-Identifier: Apache-2.0
"""Tests for module commands: _module_transition, detect_changes, force_update_data."""

from unittest.mock import MagicMock, patch

import suctl_odoo
from conftest import FakeDataRecord, FakeModule, make_env


# ── helper ────────────────────────────────────────────────────────────────────

def _run_transition(modules, names, to_state, *, requires_installed, result_key, db='testdb'):
    env = make_env(modules=modules)
    with patch('suctl_odoo._cursor', return_value=MagicMock()), \
         patch('suctl_odoo._env', return_value=env), \
         patch('odoo.modules.registry.Registry') as reg:
        result = suctl_odoo._module_transition(
            db, names, to_state,
            requires_installed=requires_installed,
            result_key=result_key,
        )
    return result, reg


# ── _module_transition: validation ────────────────────────────────────────────

def test_missing_module_returns_not_found():
    result, _ = _run_transition([], ['sale'], 'to install',
                                requires_installed=False, result_key='installed')
    assert not result['ok']
    assert result['error']['code'] == 'ERR_MODULE_NOT_FOUND'


def test_install_already_installed_returns_conflict():
    mods = [FakeModule('sale', 'installed')]
    result, _ = _run_transition(mods, ['sale'], 'to install',
                                requires_installed=False, result_key='installed')
    assert not result['ok']
    assert result['error']['code'] == 'ERR_MODULE_CONFLICT'
    assert 'already installed' in result['error']['message']


def test_upgrade_not_installed_returns_conflict():
    mods = [FakeModule('sale', 'uninstalled')]
    result, _ = _run_transition(mods, ['sale'], 'to upgrade',
                                requires_installed=True, result_key='upgraded')
    assert not result['ok']
    assert result['error']['code'] == 'ERR_MODULE_CONFLICT'
    assert 'not installed' in result['error']['message']


def test_partial_missing_returns_not_found():
    """One present, one missing — should surface the missing one."""
    mods = [FakeModule('sale', 'uninstalled')]
    result, _ = _run_transition(mods, ['sale', 'ghost'], 'to install',
                                requires_installed=False, result_key='installed')
    assert not result['ok']
    assert result['error']['code'] == 'ERR_MODULE_NOT_FOUND'
    assert 'ghost' in result['error']['message']


# ── _module_transition: happy paths ───────────────────────────────────────────

def test_install_writes_state_and_triggers_registry_update():
    mods = [FakeModule('sale', 'uninstalled')]
    result, reg = _run_transition(mods, ['sale'], 'to install',
                                  requires_installed=False, result_key='installed')
    assert result['ok']
    assert result['data']['installed'] == ['sale']
    assert mods[0].state == 'to install'
    reg.new.assert_called_once_with('testdb', update_module=True)


def test_upgrade_writes_state_and_triggers_registry_update():
    mods = [FakeModule('sale', 'installed'), FakeModule('stock', 'installed')]
    result, reg = _run_transition(mods, ['sale', 'stock'], 'to upgrade',
                                  requires_installed=True, result_key='upgraded')
    assert result['ok']
    assert set(result['data']['upgraded']) == {'sale', 'stock'}
    assert all(m.state == 'to upgrade' for m in mods)
    reg.new.assert_called_once()


def test_uninstall_writes_state_and_triggers_registry_update():
    mods = [FakeModule('sale', 'installed')]
    result, reg = _run_transition(mods, ['sale'], 'to remove',
                                  requires_installed=True, result_key='uninstalled')
    assert result['ok']
    assert mods[0].state == 'to remove'
    reg.new.assert_called_once_with('testdb', update_module=True)


# ── cmd_module_upgrade_all ────────────────────────────────────────────────────

def _run_upgrade_all(modules, db='testdb'):
    env = make_env(modules=modules)
    with patch('suctl_odoo._cursor', return_value=MagicMock()), \
         patch('suctl_odoo._env', return_value=env), \
         patch('odoo.modules.registry.Registry') as reg:
        result = suctl_odoo.cmd_module_upgrade_all({'db': db})
    return result, reg


def test_upgrade_all_sets_all_installed_to_upgrade():
    mods = [FakeModule('sale', 'installed'), FakeModule('stock', 'installed')]
    result, reg = _run_upgrade_all(mods)
    assert result['ok']
    assert result['data']['upgraded'] == 2
    assert set(result['data']['names']) == {'sale', 'stock'}
    assert all(m.state == 'to upgrade' for m in mods)
    reg.new.assert_called_once_with('testdb', update_module=True)


def test_upgrade_all_empty_database_returns_zero():
    result, reg = _run_upgrade_all([])
    assert result['ok']
    assert result['data']['upgraded'] == 0
    assert result['data']['names'] == []
    reg.new.assert_not_called()


def test_upgrade_all_missing_db():
    resp = suctl_odoo._dispatch({'cmd': 'module.upgrade_all', 'params': {}})
    assert not resp['ok']
    assert resp['error']['code'] == 'ERR_INVALID_REQUEST'


# ── cmd_module_* param validation — go through _dispatch so ValueError is mapped ──

def test_install_missing_db():
    resp = suctl_odoo._dispatch({'cmd': 'module.install', 'params': {'names': ['sale']}})
    assert not resp['ok']
    assert resp['error']['code'] == 'ERR_INVALID_REQUEST'


def test_install_missing_names():
    resp = suctl_odoo._dispatch({'cmd': 'module.install', 'params': {'db': 'testdb'}})
    assert not resp['ok']
    assert resp['error']['code'] == 'ERR_INVALID_REQUEST'


def test_install_empty_names_list():
    resp = suctl_odoo._dispatch({'cmd': 'module.install', 'params': {'db': 'testdb', 'names': []}})
    assert not resp['ok']
    assert resp['error']['code'] == 'ERR_INVALID_REQUEST'


# ── cmd_module_detect_changes ─────────────────────────────────────────────────

def _run_detect(modules):
    env = make_env(modules=modules)
    with patch('suctl_odoo._cursor', return_value=MagicMock()), \
         patch('suctl_odoo._env', return_value=env):
        return suctl_odoo.cmd_module_detect_changes({'db': 'testdb'})


def test_detect_no_changes():
    result = _run_detect([FakeModule('sale', 'installed', '16.0.1', '16.0.1')])
    assert result['ok']
    assert result['data']['changes'] == []


def test_detect_returns_changed_modules():
    mods = [
        FakeModule('sale',  'installed', '16.0.1', '16.0.2'),   # changed
        FakeModule('stock', 'installed', '16.0.1', '16.0.1'),   # unchanged
    ]
    result = _run_detect(mods)
    assert result['ok']
    assert len(result['data']['changes']) == 1
    assert result['data']['changes'][0]['name'] == 'sale'


# ── cmd_module_force_update_data ──────────────────────────────────────────────

def _run_force(modules, data_records, name):
    env = make_env(modules=modules, data_records=data_records)
    with patch('suctl_odoo._cursor', return_value=MagicMock()), \
         patch('suctl_odoo._env', return_value=env), \
         patch('odoo.modules.registry.Registry') as reg:
        result = suctl_odoo.cmd_module_force_update_data({'db': 'testdb', 'name': name})
    return result, reg


def test_force_update_module_not_installed():
    result, _ = _run_force([], [], 'sale')
    assert not result['ok']
    assert result['error']['code'] == 'ERR_MODULE_NOT_FOUND'


def test_force_update_unlocks_records_and_triggers_upgrade():
    mods = [FakeModule('sale', 'installed')]
    data = [FakeDataRecord('sale'), FakeDataRecord('sale')]
    result, reg = _run_force(mods, data, 'sale')
    assert result['ok']
    assert result['data']['unlocked_records'] == 2
    assert all(r.noupdate is False for r in data)
    assert mods[0].state == 'to upgrade'
    reg.new.assert_called_once_with('testdb', update_module=True)
