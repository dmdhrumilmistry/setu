// Keyboard conventions between the viewer's device and the remote computer.
// Keys travel as physical keys (KeyboardEvent.code). The one convention
// that differs between Mac and PC keyboards is which modifier drives
// shortcuts: Cmd on a Mac, Ctrl elsewhere. When exactly one side is a Mac,
// Ctrl and Cmd are swapped so Ctrl+C on a PC is Cmd+C on a Mac host and
// Cmd+C on a Mac is Ctrl+C on a Windows or Linux host.

export const isMacPlatform = (platform) => /Mac|iPhone|iPad|iPod/i.test(platform || '');
export const isMacHost = (os) => os === 'darwin';

const SWAP = { ControlLeft: 'MetaLeft', ControlRight: 'MetaRight', MetaLeft: 'ControlLeft', MetaRight: 'ControlRight' };
export const MODIFIERS = new Set(['ShiftLeft', 'ShiftRight', 'ControlLeft', 'ControlRight', 'AltLeft', 'AltRight', 'MetaLeft', 'MetaRight']);

// crossPlatform reports whether Ctrl/Cmd swapping applies at all.
export const crossPlatform = (clientMac, hostOS) => clientMac !== isMacHost(hostOS);

// hostCode is the key to press on the host for a key pressed here.
export function hostCode(code, swap) {
  return (swap && SWAP[code]) || code;
}

// isShortcut reports whether an event uses this device's shortcut modifier
// (Cmd on a Mac, Ctrl elsewhere), as in copy (C), cut (X) and paste (V).
export function isShortcut(e, clientMac) {
  return clientMac ? e.metaKey : e.ctrlKey;
}

// combos are the key combinations offered in "Send keys...", written for
// the host's conventions (they are sent as is, never swapped).
export function combos(hostOS) {
  if (isMacHost(hostOS)) {
    return [
      ['Cmd+Space (Spotlight)', ['MetaLeft', 'Space']],
      ['Cmd+Tab (switch apps)', ['MetaLeft', 'Tab']],
      ['Cmd+Q (quit app)', ['MetaLeft', 'KeyQ']],
      ['Cmd+Option+Esc (Force Quit)', ['MetaLeft', 'AltLeft', 'Escape']],
      ['Cmd+Shift+3 (screenshot)', ['MetaLeft', 'ShiftLeft', 'Digit3']],
      ['Ctrl+Cmd+Q (lock screen)', ['ControlLeft', 'MetaLeft', 'KeyQ']],
      ['Esc', ['Escape']],
    ];
  }
  const win = hostOS === 'windows';
  return [
    [win ? 'Windows key' : 'Super key', ['MetaLeft']],
    ['Alt+Tab (switch windows)', ['AltLeft', 'Tab']],
    ['Alt+F4 (close window)', ['AltLeft', 'F4']],
    win ? ['Ctrl+Shift+Esc (Task Manager)', ['ControlLeft', 'ShiftLeft', 'Escape']] : ['Ctrl+Alt+T (terminal)', ['ControlLeft', 'AltLeft', 'KeyT']],
    win ? ['Windows+L (lock screen)', ['MetaLeft', 'KeyL']] : ['Super+L (lock screen)', ['MetaLeft', 'KeyL']],
    ['Print Screen', ['PrintScreen']],
    ['Esc', ['Escape']],
  ];
}

// chunks splits text into pieces of at most n UTF-16 units without
// cutting a surrogate pair (an emoji) in half.
export function chunks(text, n) {
  const out = [];
  for (let i = 0; i < text.length;) {
    let end = Math.min(i + n, text.length);
    if (end < text.length && /[\uD800-\uDBFF]/.test(text[end - 1])) end += end - 1 > i ? -1 : 1;
    out.push(text.slice(i, end));
    i = end;
  }
  return out;
}
