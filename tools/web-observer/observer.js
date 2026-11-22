/* First-party diagnostics only: no cookies, form values or response bodies leave the browser. */
(() => {
  'use strict';
  if (window.__nicoleObserver) return;
  window.__nicoleObserver = true;
  const endpoint = '/_diagnostics/web-events';
  const nativeFetch = window.fetch.bind(window);
  const version = 'web-observer-20260915-1';
  const randomID = () => Array.from(crypto.getRandomValues(new Uint8Array(12)), b => b.toString(16).padStart(2, '0')).join('');
  const session = randomID(); // Per page load; no tracking cookie or persistent storage.
  const known = new Set('login sign-in sign-up register forgot-password reset otp oauth pricing wallet console topup dashboard keys profile subscriptions models channels users usage-logs task-logs system-settings billing model-pricing about rankings playground api user self groups status pricing setup option top-up amount recharge history payment pay stripe checkout subscription notice model list search enabled'.split(' '));
  function pathOf(value) {
    try {
      const u = new URL(value, location.href);
      if (u.origin !== location.origin) return '/external';
      return '/' + u.pathname.split('/').filter(Boolean).slice(0, 6).map(p => known.has(p) ? p : ':other').join('/');
    } catch (_) { return '/unknown'; }
  }
  function details(error) {
    const message = typeof error === 'string' ? error : String(error && error.message || '');
    let signature = 'unclassified_error';
    const rules = [
      [/Minified React error #(\d+)/i, m => 'react_error_' + m[1]],
      [/Cannot read properties of (undefined|null).*reading ['"]([a-zA-Z_$][\w$]{0,30})['"]/, m => 'read_' + m[1] + '_' + m[2]],
      [/([a-zA-Z_$][\w$.]{0,30}) is not a function/, m => 'not_function_' + m[1]],
      [/([a-zA-Z_$][\w$]{0,30}) is not defined/, m => 'not_defined_' + m[1]],
      [/Loading chunk .* failed|ChunkLoadError|Failed to fetch dynamically imported module/i, () => 'chunk_load_failed'],
      [/ResizeObserver loop/i, () => 'resize_observer_loop'],
      [/Network Error|Failed to fetch|Load failed/i, () => 'network_error'],
      [/Maximum update depth|Too many re-renders/i, () => 'react_render_loop'],
      [/Rendered (more|fewer) hooks/i, () => 'react_hook_order'],
      [/Objects are not valid as a React child/i, () => 'react_invalid_child'],
    ];
    for (const [pattern, result] of rules) { const match = message.match(pattern); if (match) { signature = result(match); break; } }
    let hash = 2166136261;
    for (let i = 0; i < Math.min(message.length, 4000); i++) hash = Math.imul(hash ^ message.charCodeAt(i), 16777619);
    const stack = String(error && error.stack || '').match(/\/static\/js\/[a-zA-Z0-9_.-]+\.js:\d+:\d+/g) || [];
    return { signature, fingerprint: (hash >>> 0).toString(16), frames: stack.slice(0, 8) };
  }
  const crumbs = [];
  function breadcrumb(action, path) {
    crumbs.push({ action, path: path || pathOf(location.href) });
    if (crumbs.length > 8) crumbs.shift();
  }
  breadcrumb('navigate');
  const recent = new Map();
  let windowStart = Date.now(), count = 0, lastError = null, lastErrorAt = 0;
  function report(kind, fields = {}) {
    try {
      const now = Date.now();
      if (now - windowStart > 60000) { windowStart = now; count = 0; recent.clear(); }
      const page = pathOf(location.href);
      const key = [kind, page, fields.signature, fields.api, fields.status].join('|');
      if (count >= 20 || recent.has(key)) return null;
      recent.set(key, now); count++;
      const eventID = randomID();
      const event = { version, event_id: eventID, session, kind, page, breadcrumbs: crumbs.slice(), ...fields };
      nativeFetch(endpoint, { method: 'POST', headers: { 'Content-Type': 'application/json' }, credentials: 'omit', body: JSON.stringify(event), keepalive: true }).catch(() => {});
      return eventID;
    } catch (_) { return null; }
  }
  function recordError(kind, value) {
    try { lastError = details(value); lastErrorAt = Date.now(); report(kind, lastError); } catch (_) { /* Diagnostics never break the app. */ }
  }
  window.addEventListener('error', event => {
    if (event.error || event.message) recordError('javascript_error', event.error || event.message);
    else if (event.target && /^(SCRIPT|LINK)$/.test(event.target.tagName)) report('resource_error', { signature: 'asset_load_failed' });
  }, true);
  window.addEventListener('unhandledrejection', event => recordError('promise_error', event.reason));
  const nativeConsoleError = console.error;
  console.error = function (...args) {
    try {
      const error = args.find(a => a instanceof Error);
      if (error) recordError('caught_error', error);
    } catch (_) { /* Keep original console behavior. */ }
    return Reflect.apply(nativeConsoleError, console, args);
  };
  function apiResult(url, method, status, requestID, businessFailure, started) {
    try {
      const u = new URL(url, location.href);
      if (u.origin !== location.origin || !u.pathname.startsWith('/api/')) return;
      if (!(status === 0 || status >= 400 || businessFailure)) return;
      report('api_error', { api: pathOf(url), method: /^(GET|POST|PUT|DELETE|PATCH)$/.test(method) ? method : 'OTHER', status,
        request_id: /^\d{14}[a-zA-Z0-9]{10,80}$/.test(requestID || '') ? requestID : '',
        signature: businessFailure ? 'business_rejected' : status === 0 ? 'network_error' : 'http_error',
        duration_ms: Math.min(600000, Math.max(0, Date.now() - started)) });
    } catch (_) { /* Do not affect the original request. */ }
  }
  window.fetch = async function (input, init) {
    const start = Date.now();
    const url = typeof input === 'string' || input instanceof URL ? String(input) : input.url;
    const method = String(init && init.method || input.method || 'GET').toUpperCase();
    try {
      const response = await nativeFetch(input, init);
      apiResult(url, method, response.status, response.headers.get('X-Oneapi-Request-Id'), false, start);
      return response;
    } catch (error) { apiResult(url, method, 0, '', false, start); throw error; }
  };
  const open = XMLHttpRequest.prototype.open;
  const send = XMLHttpRequest.prototype.send;
  const requests = new WeakMap();
  XMLHttpRequest.prototype.open = function (method, url, ...rest) {
    requests.set(this, { method: String(method).toUpperCase(), url: String(url) });
    return Reflect.apply(open, this, [method, url, ...rest]);
  };
  XMLHttpRequest.prototype.send = function (...args) {
    const info = requests.get(this), start = Date.now();
    if (info) this.addEventListener('loadend', () => {
      try {
        let rejected = false;
        if (this.responseType === 'json') rejected = this.response && this.response.success === false;
        else if ((!this.responseType || this.responseType === 'text') && this.responseText.length < 65536 && this.getResponseHeader('Content-Type')?.includes('application/json')) {
          try { rejected = JSON.parse(this.responseText).success === false; } catch (_) { /* Not JSON. */ }
        }
        apiResult(info.url, info.method, this.status, this.getResponseHeader('X-Oneapi-Request-Id'), rejected, start);
      } catch (_) { /* Never send response contents. */ }
    }, { once: true });
    return Reflect.apply(send, this, args);
  };
  for (const name of ['pushState', 'replaceState']) {
    const original = history[name];
    history[name] = function (...args) {
      const result = Reflect.apply(original, this, args);
      breadcrumb('navigate'); return result;
    };
  }
  window.addEventListener('popstate', () => breadcrumb('navigate'));
  document.addEventListener('click', event => {
    const link = event.target instanceof Element ? event.target.closest('a[href]') : null;
    if (link) breadcrumb('link', pathOf(link.href));
  }, true);
  document.addEventListener('submit', () => breadcrumb('submit'), true);
  let shownHeading = null, pending = false;
  function checkErrorPage() {
    pending = false;
    const heading = Array.from(document.querySelectorAll('h1')).find(h => /^(500|429|403)$/.test(h.textContent.trim()));
    if (!heading) { shownHeading = null; document.getElementById('nicole-diagnostic-id')?.remove(); return; }
    if (shownHeading === heading || !document.querySelector('a[href="https://github.com/QuantumNous/new-api/issues"]')) return;
    shownHeading = heading;
    const context = lastError && Date.now() - lastErrorAt < 10000 ? lastError : { signature: 'fallback_without_observed_exception' };
    const eventID = report('error_page', { ...context, status: Number(heading.textContent.trim()) });
    if (eventID) {
      const badge = document.createElement('div'); badge.id = 'nicole-diagnostic-id'; badge.setAttribute('role', 'status');
      badge.textContent = (/^zh/i.test(navigator.language) ? '诊断编号：' : 'Diagnostic ID: ') + eventID;
      badge.style.cssText = 'font:12px monospace;color:#777;text-align:center;overflow-wrap:anywhere;padding:8px';
      heading.parentElement.appendChild(badge);
    }
  }
  function observe() {
    new MutationObserver(() => { if (!pending) { pending = true; setTimeout(checkErrorPage, 500); } }).observe(document.body, { childList: true, subtree: true });
    checkErrorPage();
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', observe, { once: true }); else observe();
})();
