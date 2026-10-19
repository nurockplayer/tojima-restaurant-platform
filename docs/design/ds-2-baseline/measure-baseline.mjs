// DS-2 baseline measurement (P-89 §2): headless Chrome over CDP, no npm dependencies.
// Derived from ../ds-1-acceptance/capture.mjs. Usage: node measure-baseline.mjs <label> <baseURL> <credsJson> <outDir>
// Produces PNG screenshots, rendered contrast audit, overflow and focus evidence (report.json).
import { spawn } from 'node:child_process';
import { mkdirSync, readFileSync, writeFileSync, mkdtempSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';

const [label, base, credsPath, outRoot] = process.argv.slice(2);
const creds = JSON.parse(readFileSync(credsPath, 'utf8'));
const out = join(outRoot, label);
mkdirSync(out, { recursive: true });
const profile = mkdtempSync(join(tmpdir(), 'ds2-base-chrome-'));
const port = 9300 + Math.floor(Math.random() * 500);
const chrome = spawn('/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', [
  '--headless=new', `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`,
  '--no-first-run', '--no-default-browser-check', '--disable-extensions', '--hide-scrollbars', 'about:blank',
], { stdio: 'ignore' });

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
let ws, seq = 0;
const pending = new Map();
async function connect() {
  for (let i = 0; i < 50; i++) {
    try {
      const v = await (await fetch(`http://127.0.0.1:${port}/json/version`)).json();
      ws = new WebSocket(v.webSocketDebuggerUrl);
      await new Promise((r, j) => { ws.onopen = r; ws.onerror = j; });
      ws.onmessage = (e) => { const m = JSON.parse(e.data); if (m.id && pending.has(m.id)) { const p = pending.get(m.id); pending.delete(m.id); m.error ? p.j(new Error(JSON.stringify(m.error))) : p.r(m.result); } };
      return;
    } catch { await sleep(200); }
  }
  throw new Error('chrome did not start');
}
function send(method, params = {}, sessionId) {
  const id = ++seq;
  ws.send(JSON.stringify({ id, method, params, sessionId }));
  return new Promise((r, j) => pending.set(id, { r, j }));
}

async function newPage(width, height, mobile) {
  const { targetId } = await send('Target.createTarget', { url: 'about:blank' });
  const { sessionId } = await send('Target.attachToTarget', { targetId, flatten: true });
  const s = (m, p) => send(m, p, sessionId);
  await s('Page.enable'); await s('Runtime.enable');
  await s('Emulation.setDeviceMetricsOverride', { width, height, deviceScaleFactor: 2, mobile });
  if (mobile) await s('Emulation.setTouchEmulationEnabled', { enabled: true, maxTouchPoints: 5 });
  const evaluate = async (expr) => {
    const r = await s('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true });
    if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description ?? r.exceptionDetails.text);
    return r.result.value;
  };
  const goto = async (url) => { await s('Page.navigate', { url }); await sleep(1800); };
  const waitFor = async (sel, ms = 8000) => {
    const t0 = Date.now();
    while (Date.now() - t0 < ms) { if (await evaluate(`!!document.querySelector(${JSON.stringify(sel)})`)) return true; await sleep(150); }
    throw new Error(`timeout waiting for ${sel}`);
  };
  const shot = async (name, full = true) => {
    await sleep(350);
    const r = await s('Page.captureScreenshot', { format: 'png', captureBeyondViewport: full });
    writeFileSync(join(out, `${name}.png`), Buffer.from(r.data, 'base64'));
  };
  const key = async (k, code, keyCode, shift = false) => {
    const mod = shift ? 8 : 0;
    await s('Input.dispatchKeyEvent', { type: 'keyDown', key: k, code, windowsVirtualKeyCode: keyCode, modifiers: mod });
    await s('Input.dispatchKeyEvent', { type: 'keyUp', key: k, code, windowsVirtualKeyCode: keyCode, modifiers: mod });
  };
  const close = () => send('Target.closeTarget', { targetId });
  return { evaluate, goto, waitFor, shot, key, close };
}

const guestURL = creds.guest_url;
const report = { label, base, at: new Date().toISOString(), states: {} };
const M = `(() => {
  const vh = innerHeight, vw = innerWidth;
  const rect = (el) => { if (!el) return null; const r = el.getBoundingClientRect(); return { top: Math.round(r.top + scrollY), bottom: Math.round(r.bottom + scrollY), h: Math.round(r.height), w: Math.round(r.width) }; };
  const cards = [...document.querySelectorAll('.product-card')];
  const heights = cards.map((c) => c.getBoundingClientRect().height).sort((a, b) => a - b);
  const fully = cards.filter((c) => { const r = c.getBoundingClientRect(); return r.top >= 0 && r.bottom <= vh; }).length;
  const partly = cards.filter((c) => { const r = c.getBoundingClientRect(); return r.top < vh && r.bottom > 0; }).length;
  const sticky = [...document.querySelectorAll('body *')].filter((e) => ['sticky', 'fixed'].includes(getComputedStyle(e).position)).map((e) => e.tagName + '.' + String(e.className).slice(0, 30));
  const ls = document.querySelector('.locale-switch');
  return {
    viewport: vw + 'x' + vh,
    topbarBrand: document.querySelector('.topbar .brand')?.innerText.replace(/\\s+/g, ' '),
    h1: document.querySelector('h1')?.innerText,
    topbar: rect(document.querySelector('.topbar')), demoBanner: rect(document.querySelector('[aria-label="DEMO"]')),
    demoBanners: document.querySelectorAll('[aria-label="DEMO"]').length,
    welcome: rect(document.querySelector('.welcome')), tabs: rect(document.querySelector('.tabs')),
    cartPanel: rect(document.querySelector('.cart-panel')), menuSection: rect(document.querySelector('.menu-section')),
    firstCard: rect(cards[0]), cardsTotal: cards.length, cardsFullyInFirstViewport: fully, cardsPartlyInFirstViewport: partly,
    cardHeight: { min: Math.round(heights[0] ?? 0), median: Math.round(heights[Math.floor(heights.length / 2)] ?? 0), max: Math.round(heights.at(-1) ?? 0) },
    categories: document.querySelectorAll('.category').length,
    cardsWithOptions: cards.filter((c) => c.querySelector('fieldset')).length,
    requiredGroups: [...document.querySelectorAll('fieldset.option-group legend small')].length,
    disabledAddButtons: cards.filter((c) => c.querySelector('button.primary')?.disabled).length,
    images: document.querySelectorAll('main img').length,
    stickyOrFixed: sticky,
    scrollHeight: document.documentElement.scrollHeight, scrollWidth: document.documentElement.scrollWidth,
    localeSwitch: ls ? { w: Math.round(ls.getBoundingClientRect().width), h: Math.round(ls.getBoundingClientRect().height) } : null,
    poweredByTojima: /Powered by Tojima/i.test(document.body.innerText),
    platformWordmarks: (document.body.innerText.match(/東島|TOJIMA|Tojima/g) || []).length,
    eyebrows: [...document.querySelectorAll('.eyebrow')].map((e) => e.innerText),
    footer: document.querySelector('.workspace-footer')?.innerText,
    fontFamily: getComputedStyle(document.body).fontFamily.slice(0, 60),
  };
})()`;
async function tabsTo(p, predicate, max = 200) {
  await p.evaluate(`document.activeElement?.blur(); window.scrollTo(0,0); true`);
  for (let i = 1; i <= max; i++) { await p.key('Tab', 'Tab', 9); if (await p.evaluate(`(() => { const e = document.activeElement; return !!e && (${predicate})(e); })()`)) return i; }
  return null;
}
async function guest(locale, width, height) {
  const p = await newPage(width, height, width < 768);
  const tag = `guest-${locale}-${width}`;
  await p.goto(guestURL); await p.waitFor('.product-card');
  if (locale === 'zh') { await p.evaluate(`document.querySelector('.locale-switch').click()`); await sleep(500); }
  await p.evaluate('window.scrollTo(0,0)'); await sleep(200);
  const m = await p.evaluate(M);
  // Screenshot the true first viewport before Tab navigation scrolls the page.
  if (width !== 1280) await p.shot(`${tag}-first-viewport`, false);
  m.tabsToFirstEnabledAdd = await tabsTo(p, `(e) => e.matches('.product-card button.primary')`);
  // No Preview-button Tab count: with an empty cart the Preview button is disabled and unfocusable.
  // (The committed baseline-3c47f7a/report.json has tabsToPreviewButton: null for this reason; P-89 does not use it.)
  report.states[tag] = m;
  console.log(tag, JSON.stringify(m));
  await p.close();
}
async function ordersFlow() {
  const p = await newPage(375, 812, true);
  await p.goto(guestURL); await p.waitFor('.product-card');
  await p.evaluate(`(() => { const c = [...document.querySelectorAll('.product-card')].find((c) => !c.querySelector('fieldset') && c.querySelector('button.primary:not(:disabled)')); c.querySelector('button.primary').click(); return true; })()`);
  await sleep(1500);
  await p.evaluate(`document.querySelector('.checkout-button').click()`); await sleep(1500);
  await p.evaluate(`(() => { const b = [...document.querySelectorAll('.cart-panel button.primary.wide')].find((x) => !x.classList.contains('checkout-button') && !x.disabled); b.click(); return true; })()`);
  await sleep(2500);
  const o = await p.evaluate(`(() => ({ activeTab: document.querySelector('.tab.active')?.innerText, status: [...document.querySelectorAll('.guest-order-status')].map((e) => e.innerText), checkoutPanels: document.querySelectorAll('.checkout-status-panel').length, method: document.querySelector('.order-method')?.innerText, kicker: document.querySelector('.order-kicker')?.innerText, orderLinks: document.querySelectorAll('a[href*="/order"]').length }))()`);
  await p.shot('guest-ja-375-orders-viewport', false);
  report.states['orders-ja-375'] = o; console.log('orders', JSON.stringify(o));
  await p.close();
}
async function orderRead(width, locale) {
  const p = await newPage(width, 812, width < 768);
  await p.goto(base + '/order'); await sleep(800);
  const r = await p.evaluate(`(() => { const b = document.querySelector('main > button'); const cs = b && getComputedStyle(b); const r = b?.getBoundingClientRect(); return b ? { text: b.innerText, cls: b.className, w: Math.round(r.width), h: Math.round(r.height), fontSize: cs.fontSize, border: cs.borderTopWidth + ' ' + cs.borderTopStyle + ' ' + cs.borderTopColor, bg: cs.backgroundColor, radius: cs.borderTopLeftRadius, mainPadding: getComputedStyle(document.querySelector('main')).padding, scrollWidth: document.documentElement.scrollWidth } : null; })()`);
  await p.shot(`order-read-${locale}-${width}`, false);
  report.states[`order-read-${width}`] = r; console.log('order-read', width, JSON.stringify(r));
  await p.close();
}
async function jsWeight() {
  const p = await newPage(375, 812, true);
  await p.goto(guestURL); await p.waitFor('.product-card'); await sleep(500);
  const r = await p.evaluate(`performance.getEntriesByType('resource').filter((e) => e.name.endsWith('.js')).map((e) => ({ name: e.name.split('/').pop(), decoded: e.decodedBodySize, transfer: e.transferSize }))`);
  report.states['guest-js'] = r; console.log('js', JSON.stringify(r));
  await p.close();
}
try {
  await connect();
  for (const l of ['ja', 'zh']) for (const [w, h] of [[320, 700], [375, 812], [1280, 900]]) await guest(l, w, h);
  await jsWeight();
  await orderRead(375, 'ja'); await orderRead(1280, 'ja');
  await ordersFlow();
} catch (e) { report.error = String(e.stack || e); console.error(e); }
finally {
  writeFileSync(join(out, 'report.json'), JSON.stringify(report, null, 2));
  chrome.kill('SIGTERM'); await sleep(500); rmSync(profile, { recursive: true, force: true });
}
