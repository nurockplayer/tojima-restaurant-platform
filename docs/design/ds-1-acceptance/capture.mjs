// DS-1 acceptance capture: headless Chrome over CDP, no npm dependencies.
// Usage: node capture.mjs <label> <baseURL> <credsJson> <outDir>
// Produces PNG screenshots, rendered contrast audit, overflow and focus evidence (report.json).
import { spawn } from 'node:child_process';
import { mkdirSync, readFileSync, writeFileSync, mkdtempSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';

const [label, base, credsPath, outRoot] = process.argv.slice(2);
const creds = JSON.parse(readFileSync(credsPath, 'utf8'));
const out = join(outRoot, label);
mkdirSync(out, { recursive: true });
const profile = mkdtempSync(join(tmpdir(), 'ds1-chrome-'));
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

// In-page helpers: React-safe form fill, rendered contrast audit, overflow.
const HELPERS = `
window.__fill = (inputs, values) => { const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
  inputs.forEach((el, i) => { set.call(el, values[i]); el.dispatchEvent(new Event('input', { bubbles: true })); }); };
window.__lum = (c) => { const m = c.match(/[\\d.]+/g).map(Number); const f = (v) => { v /= 255; return v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; };
  return 0.2126 * f(m[0]) + 0.7152 * f(m[1]) + 0.0722 * f(m[2]); };
window.__alpha = (c) => { const m = c.match(/[\\d.]+/g); return m && m.length > 3 ? Number(m[3]) : 1; };
window.__bg = (el) => { for (let e = el; e; e = e.parentElement) { const b = getComputedStyle(e).backgroundColor; if (__alpha(b) > 0.5) return b; } return 'rgb(255,255,255)'; };
window.__audit = () => { const rows = []; const seen = new Set();
  for (const el of document.querySelectorAll('body *')) {
    if (!el.childNodes.length || ![...el.childNodes].some((n) => n.nodeType === 3 && n.textContent.trim())) continue;
    const cs = getComputedStyle(el); const r = el.getBoundingClientRect();
    if (cs.visibility === 'hidden' || cs.display === 'none' || r.width === 0 || r.height === 0 || Number(cs.opacity) === 0) continue;
    if (el.closest('[hidden],[inert]')) continue;
    const fg = cs.color, bg = __bg(el); const a = __lum(fg), b = __lum(bg); const ratio = (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
    const size = parseFloat(cs.fontSize), bold = Number(cs.fontWeight) >= 700; const large = size >= 24 || (bold && size >= 18.66);
    if (parseFloat(cs.fontSize) === 0) continue; const disabled = el.closest('button:disabled,[aria-disabled="true"],input:disabled,select:disabled,fieldset:disabled');
    const need = large ? 3 : 4.5; const key = el.className + '|' + fg + '|' + bg + '|' + size + '|' + need + '|' + (disabled ? 'd' : 'e');
    if (seen.has(key)) continue; seen.add(key);
    rows.push({ text: el.textContent.trim().slice(0, 30), cls: String(el.className).slice(0, 60), fg, bg, size, ratio: Math.round(ratio * 100) / 100, need, disabled: !!disabled, pass: ratio >= need || !!disabled }); }
  for (const el of document.querySelectorAll('input:not([type=hidden]):not([type=checkbox]):not([type=radio]), select, textarea')) {
    const cs = getComputedStyle(el); const r = el.getBoundingClientRect();
    if (cs.visibility === 'hidden' || cs.display === 'none' || r.width === 0 || el.closest('[hidden],[inert]')) continue;
    const text = el.tagName === 'SELECT' ? (el.selectedOptions[0]?.textContent ?? '') : (el.value || el.placeholder || '');
    if (!text.trim()) continue;
    const fg = el.value ? cs.color : (getComputedStyle(el, '::placeholder').color || cs.color); const bg = __bg(el);
    const a = __lum(fg), b = __lum(bg); const ratio = (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05); const size = parseFloat(cs.fontSize);
    const disabled = el.disabled || !!el.closest('fieldset:disabled');
    rows.push({ text: '[control] ' + text.trim().slice(0, 24), cls: el.tagName + '.' + String(el.className).slice(0, 40), fg, bg, size, ratio: Math.round(ratio * 100) / 100, need: 4.5, disabled, pass: ratio >= 4.5 || disabled, control: true }); }
  return { total: rows.length, controls: rows.filter((r) => r.control).length, fails: rows.filter((r) => !r.pass), minFont: Math.min(...rows.map((r) => r.size)), small: rows.filter((r) => r.size < 12).map((r) => r.size + 'px ' + r.text) }; };
window.__overflow = () => ({ scrollWidth: document.documentElement.scrollWidth, innerWidth: window.innerWidth });
window.__focusInfo = () => { const e = document.activeElement; if (!e || e === document.body) return null; const cs = getComputedStyle(e); const r = e.getBoundingClientRect();
  const grow = Math.max(0, parseFloat(cs.outlineWidth) + parseFloat(cs.outlineOffset));
  const ring = { l: r.left - grow, t: r.top - grow, r: r.right + grow, b: r.bottom + grow };
  let clippedBy = null;
  for (let a = e.parentElement; a; a = a.parentElement) { const s = getComputedStyle(a); if (s.overflowX === 'visible' && s.overflowY === 'visible') continue;
    const ar = a.getBoundingClientRect(); if (ring.l < ar.left - 0.5 || ring.r > ar.right + 0.5 || ring.t < ar.top - 0.5 || ring.b > ar.bottom + 0.5) { clippedBy = String(a.className || a.tagName).slice(0, 40); break; } }
  return { tag: e.tagName, cls: String(e.className).slice(0, 50), text: (e.textContent || e.value || '').trim().slice(0, 20), outline: cs.outlineStyle + ' ' + cs.outlineWidth + ' ' + cs.outlineColor, offset: cs.outlineOffset, w: r.width, h: r.height, clippedBy }; };
true;`;

const report = { label, base, at: new Date().toISOString(), states: {} };
async function record(page, name, opts = {}) {
  await page.evaluate(HELPERS);
  await page.shot(name, opts.full !== false);
  const audit = await page.evaluate('__audit()');
  const overflow = await page.evaluate('__overflow()');
  const demoNotice = await page.evaluate(`/DEMO/.test(document.body.innerText) && /参考価格|參考價格/.test(document.body.innerText)`);
  report.states[name] = { audit, overflow, demoNotice };
  console.log(`${name}: texts=${audit.total} fails=${audit.fails.length} minFont=${audit.minFont} overflow=${overflow.scrollWidth}/${overflow.innerWidth}`);
}

const guestURL = creds.guest_url;
const clickText = (sel, re) => `(() => { const el = [...document.querySelectorAll(${JSON.stringify(sel)})].find((e) => ${re}.test(e.textContent)); if (!el) throw new Error('no ' + ${JSON.stringify(sel)} + ' ' + ${JSON.stringify(String(re))}); el.click(); return true; })()`;

async function guest(locale, width, height) {
  const p = await newPage(width, height, width < 768);
  const tag = `guest-${locale}-${width}`;
  await p.goto(guestURL);
  await p.waitFor('.product-card');
  if (locale === 'zh') { await p.evaluate(`document.querySelector('.locale-switch').click()`); await sleep(400); }
  await record(p, `${tag}-01-menu`);
  // Add a plain item (no required option) and one with the required rice option.
  await p.evaluate(`(() => { const cards = [...document.querySelectorAll('.product-card')]; const plain = cards.find((c) => !c.querySelector('fieldset') && c.querySelector('button.primary:not(:disabled)')); plain.querySelector('button.primary').click(); return true; })()`);
  await sleep(1200);
  await p.evaluate(`(() => { const c = [...document.querySelectorAll('.product-card')].find((c) => c.querySelector('fieldset input[type=radio]')); if (c) { c.querySelector('fieldset input[type=radio]').click(); } return true; })()`);
  await sleep(300);
  await p.evaluate(`(() => { const c = [...document.querySelectorAll('.product-card')].find((c) => c.querySelector('fieldset input[type=radio]:checked')); if (c) c.querySelector('button.primary').click(); return true; })()`);
  await sleep(1400);
  await p.evaluate(`window.scrollTo(0, 0)`);
  await record(p, `${tag}-02-cart`);
  // Keyboard: focus the first stepper button and capture the viewport.
  {
    const hasStepper = await p.evaluate(`(() => { const b = document.querySelector('.stepper button:not(:disabled)'); if (!b) return false; b.scrollIntoView({ block: 'center' }); b.focus(); return true; })()`);
    report.states[`${tag}-stepper-present`] = hasStepper;
    // Re-enter the button by keyboard so :focus-visible applies.
    await p.key('Tab', 'Tab', 9, true); await p.key('Tab', 'Tab', 9);
    report.states[`${tag}-focus-stepper`] = { focus: await p.evaluate('__focusInfo()') };
    await p.shot(`${tag}-03-focus-stepper`, false);
    // Raise quantity so "−" is enabled, then keyboard-focus it.
    await p.evaluate(`(() => { const b = [...document.querySelectorAll('.stepper button:not(:disabled)')].find((x) => /[＋+]/.test(x.textContent)); if (b) b.click(); return !!b; })()`); await sleep(2000);
    const minus = await p.evaluate(`(() => { const b = [...document.querySelectorAll('.stepper button:not(:disabled)')].find((x) => /[−-]/.test(x.textContent)); if (!b) return false; b.focus(); return true; })()`);
    if (minus) { await p.key('Tab', 'Tab', 9); await p.key('Tab', 'Tab', 9, true); report.states[`${tag}-focus-stepper-minus`] = { focus: await p.evaluate('__focusInfo()') }; await p.shot(`${tag}-03b-focus-stepper-minus`, false); }
    else report.states[`${tag}-focus-stepper-minus`] = { focus: null, note: 'minus not enabled' };
  }
  await p.evaluate(`document.querySelector('.checkout-button').click()`);
  await sleep(1500);
  await record(p, `${tag}-04-preview`);
  await p.evaluate(`(() => { const b = [...document.querySelectorAll('.cart-panel button.primary.wide')].find((x) => !x.classList.contains('checkout-button') && !x.disabled); if (b) b.click(); return !!b; })()`);
  await sleep(2500);
  await p.evaluate(`(() => { const t = [...document.querySelectorAll('.tabs .tab')]; if (t[1]) t[1].click(); return true; })()`);
  await sleep(1200);
  await record(p, `${tag}-05-orders`);
  // Link focus (my-orders ORDER_READ link) and the ORDER_READ page itself.
  const href = await p.evaluate(`(() => { const a = [...document.querySelectorAll('a[href*="/order"]')][0]; if (!a) return null; a.scrollIntoView({ block: 'center' }); a.focus(); return a.href; })()`);
  if (href) {
    await p.key('Tab', 'Tab', 9, true); await p.key('Tab', 'Tab', 9);
    report.states[`${tag}-focus-link`] = { focus: await p.evaluate('__focusInfo()') };
    await p.shot(`${tag}-05b-focus-link`, false);
    await p.goto(href); await sleep(1500);
    if (locale === 'zh') { await p.evaluate(`(() => { const b = [...document.querySelectorAll('button')].find((x) => /繁|中文/.test(x.textContent)); if (b) b.click(); return true; })()`); await sleep(500); }
    await record(p, `${tag}-06-order-read`);
  } else report.states[`${tag}-order-read`] = 'no ORDER_READ link found';
  await p.close();
}

async function staffLogin(p, path, role) {
  await p.goto(base + path);
  await p.waitFor('form input');
  await p.evaluate(HELPERS);
  const c = creds.credentials[role];
  await p.evaluate(`__fill([...document.querySelectorAll('form input')].slice(0, 3), ${JSON.stringify([creds.tenant_id, creds.store_id, c.token])}); true`);
  await p.evaluate(`document.querySelector('form').requestSubmit(); true`);
  await sleep(2500);
}

async function staff(width, height, locale) {
  const p = await newPage(width, height, width < 768);
  const tag = `staff-${locale}-${width}`;
  await staffLogin(p, '/staff', 'cashier');
  if (locale === 'zh') { await p.evaluate(`(() => { const b = document.querySelector('.locale-switch'); if (b) b.click(); return true; })()`); await sleep(500); }
  await p.waitFor('.staff-table-card');
  await record(p, `${tag}-01-tables`);
  await p.evaluate(`(() => { const card = [...document.querySelectorAll('.staff-table-card')].find((c) => /05/.test(c.textContent)) || document.querySelector('.staff-table-card'); card.querySelector('button').click(); return true; })()`);
  await sleep(2000);
  await record(p, `${tag}-02-detail`);
  // Open the first dialog-launching action available (cash settlement / close preview).
  // Cash settlement dialog (labels: 現金を精算 / 現金（デモ・テスト専用） / 現金結帳 / 現金（展示測試專用）).
  const opened = await p.evaluate(`(async () => { const b = [...document.querySelectorAll('button:not(:disabled)')].find((x) => /^現金/.test(x.textContent.trim()));
    if (!b) return null; b.click(); for (let i = 0; i < 20; i++) { await new Promise((r) => setTimeout(r, 200)); if (document.querySelector('dialog[open]')) return b.textContent.trim(); } return 'clicked-no-dialog:' + b.textContent.trim(); })()`);
  report.states[`${tag}-dialog-opened-by`] = opened;
  if (opened) {
    await record(p, `${tag}-03-dialog`, { full: false });
    const steps = [];
    for (let i = 0; i < 6; i++) { await p.key('Tab', 'Tab', 9); const f = await p.evaluate('__focusInfo()'); steps.push(f); if (i === 0) await p.shot(`${tag}-04-dialog-focus`, false); if (f && /BUTTON/.test(f.tag)) await p.shot(`${tag}-04-dialog-focus-${i}`, false); }
    report.states[`${tag}-dialog-focus`] = { focus: steps[0], steps };
    await p.key('Escape', 'Escape', 27); await sleep(600);
  }
  if (locale === 'ja') {
    // Release one order to the kitchen so the KDS capture shows a real card.
    const accepted = await p.evaluate(`(async () => { const b = [...document.querySelectorAll('button:not(:disabled)')].find((x) => /注文を受け付ける|確認接單/.test(x.textContent)); if (!b) return false; b.click(); await new Promise((r) => setTimeout(r, 1500)); return true; })()`);
    report.states[`${tag}-accepted-order`] = accepted;
  }
  await p.close();
}

async function switchLocale(p, locale) { if (locale === 'zh') { await p.evaluate(`(() => { const b = document.querySelector('.locale-switch'); if (b) b.click(); return true; })()`); await sleep(600); } }
async function kds(width, height, locale = 'ja') {
  const p = await newPage(width, height, width < 768);
  await staffLogin(p, '/kitchen', 'kitchen');
  await switchLocale(p, locale);
  await sleep(1500);
  await record(p, `kds-${locale}-${width}`);
  await p.close();
}

async function catalog(width, height, locale = 'ja') {
  const p = await newPage(width, height, width < 768);
  await staffLogin(p, '/catalog', 'catalog_admin');
  await switchLocale(p, locale);
  await sleep(1500);
  await record(p, `catalog-${locale}-${width}`);
  if (locale === 'ja' && width === 1280) {
    const ok = await p.evaluate(`(() => { const t = document.querySelector('textarea:not(:disabled)'); if (!t) return false; t.scrollIntoView({ block: 'center' }); t.focus(); return true; })()`);
    if (ok) { await p.key('Tab', 'Tab', 9, true); await p.key('Tab', 'Tab', 9); report.states['catalog-focus-textarea'] = { focus: await p.evaluate('__focusInfo()') }; await p.shot('catalog-ja-1280-focus-textarea', false); }
    else report.states['catalog-focus-textarea'] = { focus: null, note: 'no enabled textarea' };
  }
  await p.close();
}

async function narrow() {
  const p = await newPage(320, 700, true);
  await p.goto(guestURL); await p.waitFor('.product-card');
  await record(p, 'guest-ja-320-menu');
  await p.close();
}

try {
  await connect();
  for (const [w, h] of [[375, 812], [1280, 900]]) for (const l of ['ja', 'zh']) await guest(l, w, h);
  await narrow();
  for (const [w, h] of [[1280, 900], [375, 812]]) for (const l of ['ja', 'zh']) await staff(w, h, l);
  for (const [w, h] of [[1280, 900], [375, 812]]) for (const l of ['ja', 'zh']) { await kds(w, h, l); await catalog(w, h, l); }
} catch (e) {
  report.error = String(e.stack || e);
  console.error(e);
} finally {
  writeFileSync(join(out, 'report.json'), JSON.stringify(report, null, 2));
  chrome.kill('SIGTERM');
  await sleep(500);
  rmSync(profile, { recursive: true, force: true });
}
