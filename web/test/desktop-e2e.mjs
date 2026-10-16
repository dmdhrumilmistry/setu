// Desktop end-to-end test: real Chromium -> web client -> nostr relay ->
// `setu desktop` sharing this machine's real screen (Windows, or X11: run it
// under xvfb-run on Linux). Needs a built ./setu binary and Playwright:
//   go build -o setu ./cmd/setu && node web/test/desktop-e2e.mjs
// SETU_DESKTOP_INPUT_TEST=1 also clicks on the shared screen and replaces
// the clipboard, so CI sets it and local runs leave it off by default.
import { spawn, execFileSync } from 'node:child_process';
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
const bin = process.env.SETU_BIN || join(webRoot, '..', process.platform === 'win32' ? 'setu.exe' : 'setu');
const inputTest = !!process.env.SETU_DESKTOP_INPUT_TEST;
let relays = process.env.SETU_RELAYS || 'wss://nos.lol,wss://relay.damus.io';
const hostEnv = { ...process.env, SETU_PASSWORD: 'desktop-test-pw' };
const chromeArgs = ['--disable-features=WebRtcHideLocalIpsWithMdns'];
let relayProc;
if (process.env.SETU_LOCAL_RELAY) {
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

const host = spawn(bin, ['desktop', '--qr=false', '--view-link', '--fps', '10', '--relay', relays, '--web-url', `http://127.0.0.1:${port}/`],
  { env: hostEnv, stdio: ['ignore', 'ignore', 'pipe'] });
let log = '';
host.stderr.on('data', (d) => { log += d; process.stderr.write('host| ' + d); });

function hostClipboard() {
  try { return execFileSync('xclip', ['-o', '-selection', 'clipboard'], { timeout: 3000 }).toString(); } catch (e) { return e.code === 'ENOENT' ? null : ''; }
}
function setHostClipboard(text) {
  spawn('xclip', ['-i', '-selection', 'clipboard']).stdin.end(text);
}

async function until(fn, ms, what) {
  const t0 = Date.now();
  while (Date.now() - t0 < ms) { const v = await fn(); if (v) return v; await new Promise((r) => setTimeout(r, 200)); }
  throw new Error('timeout: ' + what);
}

async function join_(ctx, url) {
  const page = await ctx.newPage();
  page.on('pageerror', (e) => console.log('pageerror|', e.message));
  await page.goto(url);
  await page.waitForSelector('#pw-form:not([hidden])', { timeout: 60000 });
  await page.fill('#pw', 'desktop-test-pw');
  await page.click('#pw-form button');
  await page.waitForSelector('#screen-desk:not([hidden])', { timeout: 30000 });
  await page.waitForSelector('#desk-overlay', { state: 'hidden', timeout: 30000 }); // first picture drawn
  return page;
}

let browser;
let ok = false;
try {
  const links = await until(() => { const m = [...log.matchAll(/browser:\s+(\S+)/g)]; return m.length >= 2 && m.map((x) => x[1]); }, 30000, 'invite links');
  const [, w, h] = log.match(/sharing display .* \((\d+)x(\d+);/).map(Number);
  browser = await playwright.chromium.launch({ executablePath: process.env.CHROMIUM_PATH || undefined, args: chromeArgs });
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  await ctx.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: `http://127.0.0.1:${port}` });

  const page = await join_(ctx, links[0]);
  const size = await page.evaluate(() => [document.getElementById('desk-canvas').width, document.getElementById('desk-canvas').height]);
  if (size[0] !== w || size[1] !== h) throw new Error(`canvas is ${size}, host shares ${w}x${h}`);
  // The picture is the real screen, not a blank canvas.
  const distinct = await page.evaluate(() => {
    const c = document.getElementById('desk-canvas');
    const d = c.getContext('2d').getImageData(0, 0, c.width, c.height).data;
    const seen = new Set();
    for (let i = 0; i < d.length && seen.size < 8; i += 4 * 997) seen.add((d[i] << 16) | (d[i + 1] << 8) | d[i + 2]);
    return seen.size;
  });
  if (distinct < 2) throw new Error('the remote screen looks blank');
  console.log(`OK: control client sees the ${w}x${h} screen`);

  const view = await join_(ctx, links[1]);
  if (!(await view.isHidden('#desk-keys')) || !(await view.isHidden('#desk-clip-btn'))) throw new Error('view-only client shows input controls');
  if (!/view only/.test(await view.textContent('#status'))) throw new Error('view-only status missing');
  await view.close();
  console.log('OK: view-only client watches without input controls');

  if (inputTest) {
    const text = 'setu desktop e2e clipboard';
    await page.click('#desk-clip-btn');
    await page.fill('#clip-text', text);
    await page.click('#clip-send');
    await until(() => log.includes(`put ${text.length} bytes of text on this computer's clipboard`), 10000, 'clipboard reached the host');
    await page.fill('#clip-text', '');
    await page.click('#clip-get');
    await until(async () => (await page.inputValue('#clip-text')) === text, 10000, 'clipboard read back from the host');
    console.log('OK: clipboard round trip through the host');

    // Shortcuts on the remote screen, checked against the host's X11
    // clipboard with xclip (CI installs it).
    if (hostClipboard() !== null) {
      await page.click('#clip-close');
      const pasted = 'pasted with Ctrl+V\nsecond line';
      await page.evaluate((t) => navigator.clipboard.writeText(t), pasted);
      await page.keyboard.press('Control+V');
      await until(() => hostClipboard() === pasted, 10000, 'Ctrl+V reached the host clipboard');
      const copied = 'copied with Ctrl+C ' + Date.now();
      await page.keyboard.press('Control+C');
      setHostClipboard(copied); // what the focused host app would do
      await until(async () => (await page.evaluate(() => navigator.clipboard.readText())) === copied, 10000, 'Ctrl+C filled the local clipboard');
      console.log('OK: Ctrl+V and Ctrl+C on the remote screen move text both ways');
      await page.click('#desk-clip-btn');
    }

    await page.click('#clip-close');
    const box = await page.locator('#desk-canvas').boundingBox();
    await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2);
    await page.keyboard.press('Shift');
    // Leave the page as a user would (confirming the "leave?" prompt): the
    // page closes its connection, so the host notices at once.
    page.on('dialog', (d) => d.accept());
    await page.goto('about:blank');
    await until(() => /remote input: 1 clicks, 1 key presses/.test(log), 40000, 'input summary in the host log');
    console.log('OK: mouse and keyboard input reached the host');
  }
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
