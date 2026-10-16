// Key mapping between Mac and PC keyboards: node --test web/test/keymap.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { isMacPlatform, crossPlatform, hostCode, isShortcut, combos, chunks } from '../js/keymap.mjs';

test('platform detection', () => {
  for (const p of ['MacIntel', 'macOS', 'iPhone', 'iPad']) assert.equal(isMacPlatform(p), true, p);
  for (const p of ['Win32', 'Windows', 'Linux x86_64', 'Linux', 'Android', '', undefined]) assert.equal(isMacPlatform(p), false, String(p));
});

test('Ctrl and Cmd swap only between a Mac and a PC', () => {
  const cases = [
    // client is a Mac, host OS, swap expected
    [true, 'darwin', false], [true, 'windows', true], [true, 'linux', true], [true, 'freebsd', true],
    [false, 'darwin', true], [false, 'windows', false], [false, 'linux', false],
  ];
  for (const [mac, os, want] of cases) assert.equal(crossPlatform(mac, os), want, `${mac ? 'Mac' : 'PC'} -> ${os}`);
});

test('shortcuts land on the host modifier', () => {
  // A PC user's Ctrl+C becomes Cmd+C on a Mac host.
  assert.deepEqual(['ControlLeft', 'KeyC'].map((c) => hostCode(c, crossPlatform(false, 'darwin'))), ['MetaLeft', 'KeyC']);
  // A Mac user's Cmd+V becomes Ctrl+V on a Windows or Linux host.
  for (const os of ['windows', 'linux']) {
    assert.deepEqual(['MetaLeft', 'KeyV'].map((c) => hostCode(c, crossPlatform(true, os))), ['ControlLeft', 'KeyV']);
  }
  // The other modifier swaps too, so nothing is lost: the PC's Windows key is Ctrl on a Mac.
  assert.equal(hostCode('MetaRight', true), 'ControlRight');
  // Same platform, or swapping turned off: keys pass through.
  for (const c of ['ControlLeft', 'MetaLeft', 'AltLeft', 'KeyC']) assert.equal(hostCode(c, false), c);
  assert.equal(hostCode('AltLeft', true), 'AltLeft');
  assert.equal(hostCode('ShiftRight', true), 'ShiftRight');
});

test('copy/paste shortcut detection uses this device\'s modifier', () => {
  assert.equal(isShortcut({ metaKey: true, ctrlKey: false }, true), true);
  assert.equal(isShortcut({ metaKey: false, ctrlKey: true }, true), false);
  assert.equal(isShortcut({ metaKey: false, ctrlKey: true }, false), true);
  assert.equal(isShortcut({ metaKey: true, ctrlKey: false }, false), false);
});

test('key combinations follow the host', () => {
  const labels = (os) => combos(os).map(([l]) => l).join(' | ');
  assert.match(labels('darwin'), /Cmd\+Tab/);
  assert.match(labels('darwin'), /Force Quit/);
  assert.doesNotMatch(labels('darwin'), /Alt\+F4/);
  assert.match(labels('windows'), /Windows key/);
  assert.match(labels('windows'), /Task Manager/);
  assert.match(labels('linux'), /Super key/);
  for (const os of ['darwin', 'windows', 'linux']) {
    for (const [label, keys] of combos(os)) assert.ok(keys.length > 0 && keys.every((k) => /^[A-Z]/.test(k)), label);
  }
});

test('chunks never split an emoji', () => {
  const text = 'ab👋cd👋'.repeat(500);
  for (const n of [1, 2, 3, 7, 8192]) {
    const parts = chunks(text, n);
    assert.equal(parts.join(''), text);
    for (const p of parts) assert.ok(!/[\uD800-\uDBFF]$/.test(p), `part ends in half an emoji (n=${n})`);
  }
  assert.deepEqual(chunks('', 10), []);
});
