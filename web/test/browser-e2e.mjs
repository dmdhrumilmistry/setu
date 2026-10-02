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

const host = spawn(bin, ['share', '--headless', '--qr=false', '--relay', relays, '--web-url', `http://127.0.0.1:${port}/`, '--', 'sh', '-c', 'echo READY; while read l; do echo "got:$l"; done'], {
  env: hostEnv,
  stdio: ['ignore', 'ignore', 'pipe'],
});
let hostLog = '';
host.stderr.on('data', (d) => { hostLog += d; process.stderr.write('host| ' + d); });

const deadline = (ms, what) => new Promise((_, rej) => setTimeout(() => rej(new Error('timeout: ' + what)), ms));
async function until(fn, ms, what) {
  const t0 = Date.now();
  while (Date.now() - t0 < ms) { const v = await fn(); if (v) return v; await new Promise((r) => setTimeout(r, 200)); }
  throw new Error('timeout: ' + what);
}

let browser;
let ok = false;
try {
  const url = await until(() => (hostLog.match(/browser:\s+(\S+)/) || [])[1], 15000, 'invite link');
  const proxy = process.env.HTTPS_PROXY;
  const args = chromeArgs;
  // Pass the proxy as raw flags: Playwright's proxy option forces loopback through it.
  if (proxy) args.push('--proxy-server=' + proxy, '--proxy-bypass-list=127.0.0.1;localhost');
  browser = await playwright.chromium.launch({ executablePath: process.env.CHROMIUM_PATH || undefined, args });
  const ctx = await browser.newContext({ ignoreHTTPSErrors: !!proxy });
  const page = await ctx.newPage();
  page.on('console', (m) => console.log('page|', m.text()));
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
  await until(() => hostLog.includes(`verification code ${sas}`), 5000, 'matching SAS on host');
  console.log(`OK: browser joined, typed, received echo; SAS ${sas} matches host`);
  ok = true;
} catch (e) {
  console.error('FAIL:', e.message);
} finally {
  if (browser) await browser.close();
  host.kill('SIGTERM');
  if (relayProc) relayProc.kill('SIGTERM');
  server.close();
  process.exit(ok ? 0 : 1);
}
