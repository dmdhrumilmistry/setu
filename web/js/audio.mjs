// setu remote desktop sound: decodes the host's IMA ADPCM packets
// (internal/desktop/audio.go has the wire format) and plays them with Web
// Audio, a little behind real time so network jitter does not cut it up.

const MSG_AUDIO = 1;
const HEADER = 10;
const PER_CHANNEL = 4;
const LEAD = 0.08; // seconds of sound queued before playing
const MAX_LAG = 0.4; // further behind than this, skip ahead

const STEPS = [
  7, 8, 9, 10, 11, 12, 13, 14, 16, 17, 19, 21, 23, 25, 28, 31, 34, 37, 41, 45,
  50, 55, 60, 66, 73, 80, 88, 97, 107, 118, 130, 143, 157, 173, 190, 209, 230,
  253, 279, 307, 337, 371, 408, 449, 494, 544, 598, 658, 724, 796, 876, 963,
  1060, 1166, 1282, 1411, 1552, 1707, 1878, 2066, 2272, 2499, 2749, 3024, 3327,
  3660, 4026, 4428, 4871, 5358, 5894, 6484, 7132, 7845, 8630, 9493, 10442,
  11487, 12635, 13899, 15289, 16818, 18500, 20350, 22385, 24623, 27086, 29794,
  32767,
];
const INDEX = [-1, -1, -1, -1, 2, 4, 6, 8, -1, -1, -1, -1, 2, 4, 6, 8];

// decodePacket returns { rate, seq, channels: [Int16Array, ...] }, or null
// for anything that is not a well-formed audio packet.
export function decodePacket(buf) {
  const b = new Uint8Array(buf);
  if (b.length < HEADER || b[0] !== MSG_AUDIO || b[1] < 1 || b[1] > 2) return null;
  const dv = new DataView(b.buffer, b.byteOffset, b.byteLength);
  const ch = b[1];
  const rate = dv.getUint16(2, true);
  const seq = dv.getUint32(4, true);
  const frames = dv.getUint16(8, true);
  const per = (frames + 1) >> 1;
  if (b.length !== HEADER + ch * PER_CHANNEL + ch * per || rate < 3000) return null;
  const data = HEADER + ch * PER_CHANNEL;
  const channels = [];
  for (let c = 0; c < ch; c++) {
    let pred = dv.getInt16(HEADER + c * PER_CHANNEL, true);
    let index = Math.min(88, b[HEADER + c * PER_CHANNEL + 2]);
    const out = new Int16Array(frames);
    for (let i = 0; i < frames; i++) {
      const code = (i & 1 ? b[data + c * per + (i >> 1)] >> 4 : b[data + c * per + (i >> 1)]) & 15;
      const step = STEPS[index];
      let d = step >> 3;
      if (code & 4) d += step;
      if (code & 2) d += step >> 1;
      if (code & 1) d += step >> 2;
      pred = Math.max(-32768, Math.min(32767, code & 8 ? pred - d : pred + d));
      index = Math.max(0, Math.min(88, index + INDEX[code]));
      out[i] = pred;
    }
    channels.push(out);
  }
  return { rate, seq, channels };
}

// createPlayer plays packets once started. start() must run inside a user
// action (a click): browsers only let a page make sound after one.
export function createPlayer() {
  let ctx = null;
  let next = 0;
  let lastSeq = null;

  function push(buf) {
    if (!ctx) return;
    const p = decodePacket(buf);
    if (!p) return;
    // The channel is unordered: drop a packet older than one already played.
    if (lastSeq !== null && ((p.seq - lastSeq) | 0) <= 0) return;
    lastSeq = p.seq;
    const frames = p.channels[0].length;
    if (!frames) return;
    const ab = ctx.createBuffer(p.channels.length, frames, p.rate);
    p.channels.forEach((s, c) => {
      const f = ab.getChannelData(c);
      for (let i = 0; i < frames; i++) f[i] = s[i] / 32768;
    });
    const src = ctx.createBufferSource();
    src.buffer = ab;
    src.connect(ctx.destination);
    const now = ctx.currentTime;
    // After a gap (silence is not sent, or packets were lost) or a stall,
    // start again a little ahead of now.
    if (next < now + 0.01 || next > now + MAX_LAG) next = now + LEAD;
    src.start(next);
    next += ab.duration;
  }

  return {
    push,
    get playing() { return ctx !== null; },
    start() {
      if (ctx) return;
      const AC = window.AudioContext || window.webkitAudioContext;
      ctx = new AC({ latencyHint: 'interactive' });
      ctx.resume?.().catch(() => {});
      next = 0;
      lastSeq = null;
    },
    stop() {
      ctx?.close().catch(() => {});
      ctx = null;
    },
  };
}
