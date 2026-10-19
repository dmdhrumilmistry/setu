// Remote desktop sound decoding, against a packet the Go encoder made
// (internal/desktop/audio_test.go): node --test web/test/audio.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { decodePacket } from '../js/audio.mjs';

const vec = JSON.parse(readFileSync(new URL('./testdata/audio.json', import.meta.url)));
const packet = Uint8Array.from(vec.packet.match(/../g).map((h) => parseInt(h, 16)));

test('decodes what the Go encoder wrote', () => {
  const p = decodePacket(packet.buffer);
  assert.equal(p.rate, vec.rate);
  assert.equal(p.seq, vec.seq);
  assert.equal(p.channels.length, 2);
  const interleaved = [];
  for (let i = 0; i < p.channels[0].length; i++) interleaved.push(p.channels[0][i], p.channels[1][i]);
  assert.deepEqual(interleaved, vec.samples);
});

test('rejects malformed packets', () => {
  assert.equal(decodePacket(packet.slice(0, 5).buffer), null);
  assert.equal(decodePacket(packet.slice(0, packet.length - 1).buffer), null);
  const bad = packet.slice();
  bad[1] = 3; // channels
  assert.equal(decodePacket(bad.buffer), null);
  bad[1] = 2;
  bad[0] = 9; // type
  assert.equal(decodePacket(bad.buffer), null);
});
