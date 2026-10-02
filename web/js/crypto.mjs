// Mirrors internal/secure and internal/link in Go. Pure WebCrypto.
const te = new TextEncoder();
const td = new TextDecoder();

export const PBKDF2_MIN_ITER = 100000;

export function b64urlDecode(s) {
  s = s.replace(/-/g, '+').replace(/_/g, '/').replace(/\s+/g, '');
  while (s.length % 4) s += '=';
  return b64Decode(s);
}
export function b64urlEncode(bytes) {
  return b64Encode(bytes).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}
export function b64Decode(s) {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}
export function b64Encode(bytes) {
  let bin = '';
  for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(bin);
}
export const hex = (b) => Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('');
export function unhex(s) {
  const out = new Uint8Array(s.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(s.substr(i * 2, 2), 16);
  return out;
}
export const utf8 = (s) => te.encode(s);
export const fromUtf8 = (b) => td.decode(b);

export function randomBytes(n) {
  const b = new Uint8Array(n);
  crypto.getRandomValues(b);
  return b;
}

export async function sha256(bytes) {
  return new Uint8Array(await crypto.subtle.digest('SHA-256', bytes));
}

async function hkdf(secret, info) {
  const k = await crypto.subtle.importKey('raw', secret, 'HKDF', false, ['deriveBits']);
  const bits = await crypto.subtle.deriveBits({ name: 'HKDF', hash: 'SHA-256', salt: new Uint8Array(0), info: utf8(info) }, k, 256);
  return new Uint8Array(bits);
}

// Room = { id: hex string, key: CryptoKey (AES-GCM) }
export async function deriveRoom(secretB64url) {
  const secret = b64urlDecode(secretB64url);
  if (secret.length !== 32) throw new Error('invalid invite secret');
  const id = hex(await hkdf(secret, 'setu/v1/room-id'));
  const raw = await hkdf(secret, 'setu/v1/signal-key');
  const key = await crypto.subtle.importKey('raw', raw, 'AES-GCM', false, ['encrypt', 'decrypt']);
  return { id, key };
}

export const signalAAD = (roomID, from, to) => utf8(`setu/v1/signal|${roomID}|${from}|${to}`);

export async function seal(room, plaintext, aad) {
  const iv = randomBytes(12);
  const ct = new Uint8Array(await crypto.subtle.encrypt({ name: 'AES-GCM', iv, additionalData: aad }, room.key, plaintext));
  const out = new Uint8Array(iv.length + ct.length);
  out.set(iv);
  out.set(ct, iv.length);
  return b64Encode(out);
}

export async function open(room, sealed, aad) {
  const raw = b64Decode(sealed);
  if (raw.length < 28) throw new Error('short ciphertext');
  const pt = await crypto.subtle.decrypt({ name: 'AES-GCM', iv: raw.subarray(0, 12), additionalData: aad }, room.key, raw.subarray(12));
  return new Uint8Array(pt);
}

export function fingerprint(sdp) {
  for (const raw of sdp.split('\n')) {
    const line = raw.trim();
    if (!line.startsWith('a=fingerprint:')) continue;
    const parts = line.slice('a=fingerprint:'.length).trim().split(/\s+/);
    if (parts.length !== 2) continue;
    return parts[0].toLowerCase() + ' ' + parts[1].toUpperCase();
  }
  throw new Error('no DTLS fingerprint in SDP');
}

const sortPair = (a, b) => (a < b ? [a, b] : [b, a]);

export async function sas(fpA, fpB) {
  const [a, b] = sortPair(fpA, fpB);
  const h = await sha256(utf8(`setu/v1/sas\n${a}\n${b}`));
  const n = new DataView(h.buffer).getUint32(0) % 1000000;
  const s = String(n).padStart(6, '0');
  return s.slice(0, 3) + ' ' + s.slice(3);
}

export async function passwordKey(password, salt, iterations) {
  if (iterations < PBKDF2_MIN_ITER) throw new Error('host requested a weak password work factor');
  const k = await crypto.subtle.importKey('raw', utf8(password), 'PBKDF2', false, ['deriveBits']);
  const bits = await crypto.subtle.deriveBits({ name: 'PBKDF2', hash: 'SHA-256', salt, iterations }, k, 256);
  return new Uint8Array(bits);
}

export async function authProof(key, nonce, fpA, fpB) {
  const [a, b] = sortPair(fpA, fpB);
  const k = await crypto.subtle.importKey('raw', key, { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
  const msg = utf8(`setu/v1/auth\n${b64Encode(nonce)}\n${a}\n${b}`);
  return new Uint8Array(await crypto.subtle.sign('HMAC', k, msg));
}

// Copy/paste codes: base64url(deflate-raw(JSON)).
async function pipe(bytes, stream) {
  const res = new Response(new Blob([bytes]).stream().pipeThrough(stream));
  return new Uint8Array(await res.arrayBuffer());
}
export async function encodeCode(obj) {
  return b64urlEncode(await pipe(utf8(JSON.stringify(obj)), new CompressionStream('deflate-raw')));
}
export async function decodeCode(code) {
  const raw = b64urlDecode(code.replace(/\s+/g, ''));
  const out = await pipe(raw, new DecompressionStream('deflate-raw'));
  if (out.length > 256 * 1024) throw new Error('code too large');
  return JSON.parse(fromUtf8(out));
}

// Invite parsing (mirrors internal/link.Parse).
export function parseInvite(s) {
  s = (s || '').trim();
  if (!s) throw new Error('empty invite');
  let frag = s;
  const i = s.indexOf('#');
  if (i >= 0) frag = s.slice(i + 1);
  else if (!s.includes('=')) return { offer: s };
  const p = new URLSearchParams(frag);
  const inv = { secret: p.get('k') || '', offer: p.get('o') || '', relays: [] };
  const r = p.get('r');
  if (r) inv.relays = r.split(',').map((x) => x.trim()).filter(Boolean);
  if (!inv.secret && !inv.offer) throw new Error('invite has neither k= nor o=');
  return inv;
}
