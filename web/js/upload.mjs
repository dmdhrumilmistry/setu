// File upload to the host (see internal/host/upload.go): one data channel
// per file carries a header, the bytes and the host's receipt, so a large
// file never delays keystrokes or screen updates on the other channels.
const CHUNK = 32 * 1024;
const HIGH_WATER = 4 * 1024 * 1024;

// uploadFile sends one File and resolves with { path, sha256 } as saved on
// the host. onProgress receives the fraction sent.
export function uploadFile(pc, file, onProgress) {
  return new Promise((resolve, reject) => {
    const dc = pc.createDataChannel('setu-upload', { ordered: true });
    dc.binaryType = 'arraybuffer';
    dc.bufferedAmountLowThreshold = 1024 * 1024;
    let settled = false;
    const finish = (err, value) => {
      if (settled) return;
      settled = true;
      try { dc.close(); } catch { /* already closed */ }
      if (err) reject(err); else resolve(value);
    };
    dc.onopen = () => dc.send(JSON.stringify({ t: 'upload', name: file.name, size: file.size }));
    dc.onclose = () => finish(new Error('the upload was interrupted'));
    dc.onmessage = (e) => {
      let m;
      try { m = JSON.parse(e.data); } catch { return; }
      if (m.t === 'error') finish(new Error(m.msg));
      else if (m.t === 'upload-done') finish(null, { path: m.name, sha256: m.text });
      else if (m.t === 'upload-ready') send().catch((err) => finish(err));
    };
    async function send() {
      let sent = 0;
      const reader = file.stream().getReader(); // streamed: the file is never all in memory
      for (;;) {
        const { done, value } = await reader.read();
        if (done) return;
        for (let i = 0; i < value.length; i += CHUNK) {
          if (settled) return;
          if (dc.bufferedAmount > HIGH_WATER) await new Promise((r) => { dc.onbufferedamountlow = r; });
          const part = value.subarray(i, i + CHUNK);
          dc.send(part);
          sent += part.length;
          if (onProgress) onProgress(file.size ? sent / file.size : 1);
        }
      }
    }
  });
}

// uploadAll uploads files one after another, reporting through toast.
export async function uploadAll(files, upload, toast) {
  for (const file of files) {
    let shown = -1;
    toast(`Uploading ${file.name}...`);
    try {
      const { path } = await upload(file, (f) => {
        const pct = Math.floor(f * 100);
        if (pct >= shown + 5) { shown = pct; toast(`Uploading ${file.name}... ${pct}%`); }
      });
      toast(`Saved on the remote computer: ${path}`, 'Copy path', () => navigator.clipboard.writeText(path).catch(() => {}));
    } catch (err) {
      toast(`Could not upload ${file.name}: ${err.message || err}`);
    }
  }
}

// dropTarget makes el accept dropped files.
export function dropTarget(el, onFiles) {
  const over = (e) => {
    if (![...(e.dataTransfer?.types || [])].includes('Files')) return;
    e.preventDefault();
    el.classList.add('dropping');
  };
  el.addEventListener('dragenter', over);
  el.addEventListener('dragover', over);
  el.addEventListener('dragleave', (e) => { if (e.target === el) el.classList.remove('dropping'); });
  el.addEventListener('drop', (e) => {
    el.classList.remove('dropping');
    if (!e.dataTransfer?.files?.length) return;
    e.preventDefault();
    onFiles([...e.dataTransfer.files]);
  });
}
