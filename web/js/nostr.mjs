// Minimal nostr client mirroring internal/nostr (ephemeral signaling only).
import { schnorr } from '../vendor/noble-secp256k1.mjs';
import { hex, unhex, sha256, utf8 } from './crypto.mjs';

export const KIND_SIGNAL = 21210;
export const DEFAULT_RELAYS = ['wss://relay.damus.io', 'wss://nos.lol', 'wss://relay.primal.net', 'wss://nostr.mom'];

export function generateKey() {
  const sk = schnorr.utils.randomSecretKey();
  return { sk, pub: hex(schnorr.getPublicKey(sk)) };
}

async function eventHash(ev) {
  return sha256(utf8(JSON.stringify([0, ev.pubkey, ev.created_at, ev.kind, ev.tags, ev.content])));
}

export async function signEvent(kp, ev) {
  ev.pubkey = kp.pub;
  ev.created_at = ev.created_at || Math.floor(Date.now() / 1000);
  ev.tags = ev.tags || [];
  const h = await eventHash(ev);
  ev.id = hex(h);
  ev.sig = hex(schnorr.sign(h, kp.sk));
  return ev;
}

export async function verifyEvent(ev) {
  try {
    const h = await eventHash(ev);
    if (hex(h) !== ev.id) return false;
    return schnorr.verify(unhex(ev.sig), h, unhex(ev.pubkey));
  } catch {
    return false;
  }
}

export class Pool {
  constructor(urls, log = () => {}) {
    this.urls = urls;
    this.log = log;
    this.subs = new Map();
    this.seen = new Set();
    this.sockets = new Map();
    this.closed = false;
    this.waiters = [];
    for (const u of urls) this._connect(u, 1000);
  }
  get connected() {
    let n = 0;
    for (const ws of this.sockets.values()) if (ws.readyState === 1) n++;
    return n;
  }
  waitConnected(timeoutMs) {
    if (this.connected > 0) return Promise.resolve();
    return new Promise((resolve, reject) => {
      const t = setTimeout(() => reject(new Error('no nostr relay reachable')), timeoutMs);
      this.waiters.push(() => { clearTimeout(t); resolve(); });
    });
  }
  _connect(url, backoff) {
    if (this.closed) return;
    let ws;
    try { ws = new WebSocket(url); } catch (e) { this.log(`relay ${url}: ${e}`); return; }
    this.sockets.set(url, ws);
    ws.onopen = () => {
      backoff = 1000;
      for (const [id, f] of this.subs) ws.send(JSON.stringify(['REQ', id, f.filter]));
      const w = this.waiters; this.waiters = [];
      w.forEach((fn) => fn());
    };
    ws.onmessage = (m) => this._handle(url, m.data);
    ws.onclose = () => {
      if (this.closed) return;
      setTimeout(() => this._connect(url, Math.min(backoff * 2, 30000)), backoff);
    };
    ws.onerror = () => this.log(`relay ${url} error`);
  }
  async _handle(url, data) {
    let msg;
    try { msg = JSON.parse(data); } catch { return; }
    if (!Array.isArray(msg)) return;
    if (msg[0] === 'EVENT' && msg.length >= 3) {
      const sub = this.subs.get(msg[1]);
      const ev = msg[2];
      if (!sub || !ev || this.seen.has(ev.id)) return;
      if (!(await verifyEvent(ev))) return; // verify before de-dup
      if (this.seen.has(ev.id)) return;
      this.seen.add(ev.id);
      sub.handler(ev);
    } else if (msg[0] === 'OK' && msg[2] === false) {
      this.log(`relay ${url} rejected event: ${msg[3]}`);
    } else if (msg[0] === 'NOTICE' || msg[0] === 'CLOSED') {
      this.log(`relay ${url} ${msg[0]}: ${msg[msg.length - 1]}`);
    }
  }
  subscribe(id, filter, handler) {
    this.subs.set(id, { filter, handler });
    for (const ws of this.sockets.values()) if (ws.readyState === 1) ws.send(JSON.stringify(['REQ', id, filter]));
  }
  publish(ev) {
    const m = JSON.stringify(['EVENT', ev]);
    let n = 0;
    for (const ws of this.sockets.values()) if (ws.readyState === 1) { ws.send(m); n++; }
    return n;
  }
  close() {
    this.closed = true;
    for (const ws of this.sockets.values()) try { ws.close(); } catch {}
  }
}
