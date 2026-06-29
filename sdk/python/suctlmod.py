# SPDX-License-Identifier: Apache-2.0
"""
suctlmod — Python SDK for suctl modules (protocol v1).

This is the reusable half of every Python module: the inherited broker wire,
the request/response envelopes, the dispatch loop (handshake / health / invoke,
sync and async), and the face-agnostic surface DTO builders. A
module supplies only its manifest and its capability handlers; everything
below the protocol line lives here, once.

Identity is the inherited SUCTL_BROKER_FD (fd 3) — one pre-connected, full-duplex
socketpair end. This process does NOT listen, accept, bind, or unlink any socket:
it runs a single read loop over that one wire, demultiplexing inbound core
requests (cmd set) from responses to its own outbound calls (status set).

Usage:

    import suctlmod
    mod = suctlmod.Module("suctl-mod-x", manifest_path)

    @mod.capability("x.thing.survey")
    def survey(args):
        return suctlmod.surface.survey([...])

    mod.run()

A handler returns any JSON-serializable result; raise suctlmod.CapError(code,
message) to signal a typed failure. Async-declared capabilities (manifest
"async": true) return accepted immediately and report their terminal result via
job_update — the SDK runs the handler on a worker and emits running/done/failed.
"""

import datetime
import json
import logging
import os
import signal
import socket
import sys
import threading
import time
import uuid

PROTOCOL_VER = '1'


# ── Errors ─────────────────────────────────────────────────────────────────────

class CapError(Exception):
    """A typed capability failure. code is a protocol error code (e.g.
    INVALID_PARAMS, CALLABLE_FAILED); message is operator-facing."""

    def __init__(self, code, message):
        super().__init__(message)
        self.code = code
        self.message = message


# ── Logging ─────────────────────────────────────────────────────────────────────

class _PlainFormatter(logging.Formatter):
    def __init__(self, name):
        super().__init__()
        self._name = name

    def format(self, record):
        record.message = record.getMessage()
        ts = datetime.datetime.fromtimestamp(record.created, tz=datetime.timezone.utc)
        line = (
            f"{ts.strftime('%Y-%m-%d %H:%M:%S UTC')}  "
            f"{record.levelname:<5}  {self._name}  {record.message}"
        )
        if record.exc_info:
            line += '\n' + self.formatException(record.exc_info)
        return line


def _setup_logging(name, log_file):
    root = logging.getLogger()
    root.setLevel(logging.INFO)
    fmt = _PlainFormatter(name)
    if log_file:
        try:
            fh = logging.FileHandler(log_file)
            fh.setFormatter(fmt)
            root.addHandler(fh)
        except OSError:
            pass
    sh = logging.StreamHandler(sys.stderr)
    sh.setFormatter(fmt)
    root.addHandler(sh)


def _load_manifest(path, name):
    try:
        with open(path) as f:
            return json.load(f)
    except Exception:
        return {"module": name, "version": "0.0.0", "protocol": PROTOCOL_VER,
                "platform": ["linux"], "entrypoint": name,
                "description": name, "capabilities": []}


def _now():
    """RFC3339 timestamp with explicit offset, e.g. 2026-01-02T15:04:05+00:00."""
    return datetime.datetime.now(datetime.timezone.utc).isoformat(timespec='seconds')


def _new_id():
    return str(uuid.uuid4())


# ── Surface DTO builders ─────────────────────────────────────────────────────────
# Mirror sdk/surface (Go): face-agnostic survey/focus shapes carrying only
# semantic tokens, column ids, and pre-formatted values — zero face styling.

class surface:
    @staticmethod
    def col(value, color=None):
        c = {'value': value}
        if color is not None:
            c['color'] = color
        return c

    @staticmethod
    def action(capability, label, destructive=False):
        a = {'capability': capability, 'label': label}
        if destructive:
            a['destructive'] = True
        return a

    @staticmethod
    def subject(id, name, columns, inline_actions=None, facets=None):
        s = {'id': id, 'name': name, 'columns': columns}
        if inline_actions:
            s['inline_actions'] = inline_actions
        if facets:
            s['facets'] = facets
        return s

    @staticmethod
    def survey(subjects, status_summary=None, actions=None):
        """Build a survey response dict.

        actions is the enabled subset of survey-level bulk actions declared in
        surface.json survey.actions. Pass only those that are currently available
        for the given scope; core renders whatever is returned. Omit (or pass None)
        when no bulk actions are enabled for this load.
        """
        r = {'total': len(subjects), 'subjects': subjects}
        if status_summary:
            r['status_summary'] = status_summary
        if actions:
            r['actions'] = actions
        return r

    @staticmethod
    def rows(rows):
        """Build a whole-column cell-fill response dict for every row at once.

        Returned by a column's `from` capability (data lineage): rows maps row id
        → {column id → cell (use surface.col)}. One call fills the whole column
        across all rows; the face applies each row's columns to the matching
        survey row, keyed by id. Columns sharing the same `from` value are filled
        together; ids (or rows) the caller omits simply stay absent. Each row's
        inner map mirrors Subject.columns."""
        return {'rows': {rid: {'columns': cols} for rid, cols in rows.items()}}

    @staticmethod
    def field(label, value, color=None, full_width=False):
        f = {'label': label, 'value': value}
        if color is not None:
            f['color'] = color
        if full_width:
            f['full_width'] = True
        return f

    @staticmethod
    def section(title, fields):
        return {'title': title, 'fields': fields}

    @staticmethod
    def focus(id, name, sections, actions=None):
        r = {'id': id, 'name': name, 'sections': sections}
        if actions:
            r['actions'] = actions
        return r




# ── Module ──────────────────────────────────────────────────────────────────────

class Module:
    """One suctl module: manifest + capability handlers, driven over the
    inherited broker wire. Register handlers with @capability(name) (or
    default_handler for a catch-all that also receives the cap name), then
    call run()."""

    def __init__(self, name, manifest_path, log_file=None):
        self.name = name
        self._manifest_path = manifest_path
        self._log_file = log_file if log_file is not None else f'/var/log/suctl/{name}.log'
        self._manifest = _load_manifest(manifest_path, name)
        self._known = {c.get('name', '') for c in self._manifest.get('capabilities', [])}
        self._async = {c.get('name', '') for c in self._manifest.get('capabilities', []) if c.get('async')}
        self._handlers = {}
        self._default = None
        self._wire = None
        self._wlock = threading.Lock()
        self._start = time.monotonic()
        self.log = logging.getLogger(name)

    # registration -------------------------------------------------------------

    def capability(self, name):
        """Decorator: register a sync/async handler fn(args)->result for one cap."""
        def deco(fn):
            self._handlers[name] = fn
            return fn
        return deco

    def default_handler(self, fn):
        """Register a catch-all fn(cap_name, args)->result for caps without a
        specific handler (used by forwarding modules like mod-odoo)."""
        self._default = fn
        return fn

    def survey_facets(self, cap_name):
        """Return the ordered facet definitions for a survey capability, by
        searching the module's surface.json tree — surfaces and arbitrarily-
        nested drills — for the survey whose entry matches cap_name.
        Returns a list of dicts with at least 'label' and 'value' keys, or []
        if cap_name is not found or surface.json is absent/unreadable.
        Module handlers use the returned values to tag rows (D68)."""
        surface_path = os.path.join(os.path.dirname(self._manifest_path), 'surface.json')
        try:
            with open(surface_path) as f:
                data = json.load(f)
        except (OSError, ValueError):
            return []

        def _search(surfaces):
            for s in surfaces:
                survey = s.get('survey') or {}
                if survey.get('entry') == cap_name:
                    return list(survey.get('facets') or [])
                # Recurse into drills (arbitrarily nested — drills may contain drills).
                for drill in s.get('drills') or []:
                    result = _search([drill])
                    if result is not None:
                        return result
            return None

        result = _search(data.get('surfaces') or [])
        return result if result is not None else []

    def survey_actions(self, cap_name):
        """Return the declared survey-level action configs for a survey capability,
        by searching surface.json for the survey whose entry matches cap_name.
        Returns a list of dicts with 'capability', 'label', and optional
        'destructive' keys — matching the surface.json survey.actions schema —
        or [] if cap_name is not found or surface.json is absent/unreadable.

        Handlers use the returned list as the full declared set and return the
        enabled subset in the survey response (analogous to how survey_facets
        is used to tag rows — module decides which actions apply for the scope).
        """
        surface_path = os.path.join(os.path.dirname(self._manifest_path), 'surface.json')
        try:
            with open(surface_path) as f:
                data = json.load(f)
        except (OSError, ValueError):
            return []

        def _search(surfaces):
            for s in surfaces:
                survey = s.get('survey') or {}
                if survey.get('entry') == cap_name:
                    return list(survey.get('actions') or [])
                for drill in s.get('drills') or []:
                    result = _search([drill])
                    if result is not None:
                        return result
            return None

        result = _search(data.get('surfaces') or [])
        return result if result is not None else []

    # wire ---------------------------------------------------------------------

    def _send(self, obj):
        data = (json.dumps(obj, separators=(',', ':')) + '\n').encode()
        with self._wlock:
            try:
                self._wire.sendall(data)
            except OSError:
                pass

    def _ok(self, req, result):
        r = {'v': PROTOCOL_VER, 'id': req.get('id', ''), 'ts_sent': _now(),
             'status': 'ok', 'result': result}
        if req.get('job_token'):
            r['job_token'] = req['job_token']
        return r

    def _err(self, req, code, message):
        r = {'v': PROTOCOL_VER, 'id': req.get('id', ''), 'ts_sent': _now(),
             'status': 'error', 'error': {'code': code, 'message': message}}
        if req.get('job_token'):
            r['job_token'] = req['job_token']
        return r

    def _job_update(self, job_token, params):
        """Fire-and-forget a job_update for an async capability."""
        self._send({'v': PROTOCOL_VER, 'id': _new_id(), 'ts_sent': _now(),
                    'cmd': 'job_update', 'job_token': job_token, 'params': params})

    # dispatch -----------------------------------------------------------------

    def _invoke_cap(self, cap, args):
        fn = self._handlers.get(cap)
        if fn is not None:
            return fn(args)
        if self._default is not None:
            return self._default(cap, args)
        raise CapError('UNKNOWN_CALLABLE', 'no handler registered for ' + cap)

    def _run_async(self, cap, args, job_token):
        if job_token:
            self._job_update(job_token, {'state': 'running', 'message': cap + ' running'})
        try:
            out = self._invoke_cap(cap, args)
        except CapError as ce:
            if job_token:
                self._job_update(job_token, {'state': 'failed',
                                             'error': {'code': ce.code, 'message': ce.message}})
            return
        except Exception as exc:
            self.log.exception('async cap %s failed', cap)
            if job_token:
                self._job_update(job_token, {'state': 'failed',
                                             'error': {'code': 'CALLABLE_FAILED', 'message': str(exc)}})
            return
        if job_token:
            self._job_update(job_token, {'state': 'done', 'output': out})

    def _dispatch(self, req):
        cmd = req.get('cmd', '')

        if cmd == 'handshake':
            return self._ok(req, {'manifest': self._manifest})

        if cmd == 'health':
            return self._ok(req, {
                'status': 'healthy',
                'uptime_seconds': int(time.monotonic() - self._start),
            })

        if cmd == 'invoke':
            params = req.get('params') or {}
            cap = params.get('name', '')
            args = params.get('args') or {}
            if cap not in self._known:
                return self._err(req, 'UNKNOWN_CALLABLE', 'unknown callable: ' + cap)
            self.log.info('invoke: %s', cap)
            if cap in self._async:
                threading.Thread(target=self._run_async,
                                 args=(cap, args, req.get('job_token')), daemon=True).start()
                return self._ok(req, {'name': cap, 'accepted': True})
            try:
                out = self._invoke_cap(cap, args)
            except CapError as ce:
                return self._err(req, ce.code, ce.message)
            except Exception as exc:
                self.log.exception('cap %s failed', cap)
                return self._err(req, 'CALLABLE_FAILED', str(exc))
            return self._ok(req, {'name': cap, 'output': out})

        return self._err(req, 'UNKNOWN_COMMAND', 'unknown command: ' + cmd)

    def _serve(self, line):
        try:
            req = json.loads(line)
        except json.JSONDecodeError as exc:
            self._send({'v': PROTOCOL_VER, 'id': '', 'ts_sent': _now(), 'status': 'error',
                        'error': {'code': 'INVALID_PARAMS', 'message': f'JSON parse error: {exc}'}})
            return
        if not req.get('cmd'):
            return  # response to our own outbound (e.g. job_update ack) — ignore
        self._send(self._dispatch(req))

    # run ----------------------------------------------------------------------

    def run(self):
        _setup_logging(self.name, self._log_file)

        fd_str = os.environ.get('SUCTL_BROKER_FD')
        if not fd_str:
            self.log.error('SUCTL_BROKER_FD not set')
            sys.exit(1)
        try:
            fd = int(fd_str)
        except ValueError:
            self.log.error('invalid SUCTL_BROKER_FD: %r', fd_str)
            sys.exit(1)

        self._wire = socket.fromfd(fd, socket.AF_UNIX, socket.SOCK_STREAM)
        os.close(fd)
        self.log.info('broker wire on fd %d', fd)

        def _shutdown(signum, _frame):
            self.log.info('signal %d — shutting down', signum)
            try:
                self._wire.close()
            except OSError:
                pass
            sys.exit(0)

        signal.signal(signal.SIGTERM, _shutdown)
        signal.signal(signal.SIGINT, _shutdown)

        rfile = self._wire.makefile('rb')
        while True:
            try:
                line = rfile.readline()
            except OSError:
                break
            if not line:
                break
            line = line.strip()
            if not line:
                continue
            threading.Thread(target=self._serve, args=(line,), daemon=True).start()

        self.log.info('wire closed — exiting')
