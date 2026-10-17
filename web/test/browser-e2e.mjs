// Browser end-to-end test: real Chromium -> web client -> public nostr relays
// -> `setu share` (pion + PTY). Needs network access, a built ./setu binary and
// Playwright. Not part of `node --test`; run manually or in CI:
//   go build -o setu ./cmd/setu && node web/test/browser-e2e.mjs
import { spawn } from 'node:child_process';
import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { extname, join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);
let playwright;
for (const m of ['playwright', process.env.PLAYWRIGHT_MODULE].filter(Boolean)) {
  try { playwright = require(m); break; } catch {}
}
if (!playwright) { console.error('playwright not found (npm i playwright, or set PLAYWRIGHT_MODULE)'); process.exit(2); }

const webRoot = join(dirname(fileURLToPath(import.meta.url)), '..');
const bin = process.env.SETU_BIN || join(webRoot, '..', 'setu');
let relays = process.env.SETU_RELAYS || 'wss://nos.lol,wss://relay.damus.io';
const hostEnv = { ...process.env, SETU_PASSWORD: 'browser-test-pw' };
const chromeArgs = ['--disable-features=WebRtcHideLocalIpsWithMdns'];
let relayProc;
if (process.env.SETU_LOCAL_RELAY) {
  // Local wss:// relay with a self-signed cert, for sandboxes where the
  // browser cannot reach public relays.
  relayProc = spawn('go', ['run', './internal/nostr/relaytest/testrelay'], { cwd: join(webRoot, '..'), stdio: ['ignore', 'pipe', 'inherit'] });
  const info = await new Promise((resolve) => {
    let buf = '';
    relayProc.stdout.on('data', (d) => {
      buf += d;
      const u = buf.match(/URL (\S+)/), c = buf.match(/CERT (\S+)/);
      if (u && c) resolve({ url: u[1], cert: c[1] });
    });
  });
  relays = info.url;
  hostEnv.SSL_CERT_FILE = info.cert;
  chromeArgs.push('--ignore-certificate-errors');
}
const types = { '.html': 'text/html', '.mjs': 'text/javascript', '.js': 'text/javascript', '.css': 'text/css' };

const server = createServer(async (req, res) => {
  try {
    const p = new URL(req.url, 'http://x').pathname;
    const body = await readFile(join(webRoot, p === '/' ? 'index.html' : p));
    res.writeHead(200, { 'content-type': types[extname(p)] || 'text/html' });
    res.end(body);
  } catch { res.writeHead(404); res.end(); }
}).listen(0);
const port = server.address().port;

// startHost runs `setu share` headless with extra flags and collects its log.
function startHost(extra, tag) {
  const proc = spawn(bin, ['share', '--headless', '--qr=false', '--relay', relays, '--web-url', `http://127.0.0.1:${port}/`, ...extra,
    '--', 'sh', '-c', 'echo READY; while read l; do echo "got:$l"; done'], { env: hostEnv, stdio: ['ignore', 'ignore', 'pipe'] });
  const h = { proc, log: '' };
  proc.stderr.on('data', (d) => { h.log += d; process.stderr.write(tag + '| ' + d); });
  return h;
}
const host = startHost(['--verbose'], 'host');
const hosts = [host];

const deadline = (ms, what) => new Promise((_, rej) => setTimeout(() => rej(new Error('timeout: ' + what)), ms));
async function until(fn, ms, what) {
  const t0 = Date.now();
  while (Date.now() - t0 < ms) { const v = await fn(); if (v) return v; await new Promise((r) => setTimeout(r, 200)); }
  throw new Error('timeout: ' + what);
}

let browser;
let ok = false;
try {
  const url = await until(() => (host.log.match(/browser:\s+(\S+)/) || [])[1], 15000, 'invite link');

  const proxy = process.env.HTTPS_PROXY;
  const args = chromeArgs;
  // Pass the proxy as raw flags: Playwright's proxy option forces loopback through it.
  if (proxy) args.push('--proxy-server=' + proxy, '--proxy-bypass-list=127.0.0.1;localhost');
  browser = await playwright.chromium.launch({ executablePath: process.env.CHROMIUM_PATH || undefined, args });
  const ctx = await browser.newContext({ ignoreHTTPSErrors: !!proxy });
  const page = await ctx.newPage();
  // Keep a handle on the page's peer connections, to cut one below.
  await page.addInitScript(() => {
    const Orig = window.RTCPeerConnection;
    window.__pcs = [];
    window.RTCPeerConnection = function (...a) { const pc = new Orig(...a); window.__pcs.push(pc); return pc; };
    window.RTCPeerConnection.prototype = Orig.prototype;
  });
  page.on('console', (m) => console.log('page|', m.text()));
  page.on('dialog', (d) => d.accept()); // the "leave this page?" prompt on reload
  page.on('pageerror', (e) => console.log('pageerror|', e.message));
  page.on('response', (r) => { if (r.status() >= 400) console.log('http|', r.status(), r.url()); });
  await page.goto(url);
  if ((await page.evaluate(() => location.hash)) !== '') throw new Error('secret left in the address bar');
  await page.waitForSelector('#pw-form:not([hidden])', { timeout: 60000 });
  await page.fill('#pw', 'browser-test-pw');
  await page.click('#pw-form button');
  await page.waitForSelector('#screen-term:not([hidden])', { timeout: 30000 });
  const rows = () => page.evaluate(() => document.querySelector('.xterm-rows')?.textContent || '');
  await until(async () => (await rows()).includes('READY'), 15000, 'READY in terminal');
  await page.click('#term');
  await page.keyboard.type('hello');
  await page.keyboard.press('Enter');
  await until(async () => (await rows()).includes('got:hello'), 15000, 'echo from host');
  const sas = await page.textContent('#sas');
  await until(() => host.log.includes(`verification code ${sas}`), 5000, 'matching SAS on host');
  console.log(`OK: browser joined, typed, received echo; SAS ${sas} matches host`);

  // A dropped connection (a phone changing networks) resumes by itself: no
  // password prompt, same terminal, and typing works again.
  await page.evaluate(() => window.__pcs.at(-1).close());
  await until(() => /client #\d+ reconnected via nostr/.test(host.log), 45000, 'host saw the client reconnect');
  await until(async () => (await rows()).includes('[setu] reconnected'), 15000, 'reconnected notice');
  if (!(await page.isHidden('#pw-form')) || (await page.isHidden('#screen-term'))) throw new Error('reconnect asked for the password again');
  await page.click('#term');
  await page.keyboard.type('again');
  await page.keyboard.press('Enter');
  await until(async () => (await rows()).includes('got:again'), 15000, 'echo after reconnect');
  if ((await rows()).split('READY').length > 2) throw new Error('reconnect replayed output the terminal already had');
  console.log('OK: dropped connection resumed without a password, output continued');

  // Switching to another tab: the page tells the host it is in the
  // background, and a connection lost meanwhile waits for the user to come
  // back instead of being given up.
  const setVisibility = (state) => page.evaluate((st) => {
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => st });
    Object.defineProperty(document, 'hidden', { configurable: true, get: () => st === 'hidden' });
    document.dispatchEvent(new Event('visibilitychange'));
  }, state);
  await setVisibility('hidden');
  await until(() => /client #\d+ is now in the background/.test(host.log), 10000, 'host told about the background tab');
  const seen = (host.log.match(/reconnected via nostr/g) || []).length;
  await page.evaluate(() => window.__pcs.at(-1).close());
  await until(async () => /reconnects when you return/.test(await page.textContent('#status')), 15000, 'reconnect paused while hidden');
  await new Promise((r) => setTimeout(r, 3000));
  if ((host.log.match(/reconnected via nostr/g) || []).length !== seen) throw new Error('reconnected while the tab was hidden');
  await setVisibility('visible');
  await until(() => (host.log.match(/reconnected via nostr/g) || []).length === seen + 1, 45000, 'reconnected when the tab came back');
  await until(async () => /connected · control/.test(await page.textContent('#status')), 15000, 'connected again');
  console.log('OK: background tab kept its session and reconnected on return');

  // A reload (or the browser discarding the background tab and loading it
  // again) restores the session in this tab without the link or password.
  const reconnects = () => (host.log.match(/reconnected via nostr/g) || []).length;
  const before = reconnects();
  await page.reload();
  await page.waitForSelector('#screen-term:not([hidden])', { timeout: 45000 });
  await until(async () => (await rows()).includes('session restored'), 15000, 'restored notice');
  await until(async () => (await rows()).includes('got:again'), 15000, 'replayed output after reload');
  if (reconnects() !== before + 1) throw new Error('reload did not resume the same session');
  await page.click('#term');
  await page.keyboard.type('reloaded');
  await page.keyboard.press('Enter');
  await until(async () => (await rows()).includes('got:reloaded'), 15000, 'echo after reload');
  console.log('OK: reloaded tab restored the session without the link or password');

  // Leave forgets it: the next load is the home page, and the host sees the
  // client go.
  await page.click('#leave');
  await page.waitForSelector('#screen-home:not([hidden])', { timeout: 15000 });
  await page.reload();
  await page.waitForSelector('#screen-home:not([hidden])', { timeout: 15000 });
  if (await page.evaluate(() => sessionStorage.length) !== 0) throw new Error('session kept after Leave');
  console.log('OK: Leave forgot the session in this tab');

  // Joining afresh from the same browser: the host remembers its device key
  // from the password given above, so it does not ask again.
  await page.goto(url);
  await page.waitForSelector('#screen-term:not([hidden])', { timeout: 60000 });
  if (!(await page.isHidden('#pw-form'))) throw new Error('a remembered device was asked for the password again');
  await until(async () => /remembered device/.test(await page.textContent('#status')), 15000, 'remembered device status');
  console.log('OK: a fresh join from a remembered device skipped the password');

  // Swiping on the terminal scrolls its scrollback instead of pulling the
  // page (which reloads it on phones).
  await page.click('#term');
  for (let i = 0; i < 80; i++) await page.keyboard.type(`line${i}\n`);
  await until(async () => (await rows()).includes('got:line79'), 20000, 'scrollback filled');
  await page.evaluate(() => { window.__notReloaded = true; });
  const swipe = (dy) => page.evaluate((dy) => {
    const el = document.getElementById('term');
    const r = el.getBoundingClientRect();
    const x = r.left + r.width / 2;
    const touch = (y) => new Touch({ identifier: 1, target: el, clientX: x, clientY: y });
    const fire = (type, y) => el.dispatchEvent(new TouchEvent(type, { touches: type === 'touchend' ? [] : [touch(y)], changedTouches: [touch(y)], bubbles: true, cancelable: true }));
    const y0 = r.top + 40;
    fire('touchstart', y0);
    for (let i = 1; i <= 20; i++) fire('touchmove', y0 + (dy * i) / 20);
    fire('touchend', y0 + dy);
  }, dy);
  await swipe(600);
  await until(async () => !(await rows()).includes('got:line79'), 5000, 'swipe down scrolled back');
  if (!(await page.evaluate(() => window.__notReloaded))) throw new Error('swipe reloaded the page');
  await swipe(-5000);
  await until(async () => (await rows()).includes('got:line79'), 5000, 'swipe up scrolled to the bottom');
  console.log('OK: touch swipes scroll the terminal');
  await page.click('#leave');
  await page.waitForSelector('#screen-home:not([hidden])', { timeout: 15000 });

  // Device keys: the home page shows this browser's (remembered) device key,
  // and the host verified the same key when it joined above.
  const home = await ctx.newPage();
  await home.goto(`http://127.0.0.1:${port}/`);
  const fp = await until(async () => {
    const t = (await home.textContent('#device-fp')) || '';
    return /^[0-9a-f]{4}(-[0-9a-f]{4}){4}$/.test(t) ? t : '';
  }, 10000, 'device fingerprint on the home page');
  await until(() => host.log.includes(`device key ${fp}`), 5000, 'host verified the browser device key');
  console.log(`OK: browser device key ${fp} verified by the host`);
  // The stored private key must be non-extractable: scripts can sign with
  // it but never read it.
  const stored = await home.evaluate(() => new Promise((resolve, reject) => {
    const open = indexedDB.open('setu', 1);
    open.onerror = () => reject(open.error);
    open.onsuccess = () => {
      const req = open.result.transaction('keys').objectStore('keys').get('device');
      req.onsuccess = async () => {
        const k = req.result.privateKey;
        let exported = true;
        try { await crypto.subtle.exportKey('pkcs8', k); } catch { exported = false; }
        resolve({ extractable: k.extractable, exported });
      };
    };
  }));
  if (stored.extractable || stored.exported) throw new Error('device private key is extractable');
  console.log('OK: stored device private key is non-extractable');

  // A host that trusts only this device admits it with no prompt.
  const trusting = startHost(['--approve', '--trust-device', fp], 'trusting');
  hosts.push(trusting);
  const url2 = await until(() => (trusting.log.match(/browser:\s+(\S+)/) || [])[1], 15000, 'invite link (trusting host)');
  const p2 = await ctx.newPage();
  await p2.goto(url2);
  await p2.waitForSelector('#pw-form:not([hidden])', { timeout: 60000 });
  await p2.fill('#pw', 'browser-test-pw');
  await p2.click('#pw-form button');
  await p2.waitForSelector('#screen-term:not([hidden])', { timeout: 30000 });
  await until(async () => ((await p2.textContent('#status')) || '').includes('trusted device'), 15000, 'trusted-device status shown');
  await until(() => trusting.log.includes(`approved automatically: trusted device ${fp}`), 5000, 'host auto-approved the device');
  console.log('OK: trusted browser device approved automatically');

  // Another browser profile has a different key and is turned away.
  const stranger = await browser.newContext({ ignoreHTTPSErrors: !!proxy });
  const p3 = await stranger.newPage();
  await p3.goto(url2);
  await p3.waitForSelector('#pw-form:not([hidden])', { timeout: 60000 });
  await p3.fill('#pw', 'browser-test-pw');
  await p3.click('#pw-form button');
  await until(async () => /denied/i.test(await p3.textContent('body')), 30000, 'unknown device denied');
  console.log('OK: unknown browser device denied');
  ok = true;
} catch (e) {
  console.error('FAIL:', e.message);
} finally {
  if (browser) await browser.close();
  for (const h of hosts) h.proc.kill('SIGTERM');
  if (relayProc) relayProc.kill('SIGTERM');
  server.close();
  process.exit(ok ? 0 : 1);
}
