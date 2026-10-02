// setu web client: joins a shared terminal peer-to-peer from the browser.
// Mirrors internal/client in Go. Secrets only ever live in the URL fragment
// and in memory; the page talks to nostr relays (ciphertext only) and, via
// WebRTC, directly to the host.
import { Terminal } from '../vendor/xterm.mjs';
import { FitAddon } from '../vendor/addon-fit.mjs';
import * as C from './crypto.mjs';
import * as N from './nostr.mjs';

const PROTO_VERSION = 1;
const GATHER_TIMEOUT = 6000;
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
  step(msg, 'err');
  setStatus('error', 'err');
  if (state.ready) termNotice(`\r\n\x1b[31m[setu] ${msg}\x1b[0m\r\n`);
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

async function answerOffer(offer) {
  const pc = new RTCPeerConnection({ iceServers: validIce(offer.ice) });
  state.pc = pc;
  pc.ondatachannel = (e) => {
    if (e.channel.label !== 'setu') { e.channel.close(); return; }
    const dc = e.channel;
    dc.binaryType = 'arraybuffer';
    state.dc = dc;
    dc.onmessage = onMessage;
    dc.onclose = () => { if (state.ready) termNotice('\r\n\x1b[33m[setu] connection closed\x1b[0m\r\n'); setStatus('disconnected', 'err'); release(); };
  };
  pc.onconnectionstatechange = () => {
    log('pc ' + pc.connectionState);
    if (pc.connectionState === 'connected') step('Peer-to-peer connection established (DTLS encrypted)', 'ok');
    if (pc.connectionState === 'failed') fail('WebRTC connection failed. Both sides may be behind strict NATs; the host can add --turn.');
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
    if (state.ready) state.term.write(bytes);
    else state.pending.push(bytes);
    return;
  }
  let m;
  try { m = JSON.parse(e.data); } catch { return; }
  switch (m.t) {
    case 'hello': return onHello(m);
    case 'ready': return onReady(m);
    case 'error': return fail('Host: ' + m.msg);
    case 'exit':
      termNotice(`\r\n\x1b[33m[setu] remote command exited (code ${m.code ?? 0})\x1b[0m\r\n`);
      setStatus('ended', 'warn');
      return;
    case 'info': return termNotice(`\r\n[setu] ${m.msg}\r\n`);
  }
}

async function onHello(m) {
  if (m.v !== PROTO_VERSION) return fail(`Protocol mismatch (host v${m.v}, page v${PROTO_VERSION}). Update setu or the page.`);
  const localFP = C.fingerprint(state.pc.localDescription.sdp);
  const remoteFP = C.fingerprint(state.pc.remoteDescription.sdp);
  const code = await C.sas(localFP, remoteFP);
  $('sas').textContent = code;
  $('sas-box').hidden = false;
  step(`Verification code ${code} — the host terminal prints the same code when you join.`, 'ok');
  const nonce = C.b64Decode(m.nonce);
  if (m.auth === 'password') {
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
        sendCtl({ t: 'auth', proof: C.b64Encode(proof) });
      } catch (err) {
        fail(String(err.message || err));
      }
    };
  } else {
    sendCtl({ t: 'auth' });
  }
}

function onReady(m) {
  state.role = m.role;
  state.ready = true;
  setStatus(m.role === 'control' ? 'connected · control' : 'connected · view only', 'ok');
  $('title').textContent = `${m.cmd || 'terminal'} @ ${m.host || 'host'}`;
  document.title = `setu · ${m.cmd || 'terminal'}`;
  show('screen-term');
  setupTerminal();
  for (const b of state.pending) state.term.write(b);
  state.pending = [];
  if (state.role === 'control') sendCtl({ t: 'resize', cols: state.term.cols, rows: state.term.rows });
  else { $('keys').hidden = true; $('compose').hidden = true; state.term.options.disableStdin = true; }
  state.term.focus();
  if (state.pool) setTimeout(() => state.pool.close(), 3000); // signaling done
  if (navigator.wakeLock) navigator.wakeLock.request('screen').then((l) => (state.wakeLock = l)).catch(() => {});
  window.addEventListener('beforeunload', beforeUnload);
}

function beforeUnload(e) {
  if (state.ready) { e.preventDefault(); e.returnValue = ''; }
}
function release() {
  state.ready = false;
  window.removeEventListener('beforeunload', beforeUnload);
  if (state.wakeLock) state.wakeLock.release().catch(() => {});
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
async function joinNostr(inv) {
  const room = await C.deriveRoom(inv.secret);
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
      if (m.type !== 'offer' || gotOffer) return;
      gotOffer = true;
      clearInterval(timer);
      step('Host answered; negotiating a direct connection…');
      try {
        const sdp = await answerOffer(m);
        await send({ type: 'answer', sid, sdp });
        setTimeout(() => send({ type: 'answer', sid, sdp }), 1000); // ephemeral events are best effort
        resolve();
      } catch (err) { reject(err); }
    });
  });

  step(`Contacting host through ${relays.length} nostr relays (they only see ciphertext)…`);
  await pool.waitConnected(20000);
  await send({ type: 'hello', sid });
  timer = setInterval(() => send({ type: 'hello', sid }), 2000);
  const timeout = new Promise((_, reject) => setTimeout(() => reject(new Error('The host did not answer. Is `setu share` still running and the link current?')), 90000));
  try {
    await Promise.race([done, timeout]);
  } finally {
    clearInterval(timer);
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
async function start(input) {
  show('screen-connect');
  setStatus('connecting…');
  try {
    const inv = C.parseInvite(input);
    if (inv.secret) await joinNostr(inv);
    else await joinManual(inv.offer);
  } catch (err) {
    fail(String(err.message || err));
  }
}

function boot() {
  $('copy-answer').addEventListener('click', async () => {
    try { await navigator.clipboard.writeText($('answer').value); $('copy-answer').textContent = 'Copied'; }
    catch { $('answer').select(); }
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
    show('screen-home');
  }
}

boot();
