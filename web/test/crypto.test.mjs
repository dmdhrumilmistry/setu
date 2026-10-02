// Cross-checks web/js/crypto.mjs against the Go test vectors in
// internal/secure/secure_test.go. Run: node --test web/test/
import test from 'node:test';
import assert from 'node:assert/strict';
import * as C from '../js/crypto.mjs';

const secret = C.b64urlEncode(Uint8Array.from({ length: 32 }, (_, i) => i));
const FPA = 'sha-256 AA:BB:CC';
const FPB = 'sha-256 11:22:33';

test('room id matches Go', async () => {
  const room = await C.deriveRoom(secret);
  assert.equal(room.id, '477d2abd3cbaf16a91c3190076bace99c6e39dd51da5e746692f82844b2b4a10');
});

test('SAS matches Go and is symmetric', async () => {
  assert.equal(await C.sas(FPA, FPB), '774 610');
  assert.equal(await C.sas(FPB, FPA), '774 610');
});

test('auth proof matches Go', async () => {
  const key = await C.passwordKey('correct horse', C.utf8('saltsaltsaltsalt'), 100000);
  const proof = await C.authProof(key, new Uint8Array(32).fill(7), FPA, FPB);
  assert.equal(C.hex(proof), 'b6660ec4b55b56c57d702ee56424657a7a82c8ad458c49a11cfa3dbc07a73a44');
});

test('weak PBKDF2 rejected', async () => {
  await assert.rejects(C.passwordKey('x', C.utf8('s'), 10));
});

test('seal/open round trip and AAD binding', async () => {
  const room = await C.deriveRoom(secret);
  const aad = C.signalAAD(room.id, 'a', 'b');
  const ct = await C.seal(room, C.utf8('hi'), aad);
  assert.equal(C.fromUtf8(await C.open(room, ct, aad)), 'hi');
  await assert.rejects(C.open(room, ct, C.signalAAD(room.id, 'x', 'b')));
});

test('code round trip', async () => {
  const obj = { type: 'answer', sid: 'abc', sdp: 'v=0\r\n'.repeat(50) };
  assert.deepEqual(await C.decodeCode(await C.encodeCode(obj)), obj);
});

test('parseInvite', () => {
  assert.deepEqual(C.parseInvite('https://x/#k=abc&r=wss%3A%2F%2Fa%2Cwss%3A%2F%2Fb'), { secret: 'abc', offer: '', relays: ['wss://a', 'wss://b'] });
  assert.deepEqual(C.parseInvite('CODE'), { offer: 'CODE' });
  assert.throws(() => C.parseInvite('https://x/#z=1'));
});
