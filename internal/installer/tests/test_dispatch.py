# SPDX-License-Identifier: Apache-2.0
"""Tests for _dispatch: routing, error-code mapping, exception handling."""

from unittest.mock import patch

import suctl_odoo


def test_missing_cmd_field():
    resp = suctl_odoo._dispatch({})
    assert resp['ok'] is False
    assert resp['error']['code'] == 'ERR_INVALID_REQUEST'
    assert 'missing cmd' in resp['error']['message']


def test_unknown_cmd():
    resp = suctl_odoo._dispatch({'cmd': 'no.such.command'})
    assert resp['ok'] is False
    assert resp['error']['code'] == 'ERR_INVALID_REQUEST'
    assert 'no.such.command' in resp['error']['message']


def test_value_error_maps_to_invalid_request():
    def _bad(params):
        raise ValueError('param x is required')

    with patch.dict(suctl_odoo._COMMANDS, {'test.bad': _bad}):
        resp = suctl_odoo._dispatch({'cmd': 'test.bad', 'params': {}})

    assert resp['ok'] is False
    assert resp['error']['code'] == 'ERR_INVALID_REQUEST'
    assert 'param x is required' in resp['error']['message']


def test_operational_error_maps_to_odoo_not_ready():
    """psycopg2.OperationalError (and any exception named *OperationalError*)
    surfaces as ERR_ODOO_NOT_READY so suctl can report it cleanly."""

    class FakeOperationalError(Exception):
        pass

    FakeOperationalError.__name__ = 'OperationalError'

    def _bad(params):
        raise FakeOperationalError('connection refused')

    with patch.dict(suctl_odoo._COMMANDS, {'test.db': _bad}):
        resp = suctl_odoo._dispatch({'cmd': 'test.db', 'params': {}})

    assert resp['ok'] is False
    assert resp['error']['code'] == 'ERR_ODOO_NOT_READY'


def test_interface_error_maps_to_odoo_not_ready():
    class FakeInterfaceError(Exception):
        pass

    FakeInterfaceError.__name__ = 'InterfaceError'

    def _bad(params):
        raise FakeInterfaceError('interface gone')

    with patch.dict(suctl_odoo._COMMANDS, {'test.iface': _bad}):
        resp = suctl_odoo._dispatch({'cmd': 'test.iface', 'params': {}})

    assert resp['error']['code'] == 'ERR_ODOO_NOT_READY'


def test_unexpected_exception_maps_to_internal():
    def _bad(params):
        raise RuntimeError('something exploded')

    with patch.dict(suctl_odoo._COMMANDS, {'test.crash': _bad}):
        resp = suctl_odoo._dispatch({'cmd': 'test.crash', 'params': {}})

    assert resp['ok'] is False
    assert resp['error']['code'] == 'ERR_INTERNAL'


def test_successful_handler_returns_ok():
    def _good(params):
        return suctl_odoo._ok({'pong': True})

    with patch.dict(suctl_odoo._COMMANDS, {'test.ok': _good}):
        resp = suctl_odoo._dispatch({'cmd': 'test.ok', 'params': {}})

    assert resp['ok'] is True
    assert resp['data']['pong'] is True


def test_params_default_to_empty_dict_when_absent():
    """A request with no `params` key must not crash the dispatcher."""
    received = {}

    def _capture(params):
        received['params'] = params
        return suctl_odoo._ok({})

    with patch.dict(suctl_odoo._COMMANDS, {'test.capture': _capture}):
        suctl_odoo._dispatch({'cmd': 'test.capture'})

    assert received['params'] == {}
