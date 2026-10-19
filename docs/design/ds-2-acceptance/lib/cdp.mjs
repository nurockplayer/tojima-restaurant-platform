// Minimal CDP driver for the DS-2 acceptance harness. Zero npm dependencies (Node 24 built-in WebSocket).
import { spawn } from 'node:child_process';
import { mkdtempSync, rmSync, writeFileSync, readFileSync, mkdirSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const HERE = dirname(fileURLToPath(import.meta.url));
const PROBES = readFileSync(join(HERE, 'page-probes.js'), 'utf8');

const KEYS = {
  Tab: { key: 'Tab', code: 'Tab', vk: 9 },
  Enter: { key: 'Enter', code: 'Enter', vk: 13, text: '\r' },
  Escape: { key: 'Escape', code: 'Escape', vk: 27 },
  Space: { key: ' ', code: 'Space', vk: 32, text: ' ' },
  ArrowDown: { key: 'ArrowDown', code: 'ArrowDown', vk: 40 },
  ArrowUp: { key: 'ArrowUp', code: 'ArrowUp', vk: 38 },
  ArrowRight: { key: 'ArrowRight', code: 'ArrowRight', vk: 39 },
  ArrowLeft: { key: 'ArrowLeft', code: 'ArrowLeft', vk: 37 },
  Shift: { key: 'Shift', code: 'ShiftLeft', vk: 16 },
};

export class Chrome {
  constructor({ chromePath = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' } = {}) {
    this.chromePath = chromePath;
    this.seq = 0;
    this.pending = new Map();
    this.listeners = new Map(); // sessionId -> Set<fn(method, params)>
  }

  async start() {
    this.profile = mkdtempSync(join(tmpdir(), 'ds2-accept-chrome-'));
    this.port = 9300 + Math.floor(Math.random() * 500);
    this.proc = spawn(this.chromePath, [
      '--headless=new', `--remote-debugging-port=${this.port}`, `--user-data-dir=${this.profile}`,
      '--no-first-run', '--no-default-browser-check', '--disable-extensions', '--hide-scrollbars',
      '--disable-background-timer-throttling', '--disable-renderer-backgrounding', '--disable-backgrounding-occluded-windows',
      'about:blank',
    ], { stdio: 'ignore' });
    for (let i = 0; i < 60; i++) {
      try {
        const v = await (await fetch(`http://127.0.0.1:${this.port}/json/version`)).json();
        this.ws = new WebSocket(v.webSocketDebuggerUrl);
        await new Promise((res, rej) => { this.ws.onopen = res; this.ws.onerror = rej; });
        this.ws.onmessage = (e) => this.#onMessage(JSON.parse(e.data));
        this.version = v.Browser;
        return;
      } catch { await sleep(200); }
    }
    throw new Error('chrome did not start');
  }

  #onMessage(m) {
    if (m.id && this.pending.has(m.id)) {
      const p = this.pending.get(m.id);
      this.pending.delete(m.id);
      m.error ? p.rej(new Error(`${p.method}: ${JSON.stringify(m.error)}`)) : p.res(m.result);
    } else if (m.method) {
      const set = this.listeners.get(m.sessionId || '');
      if (set) for (const fn of set) fn(m.method, m.params);
    }
  }

  send(method, params = {}, sessionId) {
    const id = ++this.seq;
    this.ws.send(JSON.stringify({ id, method, params, sessionId }));
    return new Promise((res, rej) => this.pending.set(id, { res, rej, method }));
  }

  on(sessionId, fn) {
    if (!this.listeners.has(sessionId)) this.listeners.set(sessionId, new Set());
    this.listeners.get(sessionId).add(fn);
    return () => this.listeners.get(sessionId)?.delete(fn);
  }

  /** Open an isolated page (own browser context: no shared localStorage between flows). */
  async newPage({ width, height, outDir, scripts = [], config = {} }) {
    const self = this;
    const mobile = width < 768;
    const { browserContextId } = await this.send('Target.createBrowserContext');
    const { targetId } = await this.send('Target.createTarget', { url: 'about:blank', browserContextId });
    const { sessionId } = await this.send('Target.attachToTarget', { targetId, flatten: true });
    const s = (m, p) => this.send(m, p, sessionId);
    await s('Page.enable'); await s('Runtime.enable'); await s('Network.enable');
    await s('Emulation.setDeviceMetricsOverride', { width, height, deviceScaleFactor: 2, mobile });
    await s('Emulation.setFocusEmulationEnabled', { enabled: true });
    if (mobile) await s('Emulation.setTouchEmulationEnabled', { enabled: true, maxTouchPoints: 5 });
    await s('Page.addScriptToEvaluateOnNewDocument', { source: `window.__cfg = ${JSON.stringify(config)};\n${PROBES}` });
    for (const src of scripts) await s('Page.addScriptToEvaluateOnNewDocument', { source: src });

    // ---- network + console logging ----
    const net = [];
    const errors = [];
    const byId = new Map();
    this.on(sessionId, (method, p) => {
      if (method === 'Network.requestWillBeSent') {
        const rec = { id: p.requestId, t: Date.now(), method: p.request.method, url: p.request.url, status: null };
        net.push(rec); byId.set(p.requestId, rec);
      } else if (method === 'Network.responseReceived') {
        const rec = byId.get(p.requestId); if (rec) rec.status = p.response.status;
      } else if (method === 'Network.loadingFailed') {
        const rec = byId.get(p.requestId); if (rec) { rec.failed = p.errorText; }
      } else if (method === 'Runtime.exceptionThrown') {
        errors.push({ t: Date.now(), text: (p.exceptionDetails.exception?.description || p.exceptionDetails.text || '').slice(0, 300) });
      } else if (method === 'Runtime.consoleAPICalled' && p.type === 'error') {
        errors.push({ t: Date.now(), text: 'console.error: ' + p.args.map((a) => a.value ?? a.description ?? '').join(' ').slice(0, 300) });
      }
    });

    const evaluate = async (expr) => {
      const r = await s('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true });
      if (r.exceptionDetails) throw new Error((r.exceptionDetails.exception?.description ?? r.exceptionDetails.text) + '\n  in: ' + String(expr).slice(0, 200));
      return r.result.value;
    };
    const call = (fn, ...args) => evaluate(`__h.${fn}(${args.map((a) => JSON.stringify(a === undefined ? null : a)).join(', ')})`);

    const page = {
      sessionId, targetId, net, errors, evaluate, call, outDir,
      mark: () => net.length,
      since: (mark) => net.slice(mark),
      async goto(url, settle = 1500) {
        await s('Page.navigate', { url });
        await sleep(300);
        for (let i = 0; i < 40; i++) { if ((await evaluate('document.readyState')) === 'complete') break; await sleep(150); }
        await sleep(settle);
      },
      async waitFor(expr, ms = 8000, what = expr) {
        const t0 = Date.now();
        while (Date.now() - t0 < ms) { try { if (await evaluate(`!!(${expr})`)) return Date.now() - t0; } catch { /* page mid-navigation */ } await sleep(120); }
        throw new Error(`timeout waiting for ${String(what).slice(0, 160)}`);
      },
      async waitUntil(fn, ms = 8000, step = 150) {
        const t0 = Date.now();
        while (Date.now() - t0 < ms) { const v = await fn(); if (v) return { value: v, elapsed: Date.now() - t0 }; await sleep(step); }
        return { value: null, elapsed: Date.now() - t0 };
      },
      async shot(name, { full = false } = {}) {
        await sleep(350);
        mkdirSync(join(outDir, 'shots'), { recursive: true });
        const r = await s('Page.captureScreenshot', { format: 'png', captureBeyondViewport: full });
        const file = join(outDir, 'shots', `${name}.png`);
        writeFileSync(file, Buffer.from(r.data, 'base64'));
        return `shots/${name}.png`;
      },
      /** Dispatch a key press. opts.shift adds the Shift modifier; opts.repeat repeats. */
      async key(name, { shift = false } = {}) {
        const k = KEYS[name]; if (!k) throw new Error('unknown key ' + name);
        const modifiers = shift ? 8 : 0;
        await s('Input.dispatchKeyEvent', { type: k.text ? 'keyDown' : 'rawKeyDown', key: k.key, code: k.code, windowsVirtualKeyCode: k.vk, modifiers, ...(k.text ? { text: k.text } : {}) });
        await s('Input.dispatchKeyEvent', { type: 'keyUp', key: k.key, code: k.code, windowsVirtualKeyCode: k.vk, modifiers });
      },
      /** A real mouse click on the centre of the first element matching `spec` (hit-tested). Falls back to el.click() when the centre is covered. */
      async click(spec, { force = false } = {}) {
        const pt = await call('point', spec);
        if (!pt) throw new Error('click: no element for ' + JSON.stringify(spec));
        await sleep(60);
        if (!pt.ok && !force) {
          const ok = await call('softClick', spec);
          return { via: 'el.click()', covered: true, hit: pt.hit, target: pt.target, ok };
        }
        await s('Input.dispatchMouseEvent', { type: 'mouseMoved', x: pt.x, y: pt.y });
        await s('Input.dispatchMouseEvent', { type: 'mousePressed', x: pt.x, y: pt.y, button: 'left', clickCount: 1 });
        await s('Input.dispatchMouseEvent', { type: 'mouseReleased', x: pt.x, y: pt.y, button: 'left', clickCount: 1 });
        return { via: 'mouse', covered: false, target: pt.target, size: { w: pt.w, h: pt.h } };
      },
      /** Keyboard-activate an element: focus it (keyboard modality so :focus-visible applies) then press Enter or Space. */
      async activate(spec, k = 'Enter') {
        await keyboardFocus(spec);
        await page.key(k);
      },
      setOffline: (offline) => s('Network.emulateNetworkConditions', { offline, latency: 0, downloadThroughput: -1, uploadThroughput: -1 }),
      /** Leave matching requests pending (no offline event, no response): used to exercise the sync watchdog. */
      async hangRequests(urlPattern) {
        page._paused = [];
        page._failer = self.on(sessionId, (method, p) => { if (method === 'Fetch.requestPaused') page._paused.push(p.requestId); });
        await s('Fetch.enable', { patterns: [{ urlPattern, requestStage: 'Request' }] });
      },
      /** Answer matching requests with a fixed status (e.g. 410 to expire the guest session). */
      async failRequests(urlPattern, status, body = '{"error":"session_expired"}') {
        page._failer = self.on(sessionId, (method, p) => { if (method === 'Fetch.requestPaused') s('Fetch.fulfillRequest', { requestId: p.requestId, responseCode: status, responseHeaders: [{ name: 'content-type', value: 'application/json' }], body: Buffer.from(body).toString('base64') }).catch(() => {}); });
        await s('Fetch.enable', { patterns: [{ urlPattern, requestStage: 'Request' }] });
      },
      async releaseRequests() {
        if (page._failer) { page._failer(); page._failer = null; }
        for (const id of page._paused || []) await s('Fetch.continueRequest', { requestId: id }).catch(() => {});
        page._paused = [];
        await s('Fetch.disable').catch(() => {});
      },
      async responseBody(requestId) { const r = await s('Network.getResponseBody', { requestId }); return r.base64Encoded ? Buffer.from(r.body, 'base64').toString() : r.body; },
    };
    page.close = async () => { try { await this.send('Target.closeTarget', { targetId }); } catch { /* ignore */ } try { await this.send('Target.disposeBrowserContext', { browserContextId }); } catch { /* ignore */ } };

    const keyboardFocus = async (spec) => {
      // A Shift keypress flips Chrome's focus-visible heuristic to "keyboard", so a programmatic focus() shows the ring.
      await page.key('Shift');
      const ok = await evaluate(`(() => { const el = __h.first(${JSON.stringify(spec)}); if (!el) return false; el.scrollIntoView({ block: 'center', inline: 'center', behavior: 'instant' }); el.focus(); return document.activeElement === el; })()`);
      if (!ok) throw new Error('keyboardFocus: cannot focus ' + JSON.stringify(spec));
      await sleep(80);
      return true;
    };
    page.keyboardFocus = keyboardFocus;
    return page;
  }

  async stop() {
    try { this.ws?.close(); } catch { /* ignore */ }
    this.proc?.kill('SIGTERM');
    await sleep(500);
    if (this.profile) rmSync(this.profile, { recursive: true, force: true });
  }
}
