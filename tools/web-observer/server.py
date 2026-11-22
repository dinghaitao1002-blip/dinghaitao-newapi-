"""Isolated, bounded first-party browser diagnostics sink. No database access."""
import collections
import datetime
import http.server
import json
import logging
import logging.handlers
import os
import pathlib
import re
import time

ALLOWED_ORIGINS = {'https://nicolessss.com', 'https://cdn.nicolessss.com'}
KINDS = {'javascript_error', 'promise_error', 'caught_error', 'resource_error', 'api_error', 'error_page'}
WORDS = set('login sign-in sign-up register forgot-password reset otp oauth pricing wallet console topup dashboard keys profile subscriptions models channels users usage-logs task-logs system-settings billing model-pricing about rankings playground api user self groups status pricing setup option top-up amount recharge history payment pay stripe checkout subscription notice model list search enabled external unknown :other'.split())


def safe_path(value):
    if not isinstance(value, str) or not value.startswith('/'):
        return '/unknown'
    return '/' + '/'.join(p if p in WORDS else ':other' for p in value.split('?')[0].split('#')[0].split('/')[1:7] if p)


def sanitize(data):
    if not isinstance(data, dict) or data.get('kind') not in KINDS:
        raise ValueError('invalid event')
    result = {'received_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'version': 'web-observer-20260915-1', 'kind': data['kind'], 'page': safe_path(data.get('page'))}
    for key in ('event_id', 'session'):
        value = data.get(key, '')
        if not isinstance(value, str) or not re.fullmatch(r'[a-f0-9]{24}', value):
            raise ValueError('invalid id')
        result[key] = value
    signature = data.get('signature', '')
    if not isinstance(signature, str) or not re.fullmatch(r'(react_error_\d{1,4}|read_(undefined|null)_[a-zA-Z_$][\w$]{0,30}|not_function_[\w$.]{1,31}|not_defined_[\w$]{1,31}|chunk_load_failed|resize_observer_loop|network_error|react_render_loop|react_hook_order|react_invalid_child|unclassified_error|asset_load_failed|business_rejected|http_error|fallback_without_observed_exception)', signature):
        signature = 'unclassified_error'
    result['signature'] = signature
    fingerprint = data.get('fingerprint', '')
    if isinstance(fingerprint, str) and re.fullmatch(r'[a-f0-9]{1,8}', fingerprint):
        result['fingerprint'] = fingerprint
    frames = data.get('frames', [])
    result['frames'] = [f for f in frames[:8] if isinstance(f, str) and re.fullmatch(r'/static/js/[\w.-]{1,100}\.js:\d{1,8}:\d{1,8}', f)] if isinstance(frames, list) else []
    crumbs = data.get('breadcrumbs', [])
    result['breadcrumbs'] = [{'action': b['action'], 'path': safe_path(b.get('path'))} for b in crumbs[-8:] if isinstance(b, dict) and b.get('action') in ('navigate', 'link', 'submit')] if isinstance(crumbs, list) else []
    status = data.get('status')
    if type(status) is int and 0 <= status <= 599:
        result['status'] = status
    if data['kind'] == 'api_error':
        result['api'] = safe_path(data.get('api'))
        result['method'] = data.get('method') if data.get('method') in ('GET', 'POST', 'PUT', 'DELETE', 'PATCH') else 'OTHER'
        rid = data.get('request_id', '')
        result['request_id'] = rid if isinstance(rid, str) and re.fullmatch(r'\d{14}[a-zA-Z0-9]{10,80}', rid) else ''
        duration = data.get('duration_ms', 0)
        result['duration_ms'] = min(600000, max(0, duration)) if type(duration) is int else 0
    return result


class Budget:
    def __init__(self):
        self.minute = -1
        self.seen = set()
        self.sessions = collections.Counter()

    def accept(self, event):
        minute = int(time.monotonic() // 60)
        if self.minute != minute:
            self.minute = minute
            self.seen.clear()
            self.sessions.clear()
        sid = event['session']
        if event['event_id'] in self.seen:
            return False
        if len(self.seen) >= 300 or self.sessions[sid] >= 20:
            return False
        self.seen.add(event['event_id'])
        self.sessions[sid] += 1
        return True


class Handler(http.server.BaseHTTPRequestHandler):
    budget = Budget()
    logger = logging.getLogger('web-observer')

    def setup(self):
        super().setup()
        self.connection.settimeout(3)

    def log_message(self, *_):
        pass  # No IPs, query strings or request headers in logs.

    def respond(self, code):
        self.send_response(code)
        self.send_header('Content-Length', '0')
        self.send_header('Cache-Control', 'no-store')
        self.end_headers()

    def do_GET(self):
        self.respond(204 if self.path == '/healthz' else 404)

    def do_POST(self):
        if self.path != '/_diagnostics/web-events':
            return self.respond(404)
        if self.headers.get('Origin') not in ALLOWED_ORIGINS:
            return self.respond(403)
        if self.headers.get_content_type() != 'application/json':
            return self.respond(415)
        try:
            size = int(self.headers.get('Content-Length', '0'))
            if not 0 < size <= 8192:
                return self.respond(413)
            event = sanitize(json.loads(self.rfile.read(size)))
        except (ValueError, TypeError, OSError):
            return self.respond(400)
        if not self.budget.accept(event):
            return self.respond(429)
        self.logger.info(json.dumps(event, ensure_ascii=True, separators=(',', ':')))
        self.respond(204)


def main():
    folder = pathlib.Path(os.environ.get('LOGS_DIRECTORY', '/var/log/newapi-web-observer'))
    folder.mkdir(parents=True, exist_ok=True)
    # At most ~60 MiB. No request body logging. No access to NewAPI or its credentials.
    handler = logging.handlers.RotatingFileHandler(folder / 'events.jsonl', maxBytes=10*1024*1024, backupCount=5, encoding='utf-8')
    Handler.logger.addHandler(handler)
    Handler.logger.setLevel(logging.INFO)
    http.server.HTTPServer(('127.0.0.1', int(os.environ.get('PORT', '3017'))), Handler).serve_forever()


if __name__ == '__main__':
    main()
