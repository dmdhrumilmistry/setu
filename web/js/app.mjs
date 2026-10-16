// setu web client: joins a shared terminal peer-to-peer from the browser.
// Mirrors internal/client in Go. Secrets only ever live in the URL fragment
// and in memory; the page talks to nostr relays (ciphertext only) and, via
// WebRTC, directly to the host.
import { Terminal } from '../vendor/xterm.mjs';
import { FitAddon } from '../vendor/addon-fit.mjs';
import * as C from './crypto.mjs';
import * as N from './nostr.mjs';
import { deviceKey, forgetDevice, signDevice, deviceFingerprint } from './device.mjs';
import { createDesktop, toast } from './desktop.mjs';
import { uploadFile, uploadAll, dropTarget } from './upload.mjs';
import { keepEnabled, setKeepEnabled, keepSession, touchKept, loadKept, clearKept } from './keep.mjs';

const PROTO_VERSION = 1;
const GATHER_TIMEOUT = 6000;
// Reconnecting (hosts that hand out resume tokens; see internal/host/resume.go).
const ERR_RESUME = 'this session can no longer be resumed'; // proto.ErrResume
const PING_EVERY = 5000;
const DEAD_AFTER = 15000; // no message from the host for this long, while visible
const RESUME_WINDOW = 10 * 60 * 1000; // the host's resume grace period
const ATTEMPT_TIMEOUT = 30000; // one reconnect attempt, signaling to "ready"
const MAX_BACKOFF = 30000;
const $ = (id) => document.getElementById(id);

// Refuse to run inside a frame (clickjacking / key capture by an embedder).
if (window.top !== window.self) {
  document.body.textContent = 'setu refuses to run inside a frame.';
  throw new Error('framed');
}

const state = {
  pc: null,
  dc: null,
  pool: null,
  role: '',
  ready: false,
  term: null,
  fit: null,
  ctrlArmed: false,
  pending: [],
  wakeLock: null,
  desk: null, // remote desktop viewer, for desktop shares
  screenEarly: [], // screen updates that arrived before "ready"
  // Connection lifecycle. gen counts connections, so events from a replaced
  // one are ignored. The invite secret and resume token stay in memory only.
  gen: 0,
  invite: null, // parsed nostr invite, kept to reconnect
  resume: null, // resume token from the last "ready" (Uint8Array)
  pos: null, // terminal output stream position, for resuming without gaps
  ended: false, // the session is over: never reconnect
  connected: false, // a connection reached "ready" at least once
  lostAt: 0, // when the current outage began (0: connected)
  attempt: 0,
  retryTimer: null,
  attemptTimer: null,
  lastRx: 0,
  pageHidden: false,
  paused: false, // a reconnect waits for the page to be visible again
  restoring: false, // resuming a session kept across a reload of this tab
};

// ---------- UI helpers ----------
function show(id) {
  for (const s of document.querySelectorAll('[data-screen]')) s.hidden = s.id !== id;
}
function step(text, kind = 'info') {
  const li = document.createElement('li');
  li.textContent = text;
  li.className = kind;
  $('steps').appendChild(li);
}
function setStatus(text, kind = 'info') {
  const el = $('status');
  el.textContent = text;
  el.dataset.kind = kind;
}
function fail(msg) {
  state.ended = true;
  stopReconnect();
  clearKept();
  step(msg, 'err');
  setStatus('error', 'err');
  if (state.desk) state.desk.notice(msg);
  else if (state.connected) termNotice(`\r\n\x1b[31m[setu] ${msg}\x1b[0m\r\n`);
}
function termNotice(s) {
  if (state.term) state.term.write(s);
}
function log(msg) {
  if (new URLSearchParams(location.search).has('debug')) console.log('[setu]', msg);
}

// ---------- WebRTC ----------
function waitGathered(pc) {
  if (pc.iceGatheringState === 'complete') return Promise.resolve();
  return new Promise((resolve) => {
    const t = setTimeout(resolve, GATHER_TIMEOUT);
    pc.addEventListener('icegatheringstatechange', () => {
      if (pc.iceGatheringState === 'complete') { clearTimeout(t); resolve(); }
    });
  });
}

function validIce(list) {
  return (list || []).filter((s) => Array.isArray(s.urls) && s.urls.every((u) => /^(stun|turns?):/.test(u)));
}

async function answerOffer(offer, gen = state.gen) {
  const pc = new RTCPeerConnection({ iceServers: validIce(offer.ice) });
  const stale = () => gen !== state.gen;
  if (stale()) { pc.close(); throw new Error('superseded'); }
  state.pc = pc;
  pc.ondatachannel = (e) => {
    if (stale()) { e.channel.close(); return; }
    if (e.channel.label === 'setu-screen') {
      e.channel.binaryType = 'arraybuffer';
      e.channel.onmessage = (m) => { if (state.desk) state.desk.frame(m.data); else state.screenEarly.push(m.data); };
      return;
    }
    if (e.channel.label !== 'setu') { e.channel.close(); return; }
    const dc = e.channel;
    dc.binaryType = 'arraybuffer';
    state.dc = dc;
    dc.onmessage = (m) => { if (!stale()) { state.lastRx = Date.now(); onMessage(m); } };
    dc.onclose = () => { if (!stale()) lost('the connection closed'); };
  };
  pc.onconnectionstatechange = () => {
    if (stale()) return;
    log('pc ' + pc.connectionState);
    switch (pc.connectionState) {
      case 'connected':
        if (!state.connected) step('Peer-to-peer connection established (DTLS encrypted)', 'ok');
        break;
      case 'disconnected':
        // Often transient (a network hand-over): ICE may recover by itself.
        if (state.ready) { setStatus('connection unstable…', 'warn'); probe(); }
        break;
      case 'failed':
        if (!state.connected && !state.lostAt) fail('WebRTC connection failed. Both sides may be behind strict NATs; the host can add --turn.');
        else lost('the network connection failed');
        break;
    }
  };
  await pc.setRemoteDescription({ type: 'offer', sdp: offer.sdp });
  await pc.setLocalDescription(await pc.createAnswer());
  await waitGathered(pc);
  return pc.localDescription.sdp;
}

// ---------- data channel protocol ----------
function sendCtl(obj) {
  if (state.dc && state.dc.readyState === 'open') state.dc.send(JSON.stringify(obj));
}
const MAX_FRAME = 16 * 1024; // host drops input frames > 64 KiB
function sendInput(str) {
  if (!state.ready || state.role !== 'control' || !state.dc || state.dc.readyState !== 'open') return;
  const bytes = C.utf8(str);
  for (let i = 0; i < bytes.length; i += MAX_FRAME) state.dc.send(bytes.subarray(i, i + MAX_FRAME));
}

async function onMessage(e) {
  if (typeof e.data !== 'string') {
    const bytes = new Uint8Array(e.data);
    if (state.pos !== null) state.pos += bytes.length;
    if (state.ready) state.term.write(bytes);
    else state.pending.push(bytes);
    return;
  }
  let m;
  try { m = JSON.parse(e.data); } catch { return; }
  switch (m.t) {
    case 'hello': return onHello(m);
    case 'ready': return onReady(m);
    case 'error':
      if (m.msg === ERR_RESUME && state.resume) return resumeRejected();
      return fail('Host: ' + m.msg);
    case 'pong': return;
    case 'exit':
      state.ended = true;
      stopReconnect();
      clearKept();
      if (state.desk) state.desk.ended('The host ended the remote desktop session.');
      else termNotice(`\r\n\x1b[33m[setu] remote command exited (code ${m.code ?? 0})\x1b[0m\r\n`);
      setStatus('ended', 'warn');
      return;
    case 'info':
      if (state.desk) return state.desk.notice(m.msg);
      return termNotice(`\r\n[setu] ${m.msg}\r\n`);
    case 'screen': return state.desk?.onScreen(m);
    case 'clipboard': return state.desk?.onClipboard(m);
  }
}

// A random id kept in this browser, so the host's audit log can tell a
// returning device from a new one. Not derived from any hardware id.
function deviceId() {
  try {
    let id = localStorage.getItem('setu-device-id');
    if (!/^[0-9a-f]{32}$/.test(id || '')) {
      id = Array.from(crypto.getRandomValues(new Uint8Array(16)), (b) => b.toString(16).padStart(2, '0')).join('');
      localStorage.setItem('setu-device-id', id);
    }
    return id;
  } catch {
    return undefined; // storage blocked (private mode): still join
  }
}

// clientInfo describes this device to the host's audit log. It travels only
// inside the encrypted data channel. Everything is best effort.
async function clientInfo() {
  const info = {
    app: 'setu-web',
    device_id: deviceId(),
    user_agent: navigator.userAgent,
    language: navigator.language,
    screen: `${screen.width}x${screen.height}@${window.devicePixelRatio || 1}`,
  };
  try { info.timezone = Intl.DateTimeFormat().resolvedOptions().timeZone; } catch { /* ignore */ }
  const ua = navigator.userAgentData;
  if (ua) {
    info.os = ua.platform;
    info.mobile = !!ua.mobile;
    try {
      const hints = await Promise.race([
        ua.getHighEntropyValues(['model', 'platformVersion', 'architecture']),
        new Promise((_, reject) => setTimeout(() => reject(new Error('timeout')), 500)),
      ]);
      info.model = hints.model || undefined;
      info.os_version = hints.platformVersion || undefined;
      info.arch = hints.architecture || undefined;
    } catch { /* hints unavailable */ }
  } else {
    info.os = navigator.platform;
    info.mobile = /Mobi|Android|iPhone|iPad/.test(navigator.userAgent);
  }
  return info;
}

async function onHello(m) {
  if (m.v !== PROTO_VERSION) return fail(`Protocol mismatch (host v${m.v}, page v${PROTO_VERSION}). Update setu or the page.`);
  const localFP = C.fingerprint(state.pc.localDescription.sdp);
  const remoteFP = C.fingerprint(state.pc.remoteDescription.sdp);
  const code = await C.sas(localFP, remoteFP);
  $('sas').textContent = code;
  $('sas-box').hidden = false;
  if (state.resume) termNotice(`\x1b[2m[setu] verification code ${code}\x1b[0m\r\n`);
  step(`Verification code ${code} — the host terminal prints the same code when you join.`, 'ok');
  const nonce = C.b64Decode(m.nonce);
  const client = await clientInfo();
  // Device key: proves which device this is, so a host can approve it
  // automatically. Best effort: joining works without it.
  let dev = null;
  try {
    dev = await deviceKey(rememberDevice());
    step(`This device: ${await deviceFingerprint(dev.publicRaw)} (${rememberDevice() ? 'remembered' : 'one-time key'})`);
  } catch { /* WebCrypto ECDSA unavailable */ }
  const withDevice = async (msg) => {
    if (dev) {
      msg.device_key = C.b64Encode(dev.publicRaw);
      msg.device_sig = C.b64Encode(await signDevice(dev, nonce, localFP, remoteFP));
    }
    return msg;
  };
  if (state.resume) {
    // Reconnecting: the resume token replaces the password and approval.
    sendCtl(await withDevice({ t: 'auth', resume: C.b64Encode(state.resume), pos: state.pos ?? undefined, client }));
    step('Resuming the session…');
  } else if (m.auth === 'password') {
    setStatus('password required', 'warn');
    $('pw-form').hidden = false;
    $('pw').focus();
    $('pw-form').onsubmit = async (ev) => {
      ev.preventDefault();
      const pw = $('pw').value;
      $('pw').value = '';
      $('pw-form').hidden = true;
      setStatus('verifying…');
      try {
        const key = await C.passwordKey(pw, C.b64Decode(m.salt), m.iter);
        const proof = await C.authProof(key, nonce, localFP, remoteFP);
        sendCtl(await withDevice({ t: 'auth', proof: C.b64Encode(proof), client }));
        step('Authenticated. If the host requires approval, it is being asked now...');
      } catch (err) {
        fail(String(err.message || err));
      }
    };
  } else {
    sendCtl(await withDevice({ t: 'auth', client }));
    step('If the host requires approval, it is being asked now...');
  }
}

function onReady(m) {
  const reconnected = state.connected;
  state.role = m.role;
  state.ready = true;
  state.connected = true;
  state.lostAt = 0;
  state.attempt = 0;
  clearTimeout(state.attemptTimer);
  state.resume = m.resume ? C.b64Decode(m.resume) : null;
  state.pos = typeof m.pos === 'number' ? m.pos : null;
  state.lastRx = Date.now();
  state.paused = false;
  $('reconnect').hidden = true;
  $('leave').hidden = !state.invite;
  keepSession(state.invite, state.resume);
  sendVisibility();
  const trusted = m.approval === 'trusted' ? ' · trusted device' : '';
  setStatus((m.role === 'control' ? 'connected · control' : 'connected · view only') + trusted, 'ok');
  if (trusted) $('status').title = 'The host recognised this device and approved it automatically';
  $('title').textContent = `${m.cmd || 'terminal'} @ ${m.host || 'host'}`;
  document.title = `setu · ${m.cmd || 'terminal'}`;
  // Control clients may upload files when the host allows it.
  const upload = m.upload ? (file, onProgress) => uploadFile(state.pc, file, onProgress) : null;
  if (upload) $('upload-input').onchange = (e) => { uploadAll([...e.target.files], upload, toast); e.target.value = ''; };
  if (m.kind === 'desktop') {
    show('screen-desk');
    if (!state.desk) state.desk = createDesktop({ send: sendCtl, control: state.role === 'control', hostOS: m.os, upload });
    else state.desk.notice('Reconnected');
    for (const b of state.screenEarly) state.desk.frame(b);
    state.screenEarly = [];
  } else {
    show('screen-term');
    if (!state.term) setupTerminal();
    else if (!m.resumed) state.term.reset(); // the host replays its whole buffer
    if (state.restoring) termNotice('\x1b[32m[setu] session restored after this tab reloaded\x1b[0m\r\n');
    else if (reconnected) termNotice(m.resumed ? '\x1b[32m[setu] reconnected\x1b[0m\r\n' : '\x1b[33m[setu] reconnected; some output was lost while disconnected\x1b[0m\r\n');
    for (const b of state.pending) state.term.write(b);
    state.pending = [];
    if (state.role === 'control') sendCtl({ t: 'resize', cols: state.term.cols, rows: state.term.rows });
    else { $('keys').hidden = true; $('compose').hidden = true; state.term.options.disableStdin = true; }
    if (upload && !state.dropBound) { // once: onReady runs again on every reconnect
      state.dropBound = true;
      $('key-upload').hidden = false;
      dropTarget($('term'), (files) => uploadAll(files, upload, toast));
    }
    state.term.focus();
  }
  state.restoring = false;
  const pool = state.pool;
  if (pool) setTimeout(() => pool.close(), 3000); // signaling done
  wakeLock();
  window.addEventListener('beforeunload', beforeUnload);
}

// ---------- reconnecting ----------
// A connection that drops is resumed with the token from "ready": new
// signaling and a new peer connection, but the same session, with no password
// or approval prompt, and the terminal continues where it stopped. Attempts
// back off exponentially with jitter until the host's resume window closes.
function lost(reason) {
  if (state.ended) return release();
  if (state.pageHidden) return;
  const gen = state.gen;
  if (state.lostGen === gen) return; // already handled for this connection
  state.lostGen = gen;
  state.ready = false;
  clearTimeout(state.attemptTimer);
  try { state.pc?.close(); } catch { /* ignore */ }
  if (!state.connected && !state.lostAt) return fail('Disconnected before the session started (' + reason + ').');
  if (!state.resume || !state.invite) {
    // An older host, or a --manual share: nothing to resume with.
    state.ended = true;
    notice(`connection lost (${reason})`, 'err');
    setStatus('disconnected', 'err');
    release();
    if (state.invite) $('reconnect').hidden = false;
    return;
  }
  if (!state.lostAt) {
    state.lostAt = Date.now();
    state.attempt = 0;
    notice(`connection lost (${reason}); reconnecting…`, 'warn');
  }
  scheduleReconnect();
}

function scheduleReconnect() {
  clearTimeout(state.retryTimer);
  if (state.ended) return;
  if (document.visibilityState === 'hidden') {
    // Nobody is looking, and background timers are throttled anyway: wait
    // for the user to come back. The host keeps the session for an hour.
    state.paused = true;
    setStatus('disconnected · reconnects when you return', 'warn');
    return;
  }
  if (Date.now() - state.lostAt > RESUME_WINDOW) return giveUp('Could not reconnect: the host has not answered for 10 minutes.');
  // Full jitter, so the clients of one host do not retry in step.
  const base = Math.min(MAX_BACKOFF, 1000 * 2 ** state.attempt);
  const delay = state.attempt === 0 ? 250 : base / 2 + Math.random() * (base / 2);
  setStatus(navigator.onLine === false ? 'offline · waiting for network' : `reconnecting… (attempt ${state.attempt + 1})`, 'warn');
  state.retryTimer = setTimeout(reconnect, navigator.onLine === false ? MAX_BACKOFF : delay);
}

async function reconnect() {
  if (state.ended || state.pageHidden) return;
  clearTimeout(state.retryTimer);
  const gen = ++state.gen;
  state.attempt++;
  try { state.pc?.close(); } catch { /* ignore */ }
  if (state.pool) state.pool.close();
  state.pc = state.dc = null;
  state.attemptTimer = setTimeout(() => { if (gen === state.gen) lost('the reconnect attempt timed out'); }, ATTEMPT_TIMEOUT);
  try {
    await joinNostr(state.invite, gen, ATTEMPT_TIMEOUT);
  } catch (err) {
    if (gen !== state.gen || state.ended) return;
    const msg = String(err.message || err);
    if (/^Host rejected/.test(msg)) return giveUp(msg);
    log('reconnect attempt failed: ' + msg);
    lost(msg);
  }
}

// The host no longer knows our session (it expired, or the share restarted):
// join afresh, which may ask for the password again.
function resumeRejected() {
  clearKept();
  state.restoring = false;
  state.resume = null;
  state.pos = null;
  state.gen++;
  clearTimeout(state.attemptTimer);
  notice('the session could not be resumed; joining again…', 'warn');
  show('screen-connect');
  start(null);
}

function giveUp(msg) {
  state.ended = true;
  stopReconnect();
  clearKept();
  step(msg, 'err');
  notice(msg, 'err');
  setStatus('disconnected', 'err');
  release();
  if (state.invite) $('reconnect').hidden = false;
}

function stopReconnect() {
  clearTimeout(state.retryTimer);
  clearTimeout(state.attemptTimer);
  state.retryTimer = state.attemptTimer = null;
}

// Retry at once instead of waiting out the backoff (the network is back, or
// the user returned to the tab).
function kick() {
  if (!state.lostAt || state.ended || state.pageHidden || document.visibilityState === 'hidden') return;
  if (!state.retryTimer && !state.paused) return; // an attempt is running
  if (state.paused) {
    // Count the retry window from now: the time spent away does not count.
    state.paused = false;
    state.lostAt = Date.now();
    state.attempt = 0;
  }
  clearTimeout(state.retryTimer);
  state.retryTimer = null;
  reconnect();
}

// Tell the host whether this page is in the background, so it does not
// expect pings that a throttled or frozen tab cannot send, and keeps the
// session resumable for longer if the browser drops the connection.
function sendVisibility() {
  if (state.ready && state.resume) sendCtl({ t: 'visibility', hidden: document.visibilityState === 'hidden' });
}

// Restore a session kept across a reload of this tab (see keep.mjs).
function restoreKept(k) {
  state.invite = k.inv;
  state.resume = k.token;
  state.pos = null; // the screen is gone: replay everything the host has
  state.connected = true;
  state.restoring = true;
  state.lostAt = Date.now();
  show('screen-connect');
  step('Restoring the session this tab was in…');
  $('leave').hidden = false;
  reconnect();
}

// Leave the session for good: forget the invite and token kept for this
// tab and start over from a clean page.
function leave() {
  state.ended = true;
  stopReconnect();
  clearKept();
  release();
  try { state.pc?.close(); } catch { /* ignore */ }
  if (state.pool) state.pool.close();
  location.replace(location.pathname + location.search);
}

// Check a connection that may have died silently (after a network change,
// or when the page returns from the background): the host must answer a
// ping within a few seconds.
function probe() {
  if (!state.ready || !state.resume) return;
  state.lastRx = Math.min(state.lastRx, Date.now() - (DEAD_AFTER - 5000));
  sendCtl({ t: 'ping' });
}

function notice(msg, kind) {
  const color = kind === 'err' ? 31 : 33;
  if (state.desk && kind === 'err') state.desk.ended(msg);
  else if (state.desk) state.desk.notice(msg);
  else termNotice(`\r\n\x1b[${color}m[setu] ${msg}\x1b[0m\r\n`);
}

// Liveness: hosts that issue resume tokens answer pings (and drop a pinging
// client that goes quiet for long). Browsers slow timers in background tabs,
// so silence only counts against the host while the page is visible.
setInterval(() => {
  if (state.ready && state.resume) sendCtl({ t: 'ping' });
}, PING_EVERY);
setInterval(() => {
  if (state.ready && state.resume && document.visibilityState === 'visible' && Date.now() - state.lastRx > DEAD_AFTER) {
    lost('no response from the host');
  }
}, 1000);
document.addEventListener('visibilitychange', () => {
  sendVisibility();
  if (document.visibilityState !== 'visible') return touchKept();
  // Browsers drop wake locks in the background.
  if (state.ready) wakeLock();
  probe();
  kick();
});
window.addEventListener('online', () => { probe(); kick(); });
navigator.connection?.addEventListener?.('change', () => probe());
// Close the connection when the tab goes away, so the host sees the
// disconnect (and releases any held keys) at once, not after ICE times out.
// A page restored from the back/forward cache resumes.
// The browser may freeze or discard a background tab without another event.
document.addEventListener('freeze', () => touchKept());
window.addEventListener('pagehide', () => {
  touchKept();
  state.pageHidden = true;
  stopReconnect();
  if (state.pc) state.pc.close();
});
window.addEventListener('pageshow', (e) => {
  if (!e.persisted) return;
  state.pageHidden = false;
  if (state.connected && !state.ended) {
    state.lostGen = -1;
    lost('the page was suspended');
  }
});

function beforeUnload(e) {
  if (state.ready) { e.preventDefault(); e.returnValue = ''; }
}
// Keep the screen on while connected (browsers drop the lock in the background).
function wakeLock() {
  if (!navigator.wakeLock || state.wakeLock) return;
  navigator.wakeLock.request('screen').then((l) => {
    state.wakeLock = l;
    l.addEventListener('release', () => { if (state.wakeLock === l) state.wakeLock = null; });
  }).catch(() => {});
}

function release() {
  state.ready = false;
  window.removeEventListener('beforeunload', beforeUnload);
  if (state.wakeLock) state.wakeLock.release().catch(() => {});
  state.wakeLock = null;
}

// ---------- terminal ----------
function setupTerminal() {
  const small = window.matchMedia('(max-width: 640px)').matches;
  const term = new Terminal({
    cursorBlink: true,
    fontSize: small ? 12 : 14,
    fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace',
    scrollback: 5000,
    macOptionIsMeta: true,
    theme: { background: '#0b0e14', foreground: '#d8dee9', cursor: '#7dd3fc' },
  });
  const fit = new FitAddon();
  term.loadAddon(fit);
  term.open($('term'));
  state.term = term;
  state.fit = fit;
  const refit = () => { try { fit.fit(); } catch {} };
  refit();
  new ResizeObserver(refit).observe($('term'));
  if (window.visualViewport) window.visualViewport.addEventListener('resize', refit);
  term.onResize(({ cols, rows }) => { if (state.role === 'control') sendCtl({ t: 'resize', cols, rows }); });
  term.onData((d) => {
    if (state.ctrlArmed && d.length === 1) {
      const c = d.toUpperCase().charCodeAt(0);
      if (c >= 64 && c <= 95) d = String.fromCharCode(c & 0x1f);
      disarmCtrl();
    }
    sendInput(d);
  });
  setupKeys();
}

const KEYS = {
  esc: '\x1b', tab: '\t', stab: '\x1b[Z', up: '\x1b[A', down: '\x1b[B', right: '\x1b[C', left: '\x1b[D',
  cc: '\x03', cd: '\x04', enter: '\r',
};
function disarmCtrl() {
  state.ctrlArmed = false;
  $('key-ctrl').classList.remove('armed');
}
function setupKeys() {
  for (const b of document.querySelectorAll('#keys button[data-key]')) {
    b.addEventListener('click', (e) => {
      e.preventDefault();
      const k = b.dataset.key;
      if (k === 'ctrl') {
        state.ctrlArmed = !state.ctrlArmed;
        b.classList.toggle('armed', state.ctrlArmed);
      } else if (k === 'upload') {
        $('upload-input').click();
      } else if (k === 'compose') {
        $('compose').hidden = !$('compose').hidden;
        if (!$('compose').hidden) $('compose-text').focus();
      } else {
        sendInput(KEYS[k]);
      }
      if (k !== 'compose') state.term.focus();
    });
  }
  $('compose').addEventListener('submit', (e) => {
    e.preventDefault();
    const text = $('compose-text').value;
    if (!text) return;
    // Multi-line text goes in as a bracketed paste so TUIs (Claude Code, vim…)
    // treat newlines as content rather than "submit".
    sendInput(text.includes('\n') ? `\x1b[200~${text}\x1b[201~` : text);
    if ($('compose-enter').checked) setTimeout(() => sendInput('\r'), 30);
    $('compose-text').value = '';
    state.term.focus();
  });
  $('compose-text').addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); $('compose').requestSubmit(); }
  });
}

// ---------- signaling: nostr ----------
async function joinNostr(inv, gen = state.gen, timeoutMs = 90000) {
  const room = await C.deriveRoom(inv.secret);
  const resume = state.resume ? await C.resumeId(state.resume) : undefined;
  const kp = N.generateKey();
  const relays = inv.relays.length ? inv.relays : N.DEFAULT_RELAYS;
  const sid = C.hex(C.randomBytes(8));
  const pool = new N.Pool(relays, log);
  state.pool = pool;

  const send = async (m) => {
    m.ts = Math.floor(Date.now() / 1000);
    const ct = await C.seal(room, C.utf8(JSON.stringify(m)), C.signalAAD(room.id, kp.pub, room.id));
    const ev = await N.signEvent(kp, { kind: N.KIND_SIGNAL, tags: [['p', room.id]], content: ct });
    return pool.publish(ev);
  };

  let gotOffer = false;
  let timer = null;
  const done = new Promise((resolve, reject) => {
    pool.subscribe('setu-' + C.hex(C.randomBytes(4)), { kinds: [N.KIND_SIGNAL], '#p': [kp.pub], since: Math.floor(Date.now() / 1000) - 60 }, async (ev) => {
      let m;
      try {
        m = JSON.parse(C.fromUtf8(await C.open(room, ev.content, C.signalAAD(room.id, ev.pubkey, kp.pub))));
      } catch { return; }
      if (m.sid !== sid) return;
      if (m.type === 'reject') return reject(new Error('Host rejected the connection: ' + m.reason));
      if (m.type !== 'offer' || gotOffer || gen !== state.gen) return;
      gotOffer = true;
      clearInterval(timer);
      step('Host answered; negotiating a direct connection…');
      try {
        const sdp = await answerOffer(m, gen);
        await send({ type: 'answer', sid, sdp });
        setTimeout(() => send({ type: 'answer', sid, sdp }), 1000); // ephemeral events are best effort
        resolve();
      } catch (err) { reject(err); }
    });
  });

  step(`Contacting host through ${relays.length} nostr relays (they only see ciphertext)…`);
  let timeoutTimer;
  const timeout = new Promise((_, reject) => {
    timeoutTimer = setTimeout(() => reject(new Error('The host did not answer. Is `setu share` still running and the link current?')), timeoutMs);
  });
  try {
    await Promise.race([pool.waitConnected(Math.min(20000, timeoutMs)), timeout]);
    if (gen !== state.gen) throw new Error('superseded');
    const hello = { type: 'hello', sid, resume };
    await send({ ...hello });
    timer = setInterval(() => { if (gen === state.gen) send({ ...hello }); }, 2000);
    await Promise.race([done, timeout]);
  } catch (err) {
    if (!gotOffer) pool.close();
    throw err;
  } finally {
    clearInterval(timer);
    clearTimeout(timeoutTimer);
  }
}

// ---------- signaling: manual copy/paste ----------
async function joinManual(code) {
  const offer = await C.decodeCode(code);
  if (offer.type !== 'offer' || !offer.sdp) throw new Error('That code is not a setu offer.');
  step('Offer decoded; preparing answer…');
  const sdp = await answerOffer(offer);
  const answer = await C.encodeCode({ type: 'answer', sid: offer.sid, sdp, ts: Math.floor(Date.now() / 1000) });
  $('answer').value = answer;
  $('answer-box').hidden = false;
  setStatus('waiting for host', 'warn');
  step('Send the answer code back to the host; it pastes it into `setu share --manual`.');
}

// ---------- boot ----------
// start joins with an invite link or offer code; null joins the remembered
// invite again (after a session could not be resumed).
async function start(input) {
  show('screen-connect');
  setStatus('connecting…');
  state.ended = false;
  state.lostAt = 0;
  const gen = ++state.gen;
  try {
    const inv = input === null ? state.invite : C.parseInvite(input);
    if (inv.secret) {
      state.invite = inv;
      await joinNostr(inv, gen);
    } else {
      state.invite = null; // manual offers are single use
      await joinManual(inv.offer);
    }
  } catch (err) {
    if (gen === state.gen) fail(String(err.message || err));
  }
}

// The remember-device preference (default on) lives in localStorage.
function rememberDevice() {
  try { return localStorage.getItem('setu-remember-device') !== '0'; } catch { return false; }
}

async function showDevice() {
  try {
    const k = await deviceKey(rememberDevice());
    $('device-fp').textContent = (await deviceFingerprint(k.publicRaw)) + (rememberDevice() ? '' : ' (one-time key)');
  } catch {
    $('device-fp').textContent = 'unavailable in this browser';
  }
}

function boot() {
  $('remember-device').checked = rememberDevice();
  $('remember-device').addEventListener('change', async (e) => {
    try { localStorage.setItem('setu-remember-device', e.target.checked ? '1' : '0'); } catch { /* ignore */ }
    if (!e.target.checked) await forgetDevice(); // unticking deletes the stored key
    showDevice();
  });
  $('forget-device').addEventListener('click', async () => {
    await forgetDevice();
    await showDevice();
    $('forget-device').textContent = 'Forgotten: new key created';
  });
  $('copy-answer').addEventListener('click', async () => {
    try { await navigator.clipboard.writeText($('answer').value); $('copy-answer').textContent = 'Copied'; }
    catch { $('answer').select(); }
  });
  $('keep-session').checked = keepEnabled();
  $('keep-session').addEventListener('change', (e) => setKeepEnabled(e.target.checked));
  $('leave').addEventListener('click', leave);
  $('reconnect').addEventListener('click', () => {
    $('reconnect').hidden = true;
    state.resume = null;
    state.connected = false;
    start(null);
  });
  $('join-form').addEventListener('submit', (e) => {
    e.preventDefault();
    const v = $('invite').value;
    $('invite').value = '';
    start(v);
  });
  const frag = location.hash.slice(1);
  if (frag) {
    // Drop the secret from the address bar and the history entry.
    history.replaceState(null, '', location.pathname + location.search);
    start(frag);
  } else {
    const kept = loadKept();
    if (kept) return restoreKept(kept);
    show('screen-home');
    showDevice();
  }
}

boot();
