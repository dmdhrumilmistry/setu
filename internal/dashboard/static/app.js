// setu dashboard. Plain ES module, no dependencies. Everything shown is put
// in the page with textContent / DOM APIs, never as HTML, and the CSP
// forbids inline script and style anyway.

const POLL_MS = 3000;
const ANALYTICS_MS = 30000;
const PAGES = ['overview', 'sessions', 'profiles', 'access', 'activity'];

const app = {
  csrf: null,
  page: 'overview',
  range: '7d',
  st: null,
  an: null,
  anRange: null,
  anAt: 0,
  anError: '',
  failures: 0,
  timer: 0,
  hideIdle: true,
  activityKind: '',
  activityText: '',
  signedOut: false,
};

// ---------- DOM helpers ----------

function h(tag, props, ...kids) {
  const el = document.createElement(tag);
  setProps(el, props);
  append(el, kids);
  return el;
}

const SVGNS = 'http://www.w3.org/2000/svg';
function s(tag, attrs, ...kids) {
  const el = document.createElementNS(SVGNS, tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v !== undefined && v !== null && v !== false) el.setAttribute(k, String(v));
  }
  append(el, kids);
  return el;
}

function setProps(el, props) {
  if (!props) return;
  for (const [k, v] of Object.entries(props)) {
    if (v === undefined || v === null || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k === 'text') el.textContent = v;
    else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k === 'checked' || k === 'disabled' || k === 'value') el[k] = v;
    else el.setAttribute(k, v === true ? '' : String(v));
  }
}

function append(el, kids) {
  for (const k of kids.flat(Infinity)) {
    if (k === null || k === undefined || k === false || k === '') continue;
    el.append(k instanceof Node ? k : document.createTextNode(String(k)));
  }
}

function icon(path) {
  const el = s('svg', { viewBox: '0 0 24 24', class: 'ico', 'aria-hidden': 'true' });
  for (const d of [].concat(path)) el.append(s('path', { d }));
  return el;
}
const I = {
  warn: ['M12 3l10 18H2z', 'M12 10v5', 'M12 18v.5'],
  ok: 'M5 12l5 5 9-10',
  lock: ['M6 10h12v11H6z', 'M8 10V7a4 4 0 0 1 8 0v3'],
  shield: 'M12 3l8 4v5c0 4.5-3.4 8.3-8 9-4.6-.7-8-4.5-8-9V7z',
};

// ---------- formatting ----------

const nf = new Intl.NumberFormat();
const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' });
const timeFmt = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit' });
const dayFmt = new Intl.DateTimeFormat(undefined, { weekday: 'short' });
const dateFmt = new Intl.DateTimeFormat(undefined, { day: 'numeric', month: 'short' });
const fullFmt = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' });

function t(v) { return v ? new Date(v) : null; }
function isSet(v) { return !!v && !String(v).startsWith('0001-'); }

function dur(secs) {
  secs = Math.max(0, Math.round(secs || 0));
  if (secs < 60) return `${secs} s`;
  const m = Math.floor(secs / 60);
  if (m < 60) return `${m} m`;
  const hrs = Math.floor(m / 60);
  if (hrs < 48) return `${hrs} h${m % 60 ? ` ${m % 60} m` : ''}`;
  const d = Math.floor(hrs / 24);
  return `${d} d${hrs % 24 ? ` ${hrs % 24} h` : ''}`;
}

function ago(v) {
  if (!isSet(v)) return 'never';
  const secs = (Date.now() - new Date(v).getTime()) / 1000;
  const abs = Math.abs(secs);
  const units = [[60, 'second', 1], [3600, 'minute', 60], [86400, 'hour', 3600], [86400 * 30, 'day', 86400], [Infinity, 'month', 86400 * 30]];
  for (const [lim, unit, div] of units) {
    if (abs < lim) return rtf.format(-Math.round(secs / div), unit);
  }
  return '';
}

function until(v) {
  const secs = (new Date(v).getTime() - Date.now()) / 1000;
  if (secs <= 0) return 'expired';
  return `in ${dur(secs)}`;
}

function shortTime(v) {
  const d = new Date(v);
  const now = new Date();
  if (d.toDateString() === now.toDateString()) return timeFmt.format(d);
  if (now - d < 6 * 86400e3) return dayFmt.format(d);
  return dateFmt.format(d);
}

function who(name) {
  switch (name) {
    case 'shared-control-link': return 'the shared control link';
    case 'shared-view-link': return 'the shared view link';
    case 'host-terminal': return 'the host terminal';
    case '': case undefined: return 'someone';
  }
  return name;
}
function cap(str) { return str ? str[0].toUpperCase() + str.slice(1) : str; }
// sentence turns an error from the server (often lower case, as Go errors are) into one.
function sentence(str) { str = cap(String(str).trim()); return /[.!?]$/.test(str) ? str : `${str}.`; }

function sourceName(src) {
  if (!src) return 'a share';
  const [kind, name] = src.split(':');
  if (kind === 'person') return 'every session';
  if (name === '*') return 'every service profile';
  return kind === 'share' ? `background share ${name}` : name;
}

function authLabel(a) {
  return { device: 'paired devices only', password: 'password, asked once per device', none: 'the link alone' }[a] || a;
}

function network(c) {
  const kind = { host: 'direct, local network', srflx: 'direct', prflx: 'direct', relay: 'relayed through TURN' }[c.candidate_type] || '';
  return [kind, c.transport].filter(Boolean).join(', ');
}

const EVENT_COLOR = {
  connect: 'ok', reconnect: 'control', disconnect: '', auth_failure: 'err', lockout: 'err',
  rejected: 'warn', paired: 'control', invite_disabled: 'warn', dashboard: 'warn',
};

function describe(e) {
  const where = sourceName(e.src);
  const n = who(e.name);
  const dev = e.device ? ` (${e.device})` : '';
  switch (e.kind) {
    case 'connect': return `${cap(n)} connected to ${where} ${e.role === 'view' ? 'to watch' : 'with control'}${dev}`;
    case 'reconnect': return `${cap(n)} reconnected to ${where}${dev}`;
    case 'disconnect': {
      const normal = /^(connection (closed|failed|disconnected)|channel closed|client unresponsive|replaced by its reconnection|session ended)$/.test(e.reason || '');
      return `${cap(n)} left ${where} after ${dur(e.secs)}${e.reason && !normal ? `: ${e.reason}` : ''}`;
    }
    case 'auth_failure': return `Failed login on ${where}${e.addr ? ` from ${e.addr}` : ''}${e.name ? ` using ${who(e.name)}` : ''}`;
    case 'lockout': return `${cap(where)} refused new joins: ${e.reason || 'too many failed logins'}`;
    case 'rejected': return `Join refused on ${where}${e.name ? ` (${who(e.name)})` : ''}: ${e.reason || 'refused'}`;
    case 'paired': return `New device paired on ${where} for ${n}${dev}`;
    case 'invite_disabled': return `${cap(where)} stopped admitting new clients: ${e.reason}`;
    case 'dashboard': return `Dashboard: ${e.reason} on ${where}`;
  }
  return `${e.kind} on ${where}`;
}

// ---------- API ----------

class AuthError extends Error {}

async function api(method, path, body) {
  const opts = { method, credentials: 'same-origin', headers: {}, cache: 'no-store' };
  if (method !== 'GET') {
    opts.headers['Content-Type'] = 'application/json';
    if (app.csrf) opts.headers['X-Setu-CSRF'] = app.csrf;
    opts.body = JSON.stringify(body || {});
  }
  const res = await fetch(path, opts);
  let data = {};
  try { data = await res.json(); } catch { /* empty */ }
  if (res.status === 401) throw new AuthError(sentence(data.error || 'You are not signed in.'));
  if (!res.ok) throw new Error(sentence(data.error || `The request failed (${res.status}).`));
  return data;
}

// ---------- boot ----------

async function boot() {
  initTheme();
  document.getElementById('signout').addEventListener('click', signOut);
  window.addEventListener('hashchange', route);
  document.addEventListener('visibilitychange', () => { if (!document.hidden) poll(true); });

  const m = location.hash.match(/^#t=([A-Za-z0-9_-]{20,128})$/);
  try {
    if (m) {
      // Drop the one-time token from the address bar and history first.
      history.replaceState(null, '', '/#/overview');
      const r = await api('POST', '/api/session', { token: m[1] });
      app.csrf = r.csrf;
    } else {
      const r = await api('GET', '/api/session');
      app.csrf = r.csrf;
    }
  } catch (err) {
    gate(err.message);
    return;
  }
  route();
  poll(true);
}

function route() {
  const p = (location.hash.match(/^#\/(\w+)/) || [])[1];
  app.page = PAGES.includes(p) ? p : 'overview';
  for (const a of document.querySelectorAll('.nav a')) {
    if (a.dataset.page === app.page) a.setAttribute('aria-current', 'page');
    else a.removeAttribute('aria-current');
  }
  document.title = `${{ overview: 'Overview', sessions: 'Live sessions', profiles: 'Profiles and devices', access: 'Block and allow lists', activity: 'Activity' }[app.page]} · setu dashboard`;
  render(true);
  if (needsAnalytics() && app.anRange !== app.range) loadAnalytics();
}

function needsAnalytics() { return app.page === 'overview' || app.page === 'activity'; }

async function poll(now) {
  clearTimeout(app.timer);
  if (app.signedOut) return;
  if (!now && document.hidden) {
    app.timer = setTimeout(poll, POLL_MS);
    return;
  }
  try {
    app.st = await api('GET', '/api/state');
    app.failures = 0;
    banner('');
    let fresh = false;
    if (needsAnalytics() && (app.anRange !== app.range || Date.now() - app.anAt > ANALYTICS_MS)) fresh = await loadAnalytics(true);
    // The activity page shows only history: redrawing it on every poll
    // would reset its search box while someone types.
    if (app.page !== 'activity' || fresh) render();
    else renderNav();
  } catch (err) {
    if (err instanceof AuthError) { gate(err.message); return; }
    app.failures++;
    banner('Lost contact with setu dashboard. Is it still running in its terminal? Retrying…');
  }
  app.timer = setTimeout(poll, app.failures ? Math.min(15000, POLL_MS * 2 ** app.failures) : POLL_MS);
}

async function loadAnalytics(quiet) {
  const range = app.range;
  try {
    const an = await api('GET', `/api/analytics?range=${encodeURIComponent(range)}`);
    if (range !== app.range) return false;
    app.an = an; app.anRange = range; app.anAt = Date.now(); app.anError = '';
  } catch (err) {
    if (err instanceof AuthError) { gate(err.message); return false; }
    app.anError = err.message;
  }
  if (!quiet) render();
  return true;
}

function banner(msg) {
  const b = document.getElementById('banner');
  b.replaceChildren();
  if (!msg) { b.hidden = true; return; }
  b.append(icon(I.warn), h('span', { text: msg }));
  b.hidden = false;
}

// ---------- rendering ----------

function render(fresh) {
  if (app.signedOut) return;
  const view = document.getElementById('view');
  renderNav();

  // Keep focus on the same control across a refresh.
  const focusKey = document.activeElement?.dataset?.key;
  const page = { overview, sessions, profiles, access: accessPage, activity }[app.page];
  view.replaceChildren(...(app.st ? page() : [h('div', { class: 'skeleton', 'aria-label': 'Loading' })]));
  if (focusKey) view.querySelector(`[data-key="${CSS.escape(focusKey)}"]`)?.focus();
  if (fresh) document.getElementById('main').focus({ preventScroll: true });
}

function renderNav() {
  const live = (app.st?.shares || []).reduce((n, sh) => n + (sh.status?.clients?.length || 0), 0);
  const lc = document.getElementById('live-count');
  lc.hidden = !live;
  lc.textContent = live;
  lc.setAttribute('aria-label', `${live} connected`);
  if (app.st) document.getElementById('version').textContent = app.st.version;
}

function head(title, subtitle, ...right) {
  return h('header', { class: 'page-head' },
    h('div', null, h('h1', { text: title }), subtitle && h('p', { class: 'sub' }, subtitle)),
    ...right);
}

function serviceLine() {
  const sv = app.st.service;
  if (sv.running) return [h('span', { class: 'dot ok' }), `Service running (pid ${sv.pid})`, ` · updated ${timeFmt.format(new Date(app.st.now))}`];
  return [h('span', { class: 'dot' }), 'Service not running', ' · start it with ', h('span', { class: 'mono', text: 'setu service install' })];
}

function rangePicker() {
  return h('div', { class: 'seg', role: 'group', 'aria-label': 'Time range' },
    [['24h', '24 hours'], ['7d', '7 days'], ['30d', '30 days'], ['90d', '90 days']].map(([k, label]) =>
      h('button', {
        type: 'button', 'aria-pressed': String(app.range === k), 'data-key': `range-${k}`, text: label,
        onclick: () => { app.range = k; render(); loadAnalytics(); },
      })));
}

function attention() {
  const st = app.st;
  const items = [];
  const add = (level, text, action) => items.push(h('section', { class: `card attention ${level}`, 'aria-label': 'Needs attention' },
    icon(I.warn), h('p', null, text), action));
  if (st.policy.error) add('err', `The managed policy cannot be read, so the service serves nothing: ${st.policy.error}`);
  if (st.service.config_error) add('err', `The service configuration cannot be read: ${st.service.config_error}`);
  if (st.service.live_error) add('warn', `${st.service.live_error}. Restart the service (setu service install) to see its live sessions here.`);
  for (const p of st.profiles) {
    if (p.problem && p.enabled) add('err', [h('strong', { text: p.name }), ` is not served: ${p.problem}`], linkBtn('#/profiles', 'Review profile'));
    else if (p.enabled && p.expired) add('warn', [h('strong', { text: p.name }), ' has expired keys and is not served. Rotate them to get new links.'], linkBtn('#/profiles', 'Review profile'));
    else if (p.enabled && isSet(p.expires_at) && new Date(p.expires_at) - Date.now() < 7 * 86400e3) {
      add('warn', [h('strong', { text: p.name }), ` keys expire ${until(p.expires_at)}. Rotate them before then, or its links stop working.`], linkBtn('#/profiles', 'Review profile'));
    }
  }
  for (const v of st.access || []) {
    if (v.error) add('err', [h('strong', { text: v.label }), `: the block and allow lists cannot be read, so nobody can join. ${v.error}`], linkBtn('#/access', 'Review lists'));
  }
  for (const sh of st.shares) {
    if (sh.status?.paused) add('warn', [h('strong', { text: sourceName(sh.source) }), ' is not admitting new joins (paused from the dashboard).'], linkBtn('#/sessions', 'View session'));
    if (sh.status && isSet(sh.status.locked_until)) {
      add('err', [h('strong', { text: sourceName(sh.source) }), ` is refusing new joins until ${timeFmt.format(new Date(sh.status.locked_until))} after repeated failed logins.`], linkBtn('#/sessions', 'View session'));
    }
  }
  return items;
}

function linkBtn(href, text) { return h('a', { class: 'btn', href, text }); }

function overview() {
  const an = app.an;
  const out = [head('Overview', serviceLine(), rangePicker()), ...attention()];
  const shares = app.st.shares.filter((sh) => sh.status);
  const clients = shares.flatMap((sh) => sh.status.clients.map((c) => ({ c, sh })));
  const controlNow = clients.filter(({ c }) => c.role === 'control').length;

  if (!an) {
    out.push(app.anError ? h('p', { class: 'card pad err-text', text: `Could not load analytics: ${app.anError}` }) : h('div', { class: 'skeleton' }));
  } else {
    const tt = an.totals;
    const delta = tt.connections - tt.previous;
    out.push(h('section', { class: 'kpis', 'aria-label': 'Key figures' },
      kpi('Connected now', nf.format(clients.length), `in ${shares.filter((sh) => sh.status.clients.length).length} of ${shares.length} sessions · ${controlNow} with control`),
      kpi('Connections', nf.format(tt.connections), [h('span', { class: delta > 0 ? 'up' : 'down', text: `${delta >= 0 ? '+' : '−'}${nf.format(Math.abs(delta))}` }), ' on the previous period', tt.reconnects ? ` · ${nf.format(tt.reconnects)} resumed` : '']),
      kpi('Time connected', tt.seconds ? dur(tt.seconds) : '0 m', tt.median_seconds ? `median visit ${dur(tt.median_seconds)}` : 'no finished visits yet'),
      kpi('Failed logins', nf.format(tt.auth_failures), `${nf.format(tt.lockouts)} lockouts · ${nf.format(tt.rejected)} refused joins${tt.blocked ? ` (${nf.format(tt.blocked)} by access rules)` : ''}`, tt.auth_failures > 0),
    ));
    out.push(h('section', { class: 'grid2 grid3' },
      h('figure', { class: 'card pad span2', style: null }, chartHead(), barChart(an)),
      whoCard(an),
    ));
    out.push(h('section', { class: 'grid2' }, hoursCard(an), sourcesCard(an)));
  }
  out.push(h('section', { class: 'grid2' }, liveCard(clients), recentCard(an)));
  return out;
}

function kpi(label, value, sub, bad) {
  return h('div', { class: 'card pad' },
    h('p', { class: 'kpi-label', text: label }),
    h('p', { class: `kpi-value${bad ? ' err' : ''}`, text: value }),
    h('p', { class: 'kpi-sub' }, sub));
}

function chartHead() {
  return h('figcaption', { class: 'chart-head' },
    h('h2', { text: app.an.hourly ? 'Connections per hour' : 'Connections per day' }),
    h('span', { class: 'legend' },
      h('span', null, h('span', { class: 'dot control' }), 'control'),
      h('span', null, h('span', { class: 'dot view' }), 'view only'),
      h('span', null, h('span', { class: 'dot err' }), 'failed login')));
}

function bucketLabel(b, i, an) {
  const d = new Date(b.start);
  if (an.hourly) return i % 3 === 0 ? `${String(d.getHours()).padStart(2, '0')}:00` : '';
  const n = an.series.length;
  if (n <= 7) return dayFmt.format(d);
  const every = n <= 31 ? 5 : 15;
  return (n - 1 - i) % every === 0 ? dateFmt.format(d) : '';
}

function barChart(an) {
  const W = 700, H = 220, L = 34, B = 190, T = 16;
  const n = an.series.length;
  const max = Math.max(1, ...an.series.map((b) => b.control + b.view));
  const step = niceStep(max);
  const top = Math.ceil(max / step) * step;
  const y = (v) => B - (v / top) * (B - T);
  const slot = (W - L) / n;
  const bw = Math.max(2, Math.min(44, slot * 0.62));
  const total = an.series.reduce((a, b) => a + b.control + b.view, 0);
  const peak = an.series.reduce((a, b) => (b.control + b.view > a.control + a.view ? b : a), an.series[0]);
  const summary = total
    ? `${nf.format(total)} connections; most in one ${an.hourly ? 'hour' : 'day'}: ${nf.format(peak.control + peak.view)}`
    : 'No connections in this period';
  const svgEl = s('svg', { viewBox: `0 0 ${W} ${H}`, class: 'chart', role: 'img', 'aria-label': summary });
  for (let v = 0; v <= top; v += step) {
    svgEl.append(s('line', { x1: L, x2: W, y1: y(v), y2: y(v), class: 'grid' }));
    svgEl.append(s('text', { x: L - 8, y: y(v) + 4, 'text-anchor': 'end', class: 'axis' }, nf.format(v)));
  }
  an.series.forEach((b, i) => {
    const x = L + i * slot + (slot - bw) / 2;
    const g = s('g', null);
    const when = an.hourly ? `${timeFmt.format(new Date(b.start))}` : fullDate(b.start);
    g.append(s('title', null, `${when}: ${b.control} control, ${b.view} view only, ${b.failures} failed logins`));
    g.append(s('rect', { x: L + i * slot, y: T, width: slot, height: B - T, class: 'hit' }));
    if (b.control) g.append(s('rect', { x, y: y(b.control), width: bw, height: B - y(b.control), rx: 3, class: 'c' }));
    if (b.view) g.append(s('rect', { x, y: y(b.control + b.view), width: bw, height: y(b.control) - y(b.control + b.view) - (b.control ? 2 : 0), rx: 3, class: 'v' }));
    if (b.failures) g.append(s('circle', { cx: x + bw / 2, cy: y(b.control + b.view) - 10, r: Math.min(7, 3 + b.failures), class: 'f' }));
    const label = bucketLabel(b, i, an);
    if (label) g.append(s('text', { x: x + bw / 2, y: H - 8, 'text-anchor': 'middle', class: 'axis' }, label));
    svgEl.append(g);
  });
  // The same numbers as a table, for screen readers.
  const table = h('table', { class: 'sr' },
    h('caption', { text: 'Connections by period' }),
    h('thead', null, h('tr', null, ['Period', 'Control', 'View only', 'Failed logins'].map((c) => h('th', { scope: 'col', text: c })))),
    h('tbody', null, an.series.map((b) => h('tr', null,
      h('th', { scope: 'row', text: an.hourly ? timeFmt.format(new Date(b.start)) : fullDate(b.start) }),
      h('td', { text: b.control }), h('td', { text: b.view }), h('td', { text: b.failures })))));
  return [svgEl, table, total ? null : h('p', { class: 'empty', text: 'Nobody connected in this period.' })];
}

function fullDate(v) { return new Intl.DateTimeFormat(undefined, { weekday: 'short', day: 'numeric', month: 'short' }).format(new Date(v)); }

function niceStep(max) {
  const raw = max / 4;
  const p = 10 ** Math.floor(Math.log10(raw));
  for (const m of [1, 2, 5, 10]) if (raw <= m * p) return Math.max(1, m * p);
  return Math.max(1, 10 * p);
}

function whoCard(an) {
  const max = Math.max(1, ...an.who.map((w) => w.connections));
  return h('div', { class: 'card pad' },
    h('h2', { text: 'Who connects' }),
    an.who.length
      ? h('ul', { class: 'bars', 'aria-label': 'Connections by person or link' }, an.who.map((w) => {
        const fill = h('span', { class: 'fill' });
        fill.style.width = `${(w.connections / max) * 100}%`;
        return h('li', null,
          h('span', null, who(w.name), ' ', h('span', { class: 'muted tiny', text: w.personal ? 'personal key' : 'shared' })),
          h('span', { class: 'mono soft', text: nf.format(w.connections), title: `${dur(w.seconds)} connected, last ${ago(w.last_seen)}` }),
          h('span', { class: 'track', 'aria-hidden': 'true' }, fill));
      }))
      : h('p', { class: 'empty', text: 'No connections yet.' }),
    h('p', { class: 'tiny muted', text: 'Names come from personal keys. A shared link cannot tell its holders apart.' }));
}

function hoursCard(an) {
  const max = Math.max(1, ...an.hours);
  const busiest = an.hours.indexOf(Math.max(...an.hours));
  return h('div', { class: 'card pad' },
    h('h2', { text: 'Busiest hours' }),
    h('div', { class: 'heat', role: 'img', 'aria-label': an.hours.some(Boolean) ? `Connections by hour of day; busiest at ${busiest}:00` : 'No connections in this period' },
      an.hours.map((v, i) => {
        const cell = h('span', { title: `${String(i).padStart(2, '0')}:00 — ${v} connection${v === 1 ? '' : 's'}` });
        cell.style.opacity = v ? String(0.18 + (v / max) * 0.82) : '0.08';
        return cell;
      })),
    h('div', { class: 'heat-axis', 'aria-hidden': 'true' }, h('span', { text: '00:00' }), h('span', { text: '12:00' }), h('span', { text: '23:00' })));
}

function sourcesCard(an) {
  const max = Math.max(1, ...an.sources.map((x) => x.connections));
  return h('div', { class: 'card pad' },
    h('h2', { text: 'Where they connect' }),
    an.sources.length
      ? h('ul', { class: 'bars' }, an.sources.map((x) => {
        const fill = h('span', { class: 'fill' });
        fill.style.width = `${(x.connections / max) * 100}%`;
        return h('li', null, h('span', { text: sourceName(x.source) }), h('span', { class: 'mono soft', text: nf.format(x.connections) }), h('span', { class: 'track', 'aria-hidden': 'true' }, fill));
      }))
      : h('p', { class: 'empty', text: 'No connections yet.' }));
}

function liveCard(clients) {
  return h('div', { class: 'card' },
    h('div', { class: 'card-head' }, h('h2', { text: 'Live now' }), h('a', { href: '#/sessions', class: 'small', text: 'Manage sessions →' })),
    clients.length
      ? h('div', { class: 'table-wrap' }, h('table', null,
        h('thead', null, h('tr', null, ['Who', 'Where', 'Connected', 'Device'].map((c) => h('th', { scope: 'col', text: c })))),
        h('tbody', null, clients.map(({ c, sh }) => h('tr', null,
          h('td', null, cap(who(c.name)), ' ', rolePill(c.role)),
          h('td', { text: sourceName(sh.source) }),
          h('td', { class: 'mono', text: dur((Date.now() - new Date(c.since)) / 1000) }),
          h('td', { class: 'muted small', text: c.device || '—' }))))))
      : h('p', { class: 'empty', text: 'Nobody is connected.' }));
}

function rolePill(role) {
  return h('span', { class: `pill ${role === 'view' ? 'view' : 'control'}`, text: role === 'view' ? 'view' : 'control' });
}

function feed(events) {
  return h('ul', { class: 'feed' }, events.map((e) => h('li', null,
    h('time', { datetime: e.t, title: fullFmt.format(new Date(e.t)), text: shortTime(e.t) }),
    h('span', { class: `dot ${EVENT_COLOR[e.kind] || ''}`, 'aria-hidden': 'true' }),
    h('span', { text: describe(e) }))));
}

function recentCard(an) {
  const evs = an ? an.events.slice(0, 8) : [];
  return h('div', { class: 'card' },
    h('div', { class: 'card-head' }, h('h2', { text: 'Recent activity' }), h('a', { href: '#/activity', class: 'small', text: 'All activity →' })),
    h('div', { class: 'pad' }, evs.length ? feed(evs) : h('p', { class: 'empty', text: 'Nothing has happened in this period.' })));
}

// ---------- Live sessions ----------

function sessions() {
  const shares = app.st.shares;
  const out = [head('Live sessions', 'Service profiles and background shares on this computer. Refreshes every 3 seconds.',
    h('label', { class: 'filters' }, h('input', {
      type: 'checkbox', checked: app.hideIdle, 'data-key': 'hide-idle',
      onchange: (ev) => { app.hideIdle = ev.target.checked; render(); },
    }), h('span', { class: 'soft small', text: 'Hide sessions nobody is connected to' })))];
  out.push(...attention().filter((a) => a.classList.contains('err')));
  const shown = shares.filter((sh) => !app.hideIdle || !sh.status || sh.error || sh.status.clients.length || sh.type === 'background' || isSet(sh.status.locked_until));
  if (!shares.length) {
    out.push(h('div', { class: 'card pad' },
      h('p', { text: 'Nothing is being shared unattended right now.' }),
      h('p', { class: 'muted small' }, 'Background shares (', h('span', { class: 'mono', text: 'setu share -d' }), ') and service profiles (', h('span', { class: 'mono', text: 'setu service add' }), ') show up here while they run.')));
  } else if (!shown.length) {
    out.push(h('p', { class: 'card pad muted', text: `Nobody is connected to any of the ${shares.length} running sessions.` }));
  }
  for (const sh of shown) out.push(shareCard(sh));
  out.push(h('p', { class: 'footnote', text: 'Disconnecting also cancels the client\'s resume token, so it cannot reconnect without logging in again. To keep a device out for good, remove it under Profiles and devices.' }));
  return out;
}

function shareCard(sh) {
  const st = sh.status;
  if (!st) return h('p', { class: 'card pad err-text', text: sh.error });
  const locked = isSet(st.locked_until);
  const state = locked
    ? h('span', { class: 'pill warn' }, h('span', { class: 'dot warn' }), `locked until ${timeFmt.format(new Date(st.locked_until))}`)
    : st.paused ? h('span', { class: 'pill warn' }, h('span', { class: 'dot warn' }), 'new joins paused')
    : st.accepting
      ? h('span', { class: 'pill ok' }, h('span', { class: 'dot ok' }), sh.type === 'profile' ? 'serving' : 'accepting')
      : h('span', { class: 'pill' }, 'not admitting new clients');
  const meta = [
    h('span', { class: 'mono', text: st.command || '—' }),
    h('span', { text: st.kind === 'desktop' ? 'remote desktop' : 'terminal' }),
    st.auth && h('span', { text: authLabel(st.auth) }),
    h('span', { text: `${st.clients.length} of ${st.max_clients || '?'} clients` }),
    st.view_link && h('span', { text: 'has a view link' }),
    isSet(st.started) && h('span', { text: `${sh.type === 'profile' ? 'up' : 'started'} ${dur((Date.now() - new Date(st.started)) / 1000)}${sh.pid ? ` · pid ${sh.pid}` : ''}` }),
    st.pending ? h('span', { text: `${st.pending} joining` }) : null,
    st.resumable ? h('span', { text: `${st.resumable} may reconnect` }) : null,
    st.recent_failures ? h('span', { class: 'err-text', text: `${st.recent_failures} recent failed logins` }) : null,
  ];
  const rules = (app.st.access || []).filter((a) => a.scope === sh.source || (sh.type === 'profile' && a.scope === 'profile:*')).reduce((n, a) => n + a.rules.length, 0);
  if (rules) meta.push(h('a', { href: '#/access', text: `${rules} access rule${rules === 1 ? '' : 's'}` }));
  const actions = [
    st.accepting && h('button', {
      type: 'button', class: 'btn', 'data-key': `pause-${sh.source}`, text: st.paused ? 'Resume new joins' : 'Pause new joins',
      title: st.paused ? 'Admit new clients again' : 'Refuse new joins for now; connected clients stay',
      onclick: () => shareAction(sh, st.paused ? 'resume' : 'pause'),
    }),
    st.clients.length > 1 && h('button', { type: 'button', class: 'btn btn-danger', 'data-key': `dcall-${sh.source}`, text: 'Disconnect everyone', onclick: () => disconnectAll(sh) }),
    sh.type === 'background'
      ? h('button', { type: 'button', class: 'btn btn-danger', 'data-key': `stop-${sh.name}`, text: 'Stop share', onclick: () => stopShare(sh) })
      : h('button', { type: 'button', class: 'btn btn-danger', 'data-key': `end-${sh.name}`, text: 'End session', onclick: () => endSession(sh) }),
  ];
  return h('section', { class: 'card unit', 'aria-label': sourceName(sh.source) },
    h('div', { class: 'unit-head' },
      h('div', null,
        h('h2', { class: 'unit-title' }, h('span', { class: sh.type === 'background' ? 'mono' : '', text: sh.name }),
          h('span', { class: 'pill', text: sh.type === 'profile' ? 'service profile' : 'background share' }), state),
        h('p', { class: 'meta' }, meta),
        sh.error && h('p', { class: 'meta err-text', text: sh.error })),
      h('div', { class: 'unit-actions' }, actions)),
    st.clients.length ? clientTable(sh) : h('p', { class: 'empty', text: 'Nobody connected.' }));
}

function clientTable(sh) {
  return h('div', { class: 'table-wrap' }, h('table', { class: 'stack' },
    h('thead', null, h('tr', null, ['Who', 'Device', 'Network', 'Verification', 'Connected'].map((c) => h('th', { scope: 'col', text: c })), h('th', null, h('span', { class: 'sr', text: 'Actions' })))),
    h('tbody', null, sh.status.clients.map((c) => h('tr', null,
      h('td', null, cap(who(c.name)), ' ', rolePill(c.role),
        h('span', { class: 'sub', text: [c.personal ? 'personal key' : 'shared link', authText(c.auth), c.first_id ? 'resumed' : '', c.hidden ? 'page in background' : ''].filter(Boolean).join(' · ') })),
      h('td', { 'data-label': 'Device' }, c.device || h('span', { class: 'muted', text: 'not reported' }),
        h('span', { class: 'sub mono', text: c.device_key ? `device key ${c.device_key}` : 'no device key' })),
      h('td', { 'data-label': 'Network' }, h('span', { class: 'mono', text: c.address || '—' }), h('span', { class: 'sub', text: network(c) })),
      h('td', { class: 'code', 'data-label': 'Verification code', text: c.verification_code || '—' }),
      h('td', { 'data-label': 'Connected' }, h('span', { class: 'mono', text: dur((Date.now() - new Date(c.since)) / 1000) }),
        h('span', { class: 'sub', text: c.role === 'view' ? 'watching' : isSet(c.last_input) ? `last input ${ago(c.last_input)}` : 'no input yet' })),
      h('td', { class: 'act' },
        h('button', {
          type: 'button', class: 'btn btn-danger', text: 'Disconnect', 'data-key': `dc-${sh.source}-${c.id}`,
          'aria-label': `Disconnect ${who(c.name)} from ${sourceName(sh.source)}`,
          onclick: () => disconnect(sh, c),
        }), ' ',
        h('button', {
          type: 'button', class: 'btn btn-danger', text: 'Block…', 'data-key': `bl-${sh.source}-${c.id}`,
          'aria-label': `Block ${who(c.name)}`, disabled: !c.device_key && !c.personal && !c.address,
          onclick: () => blockClient(sh, c),
        })))))));
}

function authText(a) {
  return { device: 'known device', pairing: 'paired just now', password: 'password', none: 'link only' }[a] || '';
}

// ---------- Profiles ----------

function profiles() {
  const st = app.st;
  const out = [head('Profiles and devices', ['Always-on shares served by ', h('span', { class: 'mono', text: 'setu service' }), '. Changes apply within a few seconds.'],
    st.policy.managed ? h('span', { class: 'pill', title: st.policy.source }, icon(I.shield), 'Managed policy in force') : null)];
  if (st.policy.error) out.push(h('section', { class: 'card attention err' }, icon(I.warn), h('p', { text: `The managed policy cannot be read, so nothing is served and changes are refused: ${st.policy.error}` })));
  if (st.service.config_error) out.push(h('section', { class: 'card attention err' }, icon(I.warn), h('p', { text: st.service.config_error })));
  if (!st.service.running && st.profiles.length) {
    out.push(h('section', { class: 'card attention' }, icon(I.warn), h('p', null, 'The service is not running, so no profile is served. Start it with ', h('span', { class: 'mono', text: 'setu service install' }), '.')));
  }
  if (!st.profiles.length) {
    out.push(h('div', { class: 'card pad' },
      h('p', { text: 'No service profiles yet.' }),
      h('p', { class: 'muted small' }, 'Create one in a terminal, for example ', h('span', { class: 'mono', text: 'setu service add claude -- claude' }), '.')));
  }
  for (const p of st.profiles) out.push(profileCard(p));
  out.push(h('p', { class: 'footnote' }, 'Links, keys and passwords never appear here. Show a profile\'s links with ', h('span', { class: 'mono', text: 'setu service link NAME' }), '; add or remove profiles and change how they sign in from the command line.'));
  return out;
}

function profileStatus(p) {
  if (p.problem && p.enabled) return h('span', { class: 'pill err', text: 'not served' });
  if (!p.enabled) return h('span', { class: 'pill', text: 'disabled' });
  if (p.expired) return h('span', { class: 'pill err', text: 'keys expired' });
  if (p.serving) return h('span', { class: 'pill ok' }, h('span', { class: 'dot ok' }), p.connected ? `serving · ${p.connected} connected` : 'serving');
  if (!app.st.service.running) return h('span', { class: 'pill warn', text: 'service not running' });
  return h('span', { class: 'pill warn', text: 'starting' });
}

function profileCard(p) {
  const people = p.people.map((x) => `${x.name} (${x.role})`);
  if (p.shared_control) people.push('shared control link');
  if (p.shared_view) people.push('shared view link');
  const expiring = isSet(p.expires_at) && !p.expired && new Date(p.expires_at) - Date.now() < 7 * 86400e3;
  const toggle = h('label', { class: 'switch' },
    h('input', { type: 'checkbox', checked: p.enabled, 'data-key': `en-${p.name}`, disabled: !!app.st.policy.error, onchange: (ev) => setEnabled(p, ev.target) }),
    h('span', { class: 'track', 'aria-hidden': 'true' }), 'Enabled');
  return h('section', { class: `card unit${p.problem && p.enabled ? ' err' : ''}`, 'aria-label': `Profile ${p.name}` },
    h('div', { class: 'unit-head' },
      h('div', null,
        h('h2', { class: 'unit-title' }, p.name, profileStatus(p), expiring ? h('span', { class: 'pill warn', text: `keys expire ${until(p.expires_at)}` }) : null),
        h('p', { class: 'meta' }, h('span', { class: p.kind === 'desktop' ? '' : 'mono', text: p.label }), p.dir && h('span', { text: `in ${p.dir}` })),
        p.problem && p.enabled && h('p', { class: 'meta err-text', text: p.problem })),
      toggle,
      h('button', { type: 'button', class: `btn${p.expired || expiring ? ' btn-primary' : ''}`, 'data-key': `rot-${p.name}`, text: 'Rotate keys', disabled: !!app.st.policy.error, onclick: () => rotate(p) }),
      p.auth === 'device' && h('button', { type: 'button', class: 'btn btn-primary', 'data-key': `pair-${p.name}`, text: 'Pair a device', disabled: !!app.st.policy.error, onclick: () => pair(p) })),
    h('dl', { class: 'facts' },
      fact('Sign-in', authLabel(p.auth)),
      fact('Links', people.join(', ') || 'none'),
      fact('Keys expire', isSet(p.expires_at) ? `${until(p.expires_at)} (${dateFmt.format(new Date(p.expires_at))})` : 'never'),
      fact('Clients at once', String(p.max_clients)),
      fact('Idle timeout', p.idle_timeout || 'off'),
      fact('Recording', p.recording ? 'on' : 'off'),
      fact('Uploads', p.uploads ? 'allowed' : 'off'),
      fact('Access rules', accessSummary(p.name)),
      isSet(p.pairing_until) && fact('Pairing code', `active, expires ${until(p.pairing_until)}`)),
    devicesTable(p));
}

function fact(k, v) { return h('div', null, h('dt', { text: k }), h('dd', { text: v })); }

function devicesTable(p) {
  if (p.auth === 'none' && !p.devices.length) return null;
  const idleMs = parseDuration(p.device_idle);
  return [
    h('div', { class: 'sub-head' },
      h('h3', null, 'Known devices ', h('span', { class: 'muted', text: `(${p.devices.length})` })),
      h('span', { class: 'tiny muted', text: p.device_idle ? `Unused devices are forgotten after ${dur(idleMs / 1000)}` : 'Unused devices are kept until removed' })),
    p.devices.length
      ? h('div', { class: 'table-wrap' }, h('table', { class: 'stack' },
        h('thead', null, h('tr', null, ['Device', 'For', 'Added', 'Last seen'].map((c) => h('th', { scope: 'col', text: c })), h('th', null, h('span', { class: 'sr', text: 'Actions' })))),
        h('tbody', null, p.devices.map((d) => {
          const stale = idleMs && isSet(d.last_seen) && Date.now() - new Date(d.last_seen) > idleMs * 0.75;
          return h('tr', null,
            h('td', null, d.name || h('span', { class: 'muted', text: 'unnamed' }), h('span', { class: 'sub mono', text: d.fingerprint })),
            h('td', { 'data-label': 'For' }, d.for, h('span', { class: 'sub', text: { pairing: 'paired with a code', password: 'proved the password', manual: 'added from the command line' }[d.how] || d.how })),
            h('td', { 'data-label': 'Added', text: isSet(d.added) ? dateFmt.format(new Date(d.added)) : '—' }),
            h('td', { 'data-label': 'Last seen', class: stale ? 'err-text' : '', text: isSet(d.last_seen) ? ago(d.last_seen) : 'never' }),
            h('td', { class: 'act' }, h('button', {
              type: 'button', class: 'btn btn-danger', text: 'Remove', 'data-key': `rm-${p.name}-${d.fingerprint}`,
              'aria-label': `Remove device ${d.name || d.fingerprint} from ${p.name}`, onclick: () => removeDevice(p, d),
            })));
        }))))
      : h('p', { class: 'empty', text: p.auth === 'device' ? 'No devices yet. Pair one to let it in.' : 'No remembered devices.' }),
  ];
}

function parseDuration(str) {
  if (!str) return 0;
  let ms = 0;
  for (const [, n, u] of str.matchAll(/([\d.]+)(h|m|s|ms|us|µs|ns)/g)) {
    ms += parseFloat(n) * ({ h: 3600e3, m: 60e3, s: 1e3, ms: 1 }[u] || 0);
  }
  return ms;
}

// ---------- Activity ----------

const KIND_LABELS = [
  ['', 'All events'], ['connect', 'Connections'], ['disconnect', 'Disconnections'], ['reconnect', 'Reconnections'],
  ['auth_failure', 'Failed logins'], ['rejected', 'Refused joins'], ['lockout', 'Lockouts'], ['paired', 'Pairings'],
  ['invite_disabled', 'Invites disabled'], ['dashboard', 'Dashboard changes'],
];

function activity() {
  const out = [head('Activity', 'Who connected, from where and for how long. Kept on this computer only; keystrokes and output are never part of it.', rangePicker())];
  const kindSel = h('select', { 'data-key': 'kind', 'aria-label': 'Kind of event', onchange: (ev) => { app.activityKind = ev.target.value; render(); } },
    KIND_LABELS.map(([v, l]) => h('option', { value: v, text: l })));
  kindSel.value = app.activityKind;
  const search = h('input', { type: 'search', placeholder: 'Filter by name, address or share', 'data-key': 'q', 'aria-label': 'Filter events', value: app.activityText,
    oninput: (ev) => { app.activityText = ev.target.value; renderActivityList(); } });
  out.push(h('div', { class: 'filters' }, kindSel, search));
  out.push(h('div', { class: 'card pad', id: 'activity-list' }, activityList()));
  out.push(h('p', { class: 'footnote', text: 'The newest 60 events of the period are listed. The full record, if you configured one, is in your audit collector.' }));
  return out;
}

function activityList() {
  if (!app.an) return app.anError ? h('p', { class: 'err-text', text: app.anError }) : h('div', { class: 'skeleton' });
  const q = app.activityText.trim().toLowerCase();
  const evs = app.an.events.filter((e) => (!app.activityKind || e.kind === app.activityKind)
    && (!q || [describe(e), e.addr, e.device_key, e.src].join(' ').toLowerCase().includes(q)));
  return evs.length ? feed(evs) : h('p', { class: 'empty', text: 'No events match.' });
}

function renderActivityList() {
  document.getElementById('activity-list')?.replaceChildren(activityList());
}

// ---------- dialogs and actions ----------

function dialog({ title, body, confirm, danger, typeToConfirm, cancel = 'Cancel', alert }) {
  const dlg = document.getElementById('dialog');
  return new Promise((resolve) => {
    const input = typeToConfirm ? h('input', { type: 'text', autocomplete: 'off', spellcheck: 'false', 'aria-describedby': 'dialog-hint' }) : null;
    const ok = h('button', { type: 'submit', class: `btn ${danger ? 'btn-danger-solid' : 'btn-primary'}`, value: 'ok', text: confirm, disabled: !!typeToConfirm });
    if (input) input.addEventListener('input', () => { ok.disabled = input.value !== typeToConfirm; });
    const form = h('form', { method: 'dialog' },
      h('h2', { id: 'dialog-title', text: title }),
      body,
      input && h('label', null, h('span', { id: 'dialog-hint' }, 'Type ', h('strong', { class: 'mono', text: typeToConfirm }), ' to confirm'), input),
      h('div', { class: 'dialog-actions' }, alert ? null : h('button', { type: 'submit', class: 'btn', value: 'cancel', text: cancel, formnovalidate: true }), ok));
    dlg.replaceChildren(form);
    const done = () => {
      dlg.removeEventListener('close', done);
      const yes = dlg.returnValue === 'ok';
      dlg.replaceChildren(); // nothing lingers in the DOM, a pairing code included
      resolve(yes);
    };
    dlg.addEventListener('close', done);
    dlg.returnValue = '';
    dlg.showModal();
    (input || (danger ? form.querySelector('[value=cancel]') : ok))?.focus();
  });
}

function toast(msg, isErr) {
  const el = h('div', { class: `toast${isErr ? ' err' : ''}` }, icon(isErr ? I.warn : I.ok), h('span', { text: msg }));
  document.getElementById('toasts').append(el);
  setTimeout(() => el.remove(), isErr ? 9000 : 5000);
}

async function act(fn, okMsg) {
  try {
    const r = await fn();
    if (okMsg) toast(okMsg);
    poll(true);
    return r;
  } catch (err) {
    if (err instanceof AuthError) { gate(err.message); return null; }
    toast(err.message, true);
    poll(true);
    return null;
  }
}

async function disconnect(sh, c) {
  const name = who(c.name);
  const yes = await dialog({
    title: `Disconnect ${name}?`,
    body: h('ul', null,
      h('li', { text: `${cap(name)} is cut off from ${sourceName(sh.source)} now.` }),
      h('li', { text: 'Their resume token stops working, so they must join and log in again.' }),
      c.personal || c.device_key ? h('li', { text: 'To keep this device out for good, remove it under Profiles and devices, or rotate the keys.' }) : null),
    confirm: 'Disconnect', danger: true,
  });
  if (yes) await act(() => api('POST', '/api/clients/disconnect', { source: sh.source, client: c.id }), `Disconnected ${name} from ${sourceName(sh.source)}.`);
}

async function stopShare(sh) {
  const yes = await dialog({
    title: `Stop background share ${sh.name}?`,
    body: h('ul', null,
      h('li', null, 'Its command (', h('span', { class: 'mono', text: sh.status?.command || '' }), ') is ended and everyone connected is disconnected.'),
      h('li', { text: 'Its links stop working for good.' })),
    confirm: 'Stop share', danger: true,
  });
  if (yes) await act(() => api('POST', `/api/shares/${encodeURIComponent(sh.name)}/stop`, {}), `Stopped background share ${sh.name}.`);
}

async function setEnabled(p, input) {
  const on = input.checked;
  input.checked = !on; // until it is confirmed and saved
  if (!on && p.connected) {
    const yes = await dialog({
      title: `Disable ${p.name}?`,
      body: h('p', { class: 'soft', text: `${p.connected} connected client${p.connected === 1 ? ' is' : 's are'} disconnected, and its links stop working until you enable it again.` }),
      confirm: 'Disable', danger: true,
    });
    if (!yes) return;
  }
  await act(() => api('POST', `/api/profiles/${encodeURIComponent(p.name)}/enabled`, { enabled: on }), `${p.name} ${on ? 'enabled' : 'disabled'}. The service applies it within a few seconds.`);
}

async function rotate(p) {
  const yes = await dialog({
    title: `Rotate the keys of ${p.name}?`,
    body: h('ul', null,
      h('li', null, 'Every current link to ', h('strong', { text: p.name }), ' stops working at once.'),
      h('li', { text: 'Anyone connected is disconnected while the profile restarts.' }),
      h('li', null, 'Get the new links in a terminal with ', h('span', { class: 'mono', text: `setu service link ${p.name}` }), '. They are never shown here.')),
    confirm: 'Rotate keys', danger: true, typeToConfirm: p.name,
  });
  if (yes) await act(() => api('POST', `/api/profiles/${encodeURIComponent(p.name)}/rotate`, { confirm: p.name }), `New keys for ${p.name}. Old links no longer work.`);
}

async function pair(p) {
  const r = await act(() => api('POST', `/api/profiles/${encodeURIComponent(p.name)}/pair`, {}));
  if (!r) return;
  const left = h('strong', { class: 'mono' });
  const tick = () => {
    const secs = Math.max(0, Math.round((new Date(r.expires) - Date.now()) / 1000));
    left.textContent = `${Math.floor(secs / 60)}:${String(secs % 60).padStart(2, '0')}`;
  };
  tick();
  const iv = setInterval(tick, 1000);
  const copy = h('button', { type: 'button', class: 'btn', text: 'Copy code', onclick: async () => {
    try { await navigator.clipboard.writeText(r.code); copy.textContent = 'Copied'; } catch { copy.textContent = 'Copy failed: select the code'; }
  } });
  await dialog({
    title: `Pairing code for ${p.name}`,
    body: [
      h('p', { class: 'soft small', text: 'Open the profile\'s link on the new device and enter this code when asked. It works for one device.' }),
      h('div', { class: 'secret', text: r.code }),
      h('p', { class: 'small muted' }, 'Expires in ', left, '. It is shown only now; closing this hides it.'),
      h('div', null, copy),
    ],
    confirm: 'Done', alert: true,
  });
  clearInterval(iv);
}

async function removeDevice(p, d) {
  const yes = await dialog({
    title: `Remove ${d.name || 'this device'} from ${p.name}?`,
    body: h('ul', null,
      h('li', null, 'Device ', h('span', { class: 'mono', text: d.fingerprint }), ` (${d.for}) can no longer join without ${p.auth === 'device' ? 'a new pairing code' : 'the password'}.`),
      h('li', { text: 'If it is connected now, it is disconnected within seconds.' })),
    confirm: 'Remove device', danger: true,
  });
  if (yes) await act(() => api('POST', `/api/profiles/${encodeURIComponent(p.name)}/devices/remove`, { fingerprint: d.fingerprint }), `Removed the device from ${p.name}.`);
}

// ---------- block and allow lists ----------

const KIND_TEXT = { device: 'Device key', person: 'Person', network: 'Network' };
const FOR_OPTIONS = [['', 'Until removed'], ['1h', '1 hour'], ['24h', '24 hours'], ['7d', '7 days'], ['30d', '30 days']];

function activeRules(v) { return v.rules.filter((r) => !isSet(r.expires) || new Date(r.expires) > Date.now()); }

function accessSummary(name) {
  const own = (app.st.access || []).find((a) => a.scope === `profile:${name}`);
  const all = (app.st.access || []).find((a) => a.scope === 'profile:*');
  const rules = [...(own ? activeRules(own) : []), ...(all ? activeRules(all) : [])];
  if (!rules.length) return 'none';
  const allow = rules.some((r) => r.action === 'allow');
  return `${rules.length} rule${rules.length === 1 ? '' : 's'}${allow ? ' · allow list in force' : ''}`;
}

function accessPage() {
  const lists = app.st.access || [];
  const out = [head('Block and allow lists', 'Keep a device, a person or a network out, or let only them in. Rules come on top of the link and login, never instead of them.',
    h('a', { class: 'btn', href: '#/sessions', text: 'Live sessions' }))];
  out.push(h('div', { class: 'card pad small soft' },
    h('p', null, h('strong', { text: 'How rules work. ' }), 'A matching block rule always refuses. If a list has any allow rule, only clients that match one of its allow rules may join; everyone else is refused, including clients connected now. Rules for every service profile apply on top of each profile\'s own.'),
    h('p', { class: 'tiny muted' }, 'Device keys and personal keys are proven by the connection. Network rules match the address the connection comes from (a TURN server\'s if it is relayed); block rules also match the addresses a client lists for itself.')));
  if (!lists.length) out.push(h('p', { class: 'card pad muted', text: 'There is nothing to protect yet: create a service profile or start a background share.' }));
  for (const v of lists) out.push(accessCard(v));
  return out;
}

function accessCard(v) {
  const act = activeRules(v);
  const allow = act.some((r) => r.action === 'allow');
  return h('section', { class: `card unit${v.error ? ' err' : ''}`, 'aria-label': v.label },
    h('div', { class: 'unit-head' },
      h('div', null,
        h('h2', { class: 'unit-title' }, v.label,
          allow ? h('span', { class: 'pill warn', text: 'allow list in force' }) : act.length ? h('span', { class: 'pill', text: `${act.length} block rule${act.length === 1 ? '' : 's'}` }) : h('span', { class: 'pill', text: 'no rules' })),
        h('p', { class: 'meta', text: v.scope === 'profile:*' ? 'Applies to every service profile.' : v.scope.startsWith('share:') ? 'Ends with the share.' : 'Applies to this profile, on top of the rules for every profile.' }),
        v.error && h('p', { class: 'meta err-text', text: `${v.error} Nobody can join until this is fixed.` })),
      h('div', { class: 'unit-actions' },
        h('button', { type: 'button', class: 'btn btn-danger', 'data-key': `blk-${v.scope}`, text: 'Block…', disabled: !!v.error, onclick: () => addRule(v, 'block') }),
        h('button', { type: 'button', class: 'btn', 'data-key': `alw-${v.scope}`, text: 'Allow only…', disabled: !!v.error, onclick: () => addRule(v, 'allow') }))),
    v.rules.length
      ? h('div', { class: 'table-wrap' }, h('table', { class: 'stack' },
        h('thead', null, h('tr', null, ['Rule', 'Matches', 'Expires', 'Note'].map((c) => h('th', { scope: 'col', text: c })), h('th', null, h('span', { class: 'sr', text: 'Actions' })))),
        h('tbody', null, v.rules.map((r) => {
          const expired = isSet(r.expires) && new Date(r.expires) <= Date.now();
          return h('tr', null,
            h('td', null, h('span', { class: `pill ${expired ? '' : r.action === 'block' ? 'err' : 'ok'}`, text: expired ? 'expired' : r.action }),
              h('span', { class: 'sub', text: `added ${ago(r.added)}` })),
            h('td', { 'data-label': 'Matches' }, KIND_TEXT[r.kind] || r.kind, h('span', { class: 'sub mono', text: r.value })),
            h('td', { 'data-label': 'Expires', text: isSet(r.expires) ? (expired ? `${ago(r.expires)}` : until(r.expires)) : 'never' }),
            h('td', { 'data-label': 'Note', class: 'soft', text: r.note || '—' }),
            h('td', { class: 'act' }, h('button', {
              type: 'button', class: 'btn', text: 'Remove', 'data-key': `rmr-${r.id}`,
              'aria-label': `Remove the ${r.action} rule for ${r.kind} ${r.value}`, onclick: () => removeRule(v, r),
            })));
        }))))
      : h('p', { class: 'empty', text: 'No rules: anyone with a valid link and login may join.' }));
}

function select(name, options, value) {
  const el = h('select', { name }, options.map(([v, l]) => h('option', { value: v, text: l })));
  el.value = value;
  return el;
}

async function addRule(v, action) {
  const kind = select('kind', [['device', 'Device key'], ['person', 'Person (personal key)'], ['network', 'Network (IP address or range)']], 'device');
  const value = h('input', { type: 'text', name: 'value', required: true, autocomplete: 'off', spellcheck: 'false', placeholder: '3f2a-91c0-77de-0b4e-a1c9' });
  kind.addEventListener('change', () => { value.placeholder = { device: '3f2a-91c0-77de-0b4e-a1c9', person: 'alice', network: '203.0.113.0/24' }[kind.value]; });
  const dur = select('for', FOR_OPTIONS, '');
  const note = h('input', { type: 'text', name: 'note', maxlength: '200', autocomplete: 'off', placeholder: 'Optional' });
  const firstAllow = action === 'allow' && !activeRules(v).some((r) => r.action === 'allow');
  const ok = await dialog({
    title: action === 'block' ? `Block on ${v.label.toLowerCase()}` : `Allow only… on ${v.label.toLowerCase()}`,
    body: [
      h('label', null, 'What', kind),
      h('label', null, 'Value', value),
      h('label', null, 'For how long', dur),
      h('label', null, 'Note', note),
      firstAllow ? h('p', { class: 'small err-text', text: 'This is the first allow rule here: from now on only clients that match an allow rule can join, and connected clients that do not are disconnected. Make sure your own devices match.' }) : null,
      action === 'block' ? h('p', { class: 'small soft', text: 'Matching clients are disconnected now and refused until the rule is removed or expires.' }) : null,
    ],
    confirm: action === 'block' ? 'Block' : 'Add allow rule', danger: action === 'block' || firstAllow,
  });
  if (!ok) return;
  const r = await act(() => api('POST', '/api/access/add', { scope: v.scope, kind: kind.value, value: value.value.trim(), action, for: dur.value, note: note.value.trim() }));
  if (r) toast(`${action === 'block' ? 'Blocked' : 'Allowed'} ${r.rule.kind} ${r.rule.value}${r.disconnected ? `; disconnected ${r.disconnected}` : ''}.`);
}

async function removeRule(v, r) {
  const yes = await dialog({
    title: `Remove this ${r.action} rule?`,
    body: h('p', { class: 'soft' }, `${KIND_TEXT[r.kind]} `, h('span', { class: 'mono', text: r.value }),
      r.action === 'block' ? ' will be able to join again (with a valid link and login).' : ' is no longer on the allow list here.'),
    confirm: 'Remove rule', danger: r.action === 'allow',
  });
  if (yes) await act(() => api('POST', '/api/access/remove', { scope: v.scope, id: r.id }), 'Rule removed.');
}

async function blockClient(sh, c) {
  const name = who(c.name);
  const opts = [];
  const box = (kind, value, label, checked) => {
    const el = h('input', { type: 'checkbox', checked });
    opts.push({ el, kind, value });
    return h('label', { class: 'check' }, el, h('span', null, label, ' ', h('span', { class: 'mono soft', text: value })));
  };
  const choices = [
    c.device_key && box('device', c.device_key, 'This device', true),
    c.personal && box('person', c.name, `Everyone using ${c.name}'s personal key`, !c.device_key),
    c.address && box('network', c.address, 'This network address', !c.device_key && !c.personal),
  ];
  const where = sh.type === 'profile'
    ? select('scope', [[sh.source, `Profile ${sh.name} only`], ['profile:*', 'Every service profile']], sh.source)
    : select('scope', [[sh.source, `Background share ${sh.name}`]], sh.source);
  const dur = select('for', FOR_OPTIONS, '24h');
  const note = h('input', { type: 'text', maxlength: '200', autocomplete: 'off', placeholder: 'Optional' });
  const everywhere = c.personal ? h('input', { type: 'checkbox', checked: true }) : null;
  const ok = await dialog({
    title: `Block ${name}?`,
    body: [
      h('fieldset', { class: 'choices' }, h('legend', { text: 'Block' }), choices),
      h('label', null, 'Where', where),
      h('label', null, 'For how long', dur),
      h('label', null, 'Note', note),
      everywhere && h('label', { class: 'check' }, everywhere, h('span', { text: `Also disconnect ${c.name} from every session now` })),
      h('p', { class: 'small soft', text: 'Matching clients are disconnected at once and cannot resume. Remove the rule under Block and allow lists to let them back in. For a lost device, also remove it under Profiles and devices or rotate the keys.' }),
    ],
    confirm: 'Block', danger: true,
  });
  if (!ok) return;
  const picked = opts.filter((o) => o.el.checked);
  if (!picked.length) { toast('Nothing was selected to block.', true); return; }
  let done = 0;
  for (const o of picked) {
    const r = await act(() => api('POST', '/api/access/add', { scope: where.value, kind: o.kind, value: o.value, action: 'block', for: dur.value, note: note.value.trim() }));
    if (r) done++;
  }
  if (everywhere?.checked) await act(() => api('POST', '/api/people/disconnect', { name: c.name }));
  if (done) toast(`Blocked ${name} (${done} rule${done === 1 ? '' : 's'}).`);
}

async function shareAction(sh, action) {
  const label = { pause: 'New joins paused', resume: 'New joins resumed' }[action];
  await act(() => api('POST', '/api/shares/action', { source: sh.source, action }), `${label} on ${sourceName(sh.source)}.`);
}

async function disconnectAll(sh) {
  const n = sh.status.clients.length;
  const yes = await dialog({
    title: `Disconnect everyone from ${sourceName(sh.source)}?`,
    body: h('ul', null,
      h('li', { text: `All ${n} connected clients are cut off now and their resume tokens stop working.` }),
      h('li', { text: 'The shared command keeps running, and anyone with a valid link and login can join again. Pause new joins first to keep them out.' })),
    confirm: 'Disconnect everyone', danger: true,
  });
  if (yes) await act(() => api('POST', '/api/shares/action', { source: sh.source, action: 'disconnect_all' }), `Disconnected everyone from ${sourceName(sh.source)}.`);
}

async function endSession(sh) {
  const yes = await dialog({
    title: `End the session of ${sh.name}?`,
    body: h('ul', null,
      h('li', null, 'The shared command (', h('span', { class: 'mono', text: sh.status.command }), ') is stopped and everyone is disconnected.'),
      h('li', { text: 'The service starts a fresh session within seconds. Links keep working: to keep people out, disable the profile, rotate its keys or block them.' })),
    confirm: 'End session', danger: true, typeToConfirm: sh.name,
  });
  if (yes) await act(() => api('POST', '/api/shares/action', { source: sh.source, action: 'terminate' }), `Ended the session of ${sh.name}; a fresh one starts shortly.`);
}

// ---------- session, theme ----------

function gate(msg) {
  app.signedOut = true;
  clearTimeout(app.timer);
  banner('');
  document.getElementById('dialog').open && document.getElementById('dialog').close();
  document.querySelector('.side').hidden = true;
  document.getElementById('view').replaceChildren(h('section', { class: 'gate card pad' },
    icon(I.lock),
    h('h1', { text: 'Open the dashboard from your terminal' }),
    h('p', { class: 'soft', text: msg }),
    h('p', { class: 'soft', text: 'Each dashboard link works once, in one browser, and only while setu dashboard is running. Run it again in a terminal for a fresh link:' }),
    h('code', { class: 'cmd', text: 'setu dashboard' })));
}

async function signOut() {
  try { await api('POST', '/api/logout', {}); } catch { /* signed out either way */ }
  app.csrf = null;
  gate('You signed out.');
}

const THEMES = ['auto', 'dark', 'light'];
function initTheme() {
  let theme = 'auto';
  try { theme = localStorage.getItem('setu-dashboard-theme') || 'auto'; } catch { /* storage off */ }
  applyTheme(theme);
  document.getElementById('theme').addEventListener('click', () => {
    const next = THEMES[(THEMES.indexOf(document.documentElement.dataset.theme || 'auto') + 1) % THEMES.length];
    applyTheme(next);
    try { localStorage.setItem('setu-dashboard-theme', next); } catch { /* storage off */ }
  });
}
function applyTheme(theme) {
  if (theme === 'auto') delete document.documentElement.dataset.theme;
  else document.documentElement.dataset.theme = theme;
  document.getElementById('theme').textContent = `Theme: ${theme}`;
}

boot();
