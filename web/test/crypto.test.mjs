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

test('resume id matches Go', async () => {
  const tok = Uint8Array.from({ length: 32 }, (_, i) => i);
  assert.equal(await C.resumeId(tok), '2f44565e3fdd6644726803d41e7fc36e');
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

// Device keys: a signature made by Go (secure.DeviceKey.Sign) must verify
// in WebCrypto, and both sides must agree on the fingerprint.
import { deviceTranscript, deviceFingerprint, signDevice } from '../js/device.mjs';

const goDevice = {
  pub: '04f4c396d791eab640e864213d8c4a758d1e97e4875f09145ec53cbf97e5acd86f6c0b77ea2ae52274f54cdacc70c731874696f89a9483e58bdedd6a902aa2f637',
  sig: 'a89eefafb726eb6bfb5c7dd96b660ffdd9b9cf151379e7f8770f1005dc9665ddd48d9ef038c9fbe2dd12ca7d7596916130db38916e30fb052f30910e2582390e',
  fp: '019e-dc90-3f80-4457-9a53',
};
const vecNonce = Uint8Array.from({ length: 32 }, (_, i) => i);

test('device signature from Go verifies in WebCrypto', async () => {
  const pub = await crypto.subtle.importKey('raw', C.unhex(goDevice.pub), { name: 'ECDSA', namedCurve: 'P-256' }, false, ['verify']);
  const ok = await crypto.subtle.verify({ name: 'ECDSA', hash: 'SHA-256' }, pub, C.unhex(goDevice.sig),
    deviceTranscript(vecNonce, 'sha-256 CC:DD', 'sha-256 AA:BB'));
  assert.equal(ok, true);
  assert.equal(await deviceFingerprint(C.unhex(goDevice.pub)), goDevice.fp);
});

test('device signature is bound to the connection', async () => {
  const pair = await crypto.subtle.generateKey({ name: 'ECDSA', namedCurve: 'P-256' }, false, ['sign', 'verify']);
  const sig = await signDevice({ privateKey: pair.privateKey }, vecNonce, FPA, FPB);
  assert.equal(sig.length, 64);
  const verify = (n, a, b) => crypto.subtle.verify({ name: 'ECDSA', hash: 'SHA-256' }, pair.publicKey, sig, deviceTranscript(n, a, b));
  assert.equal(await verify(vecNonce, FPB, FPA), true);
  assert.equal(await verify(new Uint8Array(32), FPA, FPB), false);
  assert.equal(await verify(vecNonce, FPA, 'sha-256 FF'), false);
});

test('pairing codes normalize like secure.NormalizePairingCode', () => {
  // Same cases as internal/secure/pairing_test.go.
  assert.equal(C.normalizePairingCode('7k2m-qx9d 4hrt'), '7K2MQX9D4HRT');
  assert.equal(C.normalizePairingCode('oil-OIL'), '011011');
});
