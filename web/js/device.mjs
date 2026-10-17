// Device keys (mirrors internal/secure/device.go). A device key lets a host
// recognise this browser and approve it automatically. The private key is a
// non-extractable WebCrypto key: it can sign, but no script (including this
// page) can read it. With "remember" it is kept in IndexedDB; otherwise it
// exists only in this tab's memory.
import { b64Encode, sha256, utf8, hex } from './crypto.mjs';

const DB = 'setu';
const STORE = 'keys';
const ID = 'device';
const ALG = { name: 'ECDSA', namedCurve: 'P-256' };

let memoryKey = null; // { privateKey, publicRaw }

function idb(mode, fn) {
  return new Promise((resolve, reject) => {
    const open = indexedDB.open(DB, 1);
    open.onupgradeneeded = () => open.result.createObjectStore(STORE);
    open.onerror = () => reject(open.error);
    open.onsuccess = () => {
      const tx = open.result.transaction(STORE, mode);
      const req = fn(tx.objectStore(STORE));
      tx.oncomplete = () => { open.result.close(); resolve(req && req.result); };
      tx.onerror = () => { open.result.close(); reject(tx.error); };
    };
  });
}

async function generate() {
  const pair = await crypto.subtle.generateKey(ALG, false, ['sign']); // private key non-extractable
  const publicRaw = new Uint8Array(await crypto.subtle.exportKey('raw', pair.publicKey));
  return { privateKey: pair.privateKey, publicRaw };
}

// deviceKey returns this browser's key. remember=true loads or stores it in
// IndexedDB; false uses a key kept only in memory. Falls back to memory if
// storage is unavailable (private browsing).
export async function deviceKey(remember) {
  if (remember) {
    try {
      const saved = await idb('readonly', (s) => s.get(ID));
      if (saved && saved.privateKey && saved.publicRaw) return { privateKey: saved.privateKey, publicRaw: new Uint8Array(saved.publicRaw) };
      const k = memoryKey || (await generate());
      await idb('readwrite', (s) => s.put({ privateKey: k.privateKey, publicRaw: k.publicRaw }, ID));
      memoryKey = k;
      // Ask the browser not to evict the key under storage pressure or
      // after a stretch without visits (Safari's 7-day rule): losing it
      // means pairing or giving the password again.
      navigator.storage?.persist?.().catch(() => {});
      return k;
    } catch {
      /* storage blocked: fall through to memory */
    }
  }
  if (!memoryKey) memoryKey = await generate();
  return memoryKey;
}

// forgetDevice deletes the stored key; the next join uses a new identity.
export async function forgetDevice() {
  memoryKey = null;
  try { await idb('readwrite', (s) => s.delete(ID)); } catch { /* nothing stored */ }
}

// hasStoredDevice reports whether a remembered key exists.
export async function hasStoredDevice() {
  try { return !!(await idb('readonly', (s) => s.get(ID))); } catch { return false; }
}

const sortPair = (a, b) => (a < b ? [a, b] : [b, a]);

export function deviceTranscript(nonce, fpA, fpB) {
  const [a, b] = sortPair(fpA, fpB);
  return utf8(`setu/v1/device\n${b64Encode(nonce)}\n${a}\n${b}`);
}

// signDevice signs this connection's transcript (IEEE P1363, 64 bytes).
export async function signDevice(key, nonce, fpA, fpB) {
  const sig = await crypto.subtle.sign({ name: 'ECDSA', hash: 'SHA-256' }, key.privateKey, deviceTranscript(nonce, fpA, fpB));
  return new Uint8Array(sig);
}

// deviceFingerprint matches secure.DeviceFingerprint: 80 bits of SHA-256.
export async function deviceFingerprint(publicRaw) {
  const x = hex((await sha256(publicRaw)).subarray(0, 10));
  return [0, 4, 8, 12, 16].map((i) => x.slice(i, i + 4)).join('-');
}
