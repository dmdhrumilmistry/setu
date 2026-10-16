// Keeps a live session resumable when this tab is reloaded. Mobile browsers
// discard background tabs and reload them when you come back; the invite link
// was already removed from the address bar, so without this the session
// would be gone.
//
// What is kept: the invite (secret and relays) and the current resume
// token, in sessionStorage. That storage belongs to this one tab: other tabs
// cannot read it, and the browser deletes it when the tab is closed. Entries
// also expire after KEEP_TTL without the page being active (the host forgets
// the session by then too), and are removed as soon as the session ends,
// fails, or the user leaves it. The token alone is useless: it is single
// use, bound to this device key, and replaced on every reconnect. Turning
// the setting off removes the entry.
import { b64Decode, b64Encode, parseInvite } from './crypto.mjs';

const KEY = 'setu-session';
const PREF = 'setu-keep-session';
export const KEEP_TTL = 60 * 60 * 1000; // the host's background resume grace

// keepEnabled is the user's preference (default on).
export function keepEnabled() {
  try { return localStorage.getItem(PREF) !== '0'; } catch { return false; }
}

export function setKeepEnabled(on) {
  try { localStorage.setItem(PREF, on ? '1' : '0'); } catch { /* storage blocked */ }
  if (!on) clearKept();
}

// keepSession stores the session after each "ready" (the token rotates).
export function keepSession(inv, token) {
  if (!keepEnabled() || !inv || !inv.secret || !token) return;
  const frag = new URLSearchParams({ k: inv.secret });
  if (inv.relays.length) frag.set('r', inv.relays.join(','));
  try {
    sessionStorage.setItem(KEY, JSON.stringify({ v: 1, inv: frag.toString(), token: b64Encode(token), at: Date.now() }));
  } catch { /* storage blocked or full: just don't keep it */ }
}

// touchKept restarts the expiry clock while the page is still alive.
export function touchKept() {
  try {
    const raw = sessionStorage.getItem(KEY);
    if (!raw) return;
    const e = JSON.parse(raw);
    e.at = Date.now();
    sessionStorage.setItem(KEY, JSON.stringify(e));
  } catch { /* ignore */ }
}

// loadKept returns { inv, token } for a session this tab can resume, or null.
export function loadKept() {
  let e;
  try { e = JSON.parse(sessionStorage.getItem(KEY) || 'null'); } catch { e = null; }
  if (!e) return null;
  try {
    if (!keepEnabled() || e.v !== 1 || typeof e.at !== 'number' || Date.now() - e.at > KEEP_TTL || e.at > Date.now() + 60000) throw new Error('stale');
    const inv = parseInvite('#' + e.inv);
    const token = b64Decode(e.token);
    if (!/^[A-Za-z0-9_-]{43}$/.test(inv.secret) || token.length !== 32) throw new Error('malformed');
    if (!inv.relays.every((r) => /^wss:\/\//.test(r))) throw new Error('bad relay');
    return { inv, token };
  } catch {
    clearKept();
    return null;
  }
}

export function clearKept() {
  try { sessionStorage.removeItem(KEY); } catch { /* ignore */ }
}
