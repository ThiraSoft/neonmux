// QR scan with the page's own camera (not the iOS camera, which would open
// Safari and its separate storage). jsQR is vendored, loaded on first use.

let jsqr = null;

function loadJsQR() {
  if (!jsqr) {
    jsqr = new Promise((resolve, reject) => {
      if (globalThis.jsQR) { resolve(globalThis.jsQR); return; }
      const s = document.createElement('script');
      s.src = '/vendor/jsQR.js';
      s.onload = () => (globalThis.jsQR ? resolve(globalThis.jsQR) : reject(new Error('jsQR missing')));
      s.onerror = () => reject(new Error('jsQR missing'));
      document.head.appendChild(s);
    });
    jsqr.catch(() => { jsqr = null; });
  }
  return jsqr;
}

// startScan shows the rear camera in video and resolves with the text of
// the first neonmux QR code it reads. stop() ends it (and rejects).
export function startScan(video) {
  let stream = null, timer = 0, stopped = false, fail = null;
  const canvas = document.createElement('canvas');
  const ctx = canvas.getContext('2d', { willReadFrequently: true });
  const cleanup = () => {
    stopped = true;
    clearTimeout(timer);
    if (stream) stream.getTracks().forEach(t => t.stop());
    stream = null;
    video.srcObject = null;
  };
  const promise = new Promise((resolve, reject) => {
    fail = e => { cleanup(); reject(e); };
    (async () => {
      const decode = await loadJsQR();
      stream = await navigator.mediaDevices.getUserMedia({ video: { facingMode: { ideal: 'environment' } }, audio: false });
      if (stopped) { cleanup(); return; }
      video.muted = true;
      video.setAttribute('playsinline', '');
      video.srcObject = stream;
      await video.play();
      const tick = () => {
        if (stopped) return;
        if (video.readyState >= 2 && video.videoWidth) {
          const k = Math.min(1, 720 / Math.max(video.videoWidth, video.videoHeight));
          const w = Math.round(video.videoWidth * k), h = Math.round(video.videoHeight * k);
          canvas.width = w;
          canvas.height = h;
          ctx.drawImage(video, 0, 0, w, h);
          const code = decode(ctx.getImageData(0, 0, w, h).data, w, h, { inversionAttempts: 'attemptBoth' });
          if (code && typeof code.data === 'string' && code.data.startsWith('nmx1:')) {
            cleanup();
            resolve(code.data);
            return;
          }
        }
        timer = setTimeout(tick, 120);
      };
      tick();
    })().catch(e => fail(e));
  });
  return { promise, stop: () => fail(new Error('stopped')) };
}
