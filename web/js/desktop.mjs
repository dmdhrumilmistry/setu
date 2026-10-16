// setu remote desktop viewer: draws the host's screen updates on a canvas
// and turns mouse, touch, keyboard and clipboard events into input for the
// host. Wire format: internal/desktop (binary tiles + frame end on the
// "setu-screen" channel) and internal/proto (JSON control messages).
const $ = (id) => document.getElementById(id);

const MSG_TILE = 1;
const MSG_FRAME_END = 2;
const ENC_PNG = 2;
const CLIP_PART = 8192; // characters per clipboard message
const TYPE_PART = 200; // characters per "Type it" message
const LONG_PRESS = 550;
const TAP_SLOP = 8;

const COMBOS = {
  super: ['MetaLeft'],
  alttab: ['AltLeft', 'Tab'],
  altf4: ['AltLeft', 'F4'],
  taskmgr: ['ControlLeft', 'ShiftLeft', 'Escape'],
  prtsc: ['PrintScreen'],
  esc: ['Escape'],
};

// Keys some mobile keyboards report without a KeyboardEvent.code.
const CODE_FOR_KEY = {
  Backspace: 'Backspace', Enter: 'Enter', Tab: 'Tab', Escape: 'Escape', Delete: 'Delete',
  ArrowLeft: 'ArrowLeft', ArrowRight: 'ArrowRight', ArrowUp: 'ArrowUp', ArrowDown: 'ArrowDown',
};

const MODIFIERS = new Set(['ShiftLeft', 'ShiftRight', 'ControlLeft', 'ControlRight', 'AltLeft', 'AltRight', 'MetaLeft', 'MetaRight']);

// chunks splits text into pieces of at most n UTF-16 units without
// cutting a surrogate pair (an emoji) in half.
function chunks(text, n) {
  const out = [];
  for (let i = 0; i < text.length;) {
    let end = Math.min(i + n, text.length);
    if (end < text.length && /[\uD800-\uDBFF]/.test(text[end - 1])) end--;
    out.push(text.slice(i, end));
    i = end;
  }
  return out;
}

const isMac =/Mac|iPhone|iPad/.test(navigator.userAgentData?.platform || navigator.platform || '');

export function createDesktop({ send, control }) {
  const canvas = $('desk-canvas');
  const ctx = canvas.getContext('2d', { alpha: false });
  const view = $('desk-view');
  const input = $('desk-input');
  const cursor = $('desk-cursor');
  const overlay = $('desk-overlay');
  let sized = false;
  let early = [];
  let tiles = [];
  let chain = Promise.resolve();
  let fit = true;
  let clipIn = '';
  let hovering = false;
  let last = { x: 0, y: 0, cx: 0, cy: 0, vis: false };
  const held = new Set();

  // ---------- screen ----------
  function frame(buf) {
    if (!sized) { early.push(buf); return; }
    const b = new Uint8Array(buf);
    const dv = new DataView(buf);
    if (b[0] === MSG_TILE) {
      const x = dv.getUint16(2, true), y = dv.getUint16(4, true);
      const type = b[1] === ENC_PNG ? 'image/png' : 'image/jpeg';
      tiles.push(createImageBitmap(new Blob([b.subarray(10)], { type })).then((bmp) => ({ bmp, x, y })));
    } else if (b[0] === MSG_FRAME_END) {
      const id = dv.getUint32(1, true);
      const cx = dv.getInt32(5, true), cy = dv.getInt32(9, true), vis = b[13] === 1;
      const pending = tiles;
      tiles = [];
      // Decode in parallel, draw in order, then acknowledge: the host sends
      // the next frame only after this, so a slow link never queues frames.
      chain = chain.then(async () => {
        for (const r of await Promise.allSettled(pending)) {
          if (r.status !== 'fulfilled') continue;
          ctx.drawImage(r.value.bmp, r.value.x, r.value.y);
          r.value.bmp.close();
        }
        last = { ...last, cx, cy, vis };
        placeCursor();
        overlay.hidden = true;
        send({ t: 'ack', frame: id });
      });
    }
  }

  function onScreen(m) {
    const d = m.displays[m.display] || m.displays[0];
    if (canvas.width !== d.w || canvas.height !== d.h) {
      canvas.width = d.w;
      canvas.height = d.h;
      ctx.fillStyle = '#000';
      ctx.fillRect(0, 0, d.w, d.h);
    }
    const sel = $('desk-display');
    sel.replaceChildren(...m.displays.map((x, i) => {
      const o = document.createElement('option');
      o.value = i;
      o.textContent = `Display ${i + 1} (${x.w}x${x.h})${x.primary ? ' (main)' : ''}`;
      return o;
    }));
    sel.value = String(m.display);
    sel.hidden = !control || m.displays.length < 2;
    layout();
    if (!sized) {
      sized = true;
      // The host is streaming to us by now, so the saved quality applies.
      if ($('desk-quality').value !== 'balanced') send({ t: 'quality', quality: $('desk-quality').value });
      const queued = early;
      early = [];
      queued.forEach(frame);
    }
  }

  function layout() {
    if (!canvas.width) return;
    const dpr = window.devicePixelRatio || 1;
    let scale = 1 / dpr;
    if (fit) scale = Math.min(view.clientWidth / canvas.width, view.clientHeight / canvas.height, 1);
    canvas.style.width = `${Math.floor(canvas.width * scale)}px`;
    canvas.style.height = `${Math.floor(canvas.height * scale)}px`;
    view.classList.toggle('fit', fit);
    placeCursor();
  }

  // The remote pointer is drawn when the local one is not over the screen
  // (view-only, touch, or the mouse is elsewhere).
  function placeCursor() {
    cursor.hidden = !last.vis || (control && hovering);
    if (cursor.hidden) return;
    cursor.style.left = `${(last.cx / canvas.width) * 100}%`;
    cursor.style.top = `${(last.cy / canvas.height) * 100}%`;
  }

  // ---------- pointer ----------
  function remote(e) {
    const r = canvas.getBoundingClientRect();
    return {
      x: Math.max(0, Math.min(canvas.width - 1, Math.round(((e.clientX - r.left) * canvas.width) / r.width))),
      y: Math.max(0, Math.min(canvas.height - 1, Math.round(((e.clientY - r.top) * canvas.height) / r.height))),
    };
  }

  let moveQueued = false;
  let lastButtons = 0;
  function pointer(p, buttons, now = false) {
    last.x = p.x;
    last.y = p.y;
    if (buttons !== lastButtons || now) {
      lastButtons = buttons;
      send({ t: 'pointer', x: p.x, y: p.y, buttons });
      return;
    }
    if (moveQueued) return;
    moveQueued = true;
    requestAnimationFrame(() => {
      moveQueued = false;
      send({ t: 'pointer', x: last.x, y: last.y, buttons: lastButtons });
    });
  }

  function click(p, buttons) {
    pointer(p, buttons, true);
    pointer(p, 0, true);
  }

  const touches = new Map();
  let touch = null; // the single-finger gesture in progress
  let wheelAcc = 0;

  function onPointerDown(e) {
    if (!control) return;
    e.preventDefault();
    input.focus({ preventScroll: true });
    canvas.setPointerCapture(e.pointerId);
    if (e.pointerType !== 'touch') {
      pointer(remote(e), e.buttons);
      return;
    }
    touches.set(e.pointerId, { x: e.clientX, y: e.clientY });
    if (touches.size === 2) {
      if (touch) clearTimeout(touch.timer);
      touch = null; // two fingers scroll
      return;
    }
    const start = { x: e.clientX, y: e.clientY };
    const p = remote(e);
    touch = { start, p, dragging: false, done: false };
    touch.timer = setTimeout(() => {
      if (touch && !touch.dragging) {
        click(p, 2); // long press = right click
        touch.done = true;
      }
    }, LONG_PRESS);
  }

  function onPointerMove(e) {
    if (e.pointerType !== 'touch') {
      hovering = true;
      placeCursor();
      if (control) pointer(remote(e), e.buttons);
      return;
    }
    if (!control || !touches.has(e.pointerId)) return;
    const prev = touches.get(e.pointerId);
    touches.set(e.pointerId, { x: e.clientX, y: e.clientY });
    if (touches.size === 2) {
      wheelAcc += prev.y - e.clientY;
      const notches = Math.trunc(wheelAcc / 30);
      if (notches) {
        wheelAcc -= notches * 30;
        send({ t: 'wheel', dx: 0, dy: notches });
      }
      return;
    }
    if (!touch || touch.done) return;
    if (!touch.dragging && Math.hypot(e.clientX - touch.start.x, e.clientY - touch.start.y) > TAP_SLOP) {
      clearTimeout(touch.timer);
      touch.dragging = true;
      pointer(touch.p, 1, true);
    }
    if (touch.dragging) pointer(remote(e), 1);
  }

  function onPointerUp(e) {
    if (e.pointerType !== 'touch') {
      if (control) pointer(remote(e), e.buttons);
      return;
    }
    touches.delete(e.pointerId);
    if (!control || !touch) return;
    clearTimeout(touch.timer);
    if (touch.dragging) pointer(remote(e), 0, true);
    else if (!touch.done) click(touch.p, 1);
    touch = null;
  }

  canvas.addEventListener('pointerdown', onPointerDown);
  canvas.addEventListener('pointermove', onPointerMove);
  canvas.addEventListener('pointerup', onPointerUp);
  canvas.addEventListener('pointercancel', onPointerUp);
  canvas.addEventListener('pointerleave', () => { hovering = false; placeCursor(); });
  canvas.addEventListener('contextmenu', (e) => e.preventDefault());
  canvas.addEventListener('wheel', (e) => {
    if (!control) return;
    e.preventDefault();
    const unit = e.deltaMode === 1 ? 3 : e.deltaMode === 2 ? 0.1 : 100;
    wheelAcc += e.deltaY / unit;
    const dy = Math.trunc(wheelAcc);
    const dx = Math.trunc(e.deltaX / unit);
    if (dy || dx) {
      wheelAcc -= dy;
      send({ t: 'wheel', dx, dy });
    }
  }, { passive: false });

  // ---------- keyboard ----------
  function keyCode(e) {
    let code = e.code || CODE_FOR_KEY[e.key] || '';
    if (isMac && code === 'MetaLeft') code = 'ControlLeft'; // Cmd+C / Cmd+V act like Ctrl on the host
    if (isMac && code === 'MetaRight') code = 'ControlRight';
    return code;
  }

  function key(code, down) {
    if (down) held.add(code); else held.delete(code);
    send({ t: 'key', key: code, down });
  }

  function releaseAll() {
    for (const code of [...held]) key(code, false);
    if (lastButtons) pointer(last, 0, true);
  }

  input.addEventListener('keydown', (e) => {
    if (!control || e.isComposing || e.keyCode === 229) return; // IME and soft keyboards: see 'input'
    const code = keyCode(e);
    if (!code) return;
    // Ctrl/Cmd+V: let the browser deliver the local clipboard first (the
    // paste handler sends it, then the keystroke).
    if (code === 'KeyV' && (e.ctrlKey || (isMac && e.metaKey))) return;
    e.preventDefault();
    key(code, true);
  });
  input.addEventListener('keyup', (e) => {
    if (!control) return;
    const code = keyCode(e);
    // macOS fires no keyup for keys pressed while Cmd is down, so releasing
    // Cmd releases them too (otherwise Cmd+C would leave C held on the host).
    if (isMac && e.key === 'Meta') {
      for (const k of [...held]) if (!MODIFIERS.has(k)) key(k, false);
    }
    if (!held.has(code)) return;
    e.preventDefault();
    key(code, false);
  });
  input.addEventListener('paste', (e) => {
    if (!control) return;
    e.preventDefault();
    const text = e.clipboardData.getData('text/plain');
    if (text) sendClipboard(text);
    send({ t: 'key', key: 'KeyV', down: true });
    send({ t: 'key', key: 'KeyV', down: false });
  });
  input.addEventListener('input', (e) => {
    if (!control || e.isComposing) return;
    if (e.inputType === 'insertLineBreak') { key('Enter', true); key('Enter', false); }
    else if (e.inputType === 'deleteContentBackward') { key('Backspace', true); key('Backspace', false); }
    else if (e.data) send({ t: 'text', text: e.data });
    input.value = '';
  });
  input.addEventListener('compositionend', (e) => {
    if (control && e.data) send({ t: 'text', text: e.data });
    input.value = '';
  });
  input.addEventListener('blur', releaseAll);
  window.addEventListener('blur', releaseAll);

  // ---------- clipboard ----------
  function sendClipboard(text) {
    const parts = chunks(text, CLIP_PART);
    if (!parts.length) parts.push('');
    parts.forEach((part, i) => send({ t: 'clipboard', text: part, more: i < parts.length - 1 }));
  }

  async function onClipboard(m) {
    clipIn += m.text || '';
    if (m.more) return;
    const text = clipIn;
    clipIn = '';
    $('clip-text').value = text;
    try {
      if (!document.hasFocus()) throw new Error('not focused');
      await navigator.clipboard.writeText(text);
      toast('Copied the remote clipboard to this device');
    } catch {
      toast('The remote clipboard changed', 'Copy', () => copyLocal(text));
    }
  }

  async function copyLocal(text) {
    try {
      await navigator.clipboard.writeText(text);
      toast('Copied');
    } catch {
      $('desk-clip').hidden = false;
      $('clip-text').select();
      toast('Press Ctrl+C (Cmd+C on a Mac) to copy');
    }
  }

  // ---------- toolbar ----------
  $('desk-fit').addEventListener('click', () => {
    fit = !fit;
    $('desk-fit').textContent = fit ? 'Fit' : '1:1';
    $('desk-fit').setAttribute('aria-pressed', String(fit));
    layout();
  });
  $('desk-display').addEventListener('change', (e) => send({ t: 'display', display: Number(e.target.value) }));
  const quality = $('desk-quality');
  try { quality.value = localStorage.getItem('setu-quality') || 'balanced'; } catch { /* storage blocked */ }
  quality.addEventListener('change', () => {
    send({ t: 'quality', quality: quality.value });
    try { localStorage.setItem('setu-quality', quality.value); } catch { /* storage blocked */ }
  });
  $('desk-keys').addEventListener('change', (e) => {
    const combo = COMBOS[e.target.value];
    e.target.value = '';
    if (!combo) return;
    combo.forEach((c) => key(c, true));
    [...combo].reverse().forEach((c) => key(c, false));
    input.focus({ preventScroll: true });
  });
  $('desk-kbd').addEventListener('click', () => input.focus());
  $('desk-clip-btn').addEventListener('click', () => {
    $('desk-clip').hidden = !$('desk-clip').hidden;
    if (!$('desk-clip').hidden) $('clip-text').focus();
  });
  $('clip-close').addEventListener('click', () => { $('desk-clip').hidden = true; input.focus({ preventScroll: true }); });
  $('clip-send').addEventListener('click', () => {
    sendClipboard($('clip-text').value);
    toast('Sent to the remote clipboard');
  });
  $('clip-type').addEventListener('click', () => {
    // Small messages, so screen updates keep flowing while a long text is typed.
    for (const part of chunks($('clip-text').value, TYPE_PART)) send({ t: 'text', text: part });
  });
  $('clip-get').addEventListener('click', () => send({ t: 'clipboard-get' }));
  $('clip-copy').addEventListener('click', () => copyLocal($('clip-text').value));
  $('desk-full').addEventListener('click', async () => {
    if (document.fullscreenElement) { await document.exitFullscreen().catch(() => {}); return; }
    await $('screen-desk').requestFullscreen().catch(() => {});
    // In fullscreen, keys like Esc, Alt+Tab and the Windows key go to the host.
    navigator.keyboard?.lock?.().catch(() => {});
  });
  document.addEventListener('fullscreenchange', () => {
    $('desk-full').textContent = document.fullscreenElement ? 'Exit fullscreen' : 'Fullscreen';
    if (!document.fullscreenElement) navigator.keyboard?.unlock?.();
    layout();
  });

  for (const id of ['desk-keys', 'desk-kbd', 'desk-clip-btn']) $(id).hidden = !control;
  canvas.style.cursor = control ? 'default' : 'not-allowed';
  new ResizeObserver(layout).observe(view);
  overlay.textContent = 'Waiting for the first picture...';
  overlay.hidden = false;
  if (control) input.focus({ preventScroll: true });

  return {
    frame,
    onScreen,
    onClipboard,
    notice: (msg) => toast(msg),
    ended(msg) {
      releaseAll();
      overlay.textContent = msg;
      overlay.hidden = false;
    },
  };
}

let toastTimer = null;
export function toast(msg, action, onAction) {
  const el = $('toast');
  $('toast-msg').textContent = msg;
  const btn = $('toast-action');
  btn.hidden = !action;
  if (action) {
    btn.textContent = action;
    btn.onclick = () => { el.hidden = true; onAction(); };
  }
  el.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { el.hidden = true; }, action ? 10000 : 3500);
}
