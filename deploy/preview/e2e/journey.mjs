// P-94: the Owner's phone checklist (P-84 7f/7g, simple part) run automatically
// against the internal preview, once per engine: Playwright Chromium with the
// Pixel 7 descriptor and Playwright WebKit with iPhone 15.
//
//   node journey.mjs <credsDir> <outDir>
//
// <credsDir> holds e2e-credentials.json (0600; the TOTP secret is enrolled on the
// first sign-in and written back) and tables.csv. <outDir> receives report.json,
// the run log and a screenshot for every failed step.
//
// Nothing secret is printed or written to the report: no password, no TOTP
// secret, no table URL or token, no cookies, and never a screenshot of the
// Keycloak sign-in or OTP-enrolment page.
import { createHmac } from 'node:crypto';
import { chmodSync, chownSync, mkdirSync, readFileSync, renameSync, statSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { chromium, devices, webkit } from 'playwright-core';

const BASE = 'https://tojima.jabiko.app';
const IDP = 'https://tojima-id.jabiko.app';
const TABLE_ID = 't04';
const TABLE_NAME = '4番卓';
const TENANT = 'preview-co';
const STORE = 'main';
const DISH = '日替わり定食';
const BEER = '生ビール';
const CURRY = 'カレーライス';
const DISH_PRICE = '1,100';

const [credsDir, outDir] = process.argv.slice(2);
if (!credsDir || !outDir) {
  console.error('usage: node journey.mjs <credsDir> <outDir>');
  process.exit(2);
}
mkdirSync(outDir, { recursive: true });
const credsPath = join(credsDir, 'e2e-credentials.json');

// ---------------------------------------------------------------------------
// Bookkeeping

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const stamp = (date = new Date()) => `${new Intl.DateTimeFormat('sv-SE', {
  timeZone: 'Asia/Tokyo', year: 'numeric', month: '2-digit', day: '2-digit',
  hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23',
}).format(date).replace(' ', 'T')}+09:00`;

const logLines = [];
function log(message) {
  const line = `[${stamp()}] ${message}`;
  logLines.push(line);
  console.log(line);
}

const creds = JSON.parse(readFileSync(credsPath, 'utf8'));
function saveTotpSecret(secret) {
  creds.totpSecret = secret;
  const tmp = join(credsDir, `.e2e-credentials.${process.pid}.tmp`);
  // The container runs as root: keep the file owned by the host user.
  const { uid, gid } = statSync(credsPath);
  writeFileSync(tmp, `${JSON.stringify(creds, null, 2)}\n`, { mode: 0o600 });
  chmodSync(tmp, 0o600);
  chownSync(tmp, uid, gid);
  renameSync(tmp, credsPath);
}

const tablesCsv = readFileSync(join(credsDir, 'tables.csv'), 'utf8').trim().split('\n').slice(1)
  .map((line) => line.split(','));
const tableRow = tablesCsv.find((row) => row[0] === TABLE_ID);
if (!tableRow) throw new Error(`${TABLE_ID} missing from tables.csv`);
const TABLE_URL = tableRow[2];

// ---------------------------------------------------------------------------
// TOTP: port of seed.py otp_code() (HMAC-SHA1, 30 s, 6 digits). Keycloak
// rejects a code replayed in the same window and one about to expire, so a
// code is only emitted in a fresh window that still has most of its life left.

const BASE32 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
let lastOtpWindow = -1;

function base32Decode(secret) {
  let bits = 0;
  let value = 0;
  const bytes = [];
  for (const character of secret.toUpperCase().replace(/=+$/, '')) {
    const index = BASE32.indexOf(character);
    if (index < 0) throw new Error('invalid base32 secret');
    value = (value << 5) | index;
    bits += 5;
    if (bits >= 8) {
      bytes.push((value >>> (bits - 8)) & 0xff);
      bits -= 8;
    }
  }
  return Buffer.from(bytes);
}

function hotp(secret, window) {
  const counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(window));
  const digest = createHmac('sha1', base32Decode(secret)).update(counter).digest();
  const offset = digest[digest.length - 1] & 15;
  const binary = ((digest[offset] & 0x7f) << 24) | (digest[offset + 1] << 16) | (digest[offset + 2] << 8) | digest[offset + 3];
  return String(binary % 1000000).padStart(6, '0');
}

async function otpCode() {
  if (!creds.totpSecret) throw new Error('no TOTP secret enrolled');
  let now = Date.now() / 1000;
  let window = Math.floor(now / 30);
  if (window <= lastOtpWindow || now % 30 > 25) {
    await sleep(((Math.max(window, lastOtpWindow) + 1) * 30 - now + 1) * 1000);
    now = Date.now() / 1000;
    window = Math.floor(now / 30);
  }
  lastOtpWindow = window;
  return hotp(creds.totpSecret, window);
}

// ---------------------------------------------------------------------------
// Small browser helpers

/** A page's current location without the query string: table URLs carry a token. */
function safePath(page) {
  try {
    const url = new URL(page.url());
    return `${url.origin}${url.pathname}`;
  } catch {
    return 'unknown';
  }
}

function onIdp(page) {
  return page.url().startsWith(IDP);
}

async function domSummary(page) {
  const info = await page.evaluate(() => ({
    inputs: [...document.querySelectorAll('input, select, textarea')].map((el) => ({
      name: el.getAttribute('name'), id: el.id, type: el.getAttribute('type') ?? el.tagName.toLowerCase(),
    })),
    buttons: [...document.querySelectorAll('input[type=submit], button')].map((el) => (el.getAttribute('value') || el.textContent || '').trim().slice(0, 40)),
    headings: [...document.querySelectorAll('h1, h2')].map((el) => (el.textContent || '').trim().slice(0, 60)),
  })).catch(() => null);
  return { path: safePath(page), ...(info ?? {}) };
}

async function shot(page, file) {
  if (!page || page.isClosed()) return null;
  // The Keycloak sign-in and enrolment pages show the password form and the
  // TOTP secret QR, so they are never captured.
  if (onIdp(page)) return null;
  try {
    // The staff notice can carry the table PIN and the guest entry card the
    // token; pixels cannot be redacted afterwards, so they are masked.
    const mask = [];
    for (const selector of ['.notice', 'input[data-control="guest-entry"]']) {
      const locator = page.locator(selector);
      if (await locator.count()) mask.push(locator);
    }
    await page.screenshot({ path: file, fullPage: false, mask });
    return file;
  } catch {
    return null;
  }
}

/** Table URLs carry a join token and the staff notice carries a table PIN; neither is reported. */
function redact(value) {
  return String(value)
    .replace(/([?&](?:table|session)=)[^\s&"']+/gi, '$1redacted')
    .replace(/(PIN[:：]\s*)\d+/gi, '$1***');
}

function redactDeep(value) {
  if (typeof value === 'string') return redact(value);
  if (Array.isArray(value)) return value.map(redactDeep);
  if (value && typeof value === 'object') return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, redactDeep(item)]));
  return value;
}

async function waitVisible(locator, timeout = 15000) {
  try {
    await locator.first().waitFor({ state: 'visible', timeout });
    return true;
  } catch {
    return false;
  }
}

async function text(page, locator) {
  try {
    return (await locator.first().innerText({ timeout: 3000 })).replace(/\s+/g, ' ').trim();
  } catch {
    return '';
  }
}

async function poll(fn, timeout, interval = 500) {
  const deadline = Date.now() + timeout;
  let last;
  while (Date.now() < deadline) {
    try {
      last = await fn();
      if (last) return last;
    } catch (cause) {
      last = cause;
    }
    await sleep(interval);
  }
  return false;
}

// ---------------------------------------------------------------------------
// Keycloak sign-in, OTP enrolment and step-up

async function submitKeycloak(page) {
  const submit = page.locator('input[type="submit"], button[type="submit"]').first();
  await submit.click({ timeout: 10000 });
}

async function fillKeycloak(page, locator, value, name) {
  try {
    await locator.fill(value, { timeout: 10000 });
    return true;
  } catch (cause) {
    // WebKit can navigate to the OTP form while a previous form is being
    // filled. Reclassify the current page instead of waiting on the old input.
    if (name !== 'OTP' && await page.locator('#otp').isVisible().catch(() => false)) return false;
    throw new Error(`Keycloak ${name} fill failed at ${safePath(page)}: ${String(cause.message).split('\n')[0]}`);
  }
}

async function keycloakFlow(page, run) {
  for (let round = 0; round < 24; round += 1) {
    if (!onIdp(page)) {
      if (page.url().startsWith(BASE)) {
        await page.waitForLoadState('domcontentloaded');
        return;
      }
      await page.waitForTimeout(500);
      continue;
    }
    const secret = page.locator('#kc-totp-secret-key');
    const enrollCode = page.locator('input[name="totp"]');
    const otp = page.locator('input[name="otp"]');
    const password = page.locator('input[name="password"]');
    const manual = page.locator('#mode-manual');
    if (await secret.count()) {
      const raw = await secret.first().innerText({ timeout: 5000 });
      const value = raw.replace(/[^A-Za-z2-7]/g, '').toUpperCase();
      if (!/^[A-Z2-7]{16,}$/.test(value)) throw new Error('OTP enrolment secret unreadable');
      saveTotpSecret(value);
      run.enrolled = true;
      if (!(await fillKeycloak(page, enrollCode, await otpCode(), 'enrolment OTP'))) continue;
      const device = page.locator('input[name="userLabel"]');
      if (await device.count() && !(await device.first().inputValue()) && !(await fillKeycloak(page, device.first(), 'e2e-bot', 'device label'))) continue;
      await submitKeycloak(page);
      await page.waitForLoadState('domcontentloaded');
      continue;
    }
    if (await enrollCode.count()) {
      // The enrolment page opens on the QR step; the manual step shows the secret.
      if (await manual.count()) {
        await manual.first().click();
        await page.waitForLoadState('domcontentloaded');
        continue;
      }
      throw new Error('OTP enrolment offers no manual secret');
    }
    if (await password.count()) {
      if (!(await fillKeycloak(page, page.locator('input[name="username"]'), creds.username, 'username'))) continue;
      if (!(await fillKeycloak(page, password, creds.password, 'password'))) continue;
      await submitKeycloak(page);
      await page.waitForLoadState('domcontentloaded');
      continue;
    }
    if (await otp.count()) {
      const code = await otpCode();
      // The code may have waited for a fresh window; a stale page cannot take it.
      if (!(await otp.first().fill(code, { timeout: 10000 }).then(() => true, () => false))) {
        await page.reload({ waitUntil: 'domcontentloaded' });
        continue;
      }
      await submitKeycloak(page);
      await page.waitForLoadState('domcontentloaded');
      continue;
    }
    await page.waitForTimeout(1000);
  }
  throw new Error(`Keycloak flow did not return to the app (${safePath(page)})`);
}

async function connectStaff(page) {
  const form = page.locator('form.staff-entry');
  if (!(await waitVisible(form, 30000))) {
    throw new Error(`staff connect form missing (${safePath(page)})`);
  }
  for (const [label, value] of [['企業ID', TENANT], ['店舗ID', STORE]]) {
    try {
      await page.getByLabel(label).fill(value, { timeout: 10000 });
    } catch (cause) {
      throw new Error(`staff connect ${label} fill failed at ${safePath(page)}: ${String(cause.message).split('\n')[0]}`);
    }
  }
  await page.getByRole('button', { name: '接続' }).click();
  if (!(await waitVisible(page.locator('nav.staff-tabs'), 30000))) {
    throw new Error(`staff workspace did not open (${safePath(page)})`);
  }
}

/** Sign in (password, then OTP enrolment or OTP), step up for MFA and connect. */
async function staffSignIn(page, run) {
  await page.goto(`${BASE}/staff/`, { waitUntil: 'domcontentloaded' });
  const login = page.getByRole('link', { name: 'ログイン' });
  if (await waitVisible(login, 30000)) {
    await login.click();
    await keycloakFlow(page, run);
  } else if (!(await waitVisible(page.locator('form.staff-entry'), 15000))) {
    throw new Error(`staff sign-in screen not recognised (${safePath(page)})`);
  }
  // STORE_MANAGER and TENANT_ADMIN actions need a strong login; the step-up
  // route is the same one the console's 再認証 link points at.
  await page.goto(`${BASE}/staff/auth/step-up?return=%2Fstaff%2F`, { waitUntil: 'domcontentloaded' });
  await keycloakFlow(page, run);
  run.stepUp = true;
  await page.goto(`${BASE}/staff/`, { waitUntil: 'domcontentloaded' });
  await connectStaff(page);
}

// ---------------------------------------------------------------------------
// Staff console helpers

const staffCard = (page) => page.locator('.staff-table-card').filter({ has: page.getByRole('heading', { name: TABLE_NAME, exact: true }) });
const closeDialog = (page) => page.locator('dialog.confirm-dialog');
const kitchenCards = (page) => page.locator('.staff-order-card').filter({ has: page.locator('.staff-order-head strong').filter({ hasText: /^4番卓 · #/ }) });

/** Reloads /staff/ and connects when the tab has no stored context yet. */
async function ensureStaff(page) {
  await page.goto(`${BASE}/staff/`, { waitUntil: 'domcontentloaded' });
  if (await waitVisible(page.locator('form.staff-entry'), 3000)) await connectStaff(page);
  else await waitVisible(page.locator('nav.staff-tabs'), 30000);
  return page;
}

async function staffGrid(page) {
  const back = page.getByRole('button', { name: '一覧に戻る' });
  if (await back.count() && await back.first().isVisible()) await back.first().click();
  await waitVisible(staffCard(page), 20000);
}

async function staffRefresh(page) {
  await page.getByRole('button', { name: '最新状態に更新' }).click();
  await page.waitForTimeout(800);
}

async function staffEnterTable(page) {
  if (!(await page.locator('.staff-detail').count())) {
    await staffCard(page).getByRole('button', { name: '詳細' }).click({ timeout: 15000 });
  }
  await waitVisible(page.locator('.staff-detail'), 20000);
  await staffRefresh(page);
}

async function settleCash(page) {
  const open = page.getByRole('button', { name: '現金を精算' });
  if (!(await open.count())) return { settled: false, reason: 'no settle control' };
  await open.first().click();
  const dialog = closeDialog(page);
  await waitVisible(dialog, 15000);
  const settle = dialog.getByRole('button', { name: '現金を確認して精算' });
  if (!(await waitVisible(settle, 15000))) {
    const due = await text(page, dialog);
    await dialog.getByRole('button', { name: 'キャンセル' }).click();
    return { settled: false, reason: due || 'no cash due' };
  }
  const register = dialog.getByLabel('レジ精算番号');
  if (await register.count()) await register.fill(`E2E-${Date.now().toString(36)}`);
  const tendered = dialog.getByLabel('お預かり金額');
  if (await tendered.count() && !(await tendered.inputValue())) await tendered.fill('99999');
  if (await settle.isDisabled()) {
    // The control is always on the detail screen; with nothing unpaid the
    // dialog only offers 精算対象の現金注文はありません。
    const due = await text(page, dialog);
    await dialog.getByRole('button', { name: 'キャンセル' }).click();
    return { settled: false, reason: due.slice(0, 120) || 'settle control disabled' };
  }
  await settle.click();
  const receipt = await poll(async () => (await text(page, dialog)).includes('精算済み'), 25000);
  if (!receipt) throw new Error('cash settlement did not confirm');
  await dialog.getByRole('button', { name: 'キャンセル' }).click();
  await dialog.waitFor({ state: 'hidden', timeout: 10000 }).catch(() => undefined);
  return { settled: true };
}

async function closeTable(page) {
  const close = page.getByRole('button', { name: 'テーブルを閉じる' });
  if (!(await close.count())) throw new Error('close-table control missing');
  await close.first().click();
  const dialog = closeDialog(page);
  await waitVisible(dialog, 15000);
  const confirm = dialog.getByRole('button', { name: '閉卓を確認' });
  if (await confirm.isDisabled()) {
    const blockers = await text(page, dialog);
    await dialog.getByRole('button', { name: 'キャンセル' }).click();
    throw new Error(`table not closable: ${blockers.slice(0, 120)}`);
  }
  await confirm.click();
  if (!(await poll(async () => !(await page.locator('.staff-detail').count()), 25000))) {
    throw new Error(`4番卓 did not close: ${(await text(page, page.locator('.staff-detail'))).slice(0, 120)}`);
  }
  return { closed: true };
}

// Finish all actionable orders before retrying the close blockers. The loop is
// bounded so an unrelated payment or review hold remains visible as a failure.
async function resetTable(page, kitchen) {
  let blockers = '';
  for (let round = 0; round < 60; round += 1) {
    await staffRefresh(page);
    const accept = page.locator('.staff-detail').getByRole('button', { name: '注文を受け付ける' });
    if (await accept.count()) { await accept.first().click(); continue; }
    await kitchen.goto(`${BASE}/kitchen/`, { waitUntil: 'domcontentloaded' });
    if (await waitVisible(kitchen.locator('form.staff-entry'), 3000)) await connectStaff(kitchen);
    const cards = kitchenCards(kitchen);
    const start = cards.getByRole('button', { name: '調理開始' });
    const ready = cards.getByRole('button', { name: '調理完了' });
    if (await start.count()) { await start.first().click(); await kitchen.waitForTimeout(800); continue; }
    if (await ready.count()) { await ready.first().click(); await kitchen.waitForTimeout(800); continue; }
    await staffRefresh(page);
    const serve = page.locator('.staff-detail').getByRole('button', { name: '提供済みにする' });
    if (await serve.count()) { await serve.first().click(); continue; }
    await settleCash(page);
    await staffRefresh(page);
    await page.getByRole('button', { name: 'テーブルを閉じる' }).click();
    const dialog = closeDialog(page);
    await waitVisible(dialog, 15000);
    const confirm = dialog.getByRole('button', { name: '閉卓を確認' });
    if (!(await confirm.isDisabled())) {
      await confirm.click();
      if (!(await poll(async () => !(await page.locator('.staff-detail').count()), 25000))) throw new Error('4番卓 did not close');
      return;
    }
    blockers = await text(page, dialog);
    await dialog.getByRole('button', { name: 'キャンセル' }).click();
    await page.waitForTimeout(800);
  }
  throw new Error(`table not closable after reset: ${blockers.slice(0, 160) || 'orders did not reach a closable state'}`);
}

async function openTable(page) {
  await staffGrid(page);
  const card = staffCard(page);
  const open = card.getByRole('button', { name: '開卓' });
  if (!(await open.count())) throw new Error('no 開卓 control on 4番卓');
  await open.click();
  await waitVisible(page.locator('.staff-detail'), 25000);
  const notice = await text(page, page.locator('.notice'));
  const pin = /PIN/.test(notice) ? (notice.match(/(\d{4,8})/) ?? [])[1] : undefined;
  return { pin };
}

// ---------------------------------------------------------------------------
// Guest helpers

async function joinTable(page, url, pin) {
  await page.goto(url, { waitUntil: 'domcontentloaded' });
  const add = page.locator('[data-add-product]').first();
  if (await waitVisible(add, 12000)) return 'auto';
  const join = page.getByRole('button', { name: '参加する' });
  if (!(await waitVisible(join, 20000))) {
    const shown = await text(page, page.locator('main'));
    throw new Error(`guest menu and join control both missing: ${shown.slice(0, 120)}`);
  }
  if (await join.isDisabled()) {
    const shown = await text(page, page.locator('main'));
    throw new Error(`table not joinable: ${shown.slice(0, 120)}`);
  }
  if (pin) await page.locator('input[data-control="guest-entry"]').first().fill(pin);
  await join.click();
  if (!(await waitVisible(add, 30000))) {
    const shown = await text(page, page.locator('main'));
    throw new Error(`menu did not open after joining: ${shown.slice(0, 120)}`);
  }
  return 'button';
}

async function finishOptionSheet(page) {
  const dialog = page.locator('[role="dialog"]');
  if (!(await dialog.isVisible().catch(() => false))) return 'direct';
  const groups = dialog.locator('fieldset[data-required-group]');
  const count = await groups.count();
  for (let index = 0; index < count; index += 1) {
    const group = groups.nth(index);
    const legend = await text(page, group.locator('legend'));
    if (!legend.includes('必須')) continue;
    if (await group.locator('input:checked').count()) continue;
    const option = group.locator('input:not([disabled])').first();
    if (await option.count()) await option.check({ force: true }).catch(() => undefined);
  }
  await dialog.getByRole('button', { name: 'カートに追加' }).click();
  await dialog.waitFor({ state: 'detached', timeout: 20000 }).catch(() => undefined);
  return 'sheet';
}

async function addProduct(page, name) {
  const row = page.getByRole('article', { name, exact: true });
  if (!(await waitVisible(row, 15000))) throw new Error(`menu row missing: ${name}`);
  const add = row.locator('[data-add-product]');
  if (!(await add.count())) throw new Error(`add control missing: ${name}`);
  // The button is disabled while a cart write is in flight; wait it out.
  if (!(await poll(async () => !(await add.isDisabled()), 10000))) throw new Error(`add control disabled: ${name}`);
  await add.click();
  return finishOptionSheet(page);
}

async function openCart(page) {
  const bar = page.locator('[data-testid="cart-bar"] button');
  if (!(await waitVisible(bar, 20000))) throw new Error('cart bar not shown');
  await bar.click();
  await waitVisible(page.locator('[role="dialog"]'), 10000);
}

async function cartLines(page) {
  return page.locator('[data-cart-line]').count();
}

async function submitCashOrder(page) {
  const previous = await page.locator('[data-order-status]').count();
  await openCart(page);
  const dialog = page.locator('[role="dialog"]');
  await dialog.getByRole('button', { name: '金額を確認する' }).click();
  const confirm = dialog.getByRole('button', { name: 'この内容で注文を確定' });
  if (!(await waitVisible(confirm, 25000))) {
    const shown = await text(page, dialog);
    throw new Error(`order could not be confirmed: ${shown.slice(0, 160)}`);
  }
  const cash = dialog.getByRole('radio', { name: '店頭で現金' });
  if (await cash.count() && !(await cash.isChecked())) await cash.check({ force: true });
  await confirm.click();
  await poll(async () => !(await dialog.isVisible().catch(() => false)), 30000);
  if (!(await poll(async () => (await page.locator('[data-order-status]').count()) > previous, 30000))) {
    throw new Error('new order status did not appear');
  }
}

async function orderStatus(page) {
  return text(page, page.locator('[data-order-status]').last());
}

// ---------------------------------------------------------------------------
// Menu availability through the real admin API with the staff cookie session.
// The store catalog console (/catalog/) cannot be connected with a Keycloak
// session: it only accepts a staff access token. The journey records that gap
// once (step 9 detail) and drives the same server endpoint the console uses.

async function storeMenu(page) {
  return page.evaluate(async () => {
    const response = await fetch('/v1/admin/tenants/preview-co/stores/main/menu?amounts=string', {
      credentials: 'same-origin', headers: { accept: 'application/json' },
    });
    return { status: response.status, body: await response.json().catch(() => null) };
  });
}

async function setCurry(page, productKey, status, version, reason) {
  return page.evaluate(async ({ productKey: key, status: next, version: expected, reason: note }) => {
    const response = await fetch('/v1/admin/tenants/preview-co/stores/main/menu/availability', {
      method: 'PUT',
      credentials: 'same-origin',
      headers: { accept: 'application/json', 'content-type': 'application/json' },
      body: JSON.stringify({
        target: { kind: 'PRODUCT', product_key: key },
        status: next,
        reason: note,
        expected_version: expected,
      }),
    });
    return { status: response.status, body: await response.json().catch(() => null) };
  }, { productKey, status, version, reason });
}

/** The product key is read from the store menu, so seed keys are never hard-coded. */
async function curryState(page) {
  const menu = (await storeMenu(page)).body;
  for (const category of menu?.categories ?? []) {
    for (const product of category.products ?? []) {
      if (product.name === CURRY) {
        return { key: product.key, availability: product.availability ?? 'PENDING_CONFIRMATION', version: product.availability_version ?? 0 };
      }
    }
  }
  throw new Error('カレーライス missing from the store menu');
}

async function pauseCurry(page) {
  const state = await curryState(page);
  if (state.availability === 'PAUSED') return { ...state, changed: false };
  const result = await setCurry(page, state.key, 'PAUSED', state.version, 'E2E sold-out check');
  if (result.status !== 200) throw new Error(`pause failed: ${result.status} ${JSON.stringify(result.body).slice(0, 120)}`);
  return { ...state, changed: true, version: result.body?.version ?? state.version + 1 };
}

async function resumeCurry(page) {
  const state = await curryState(page);
  if (state.availability === 'AVAILABLE') return { ...state, changed: false };
  const result = await setCurry(page, state.key, 'AVAILABLE', state.version, undefined);
  if (result.status !== 200) throw new Error(`resume failed: ${result.status} ${JSON.stringify(result.body).slice(0, 120)}`);
  return { ...state, changed: true };
}

// ---------------------------------------------------------------------------
// The journey

async function runEngine(engine, result) {
  const descriptor = devices[engine.device];
  if (!descriptor) throw new Error(`device descriptor missing: ${engine.device}`);
  const browser = await engine.launch();
  const contextOptions = { ...descriptor, locale: 'ja-JP', timezoneId: 'Asia/Tokyo' };
  const staffContext = await browser.newContext(contextOptions);
  const guestAContext = await browser.newContext(contextOptions);
  const guestBContext = await browser.newContext(contextOptions);
  const staff = await staffContext.newPage();
  const kitchen = await staffContext.newPage();
  const guestA = await guestAContext.newPage();
  const guestB = await guestBContext.newPage();
  const pages = [staff, kitchen, guestA, guestB];
  const state = { pin: undefined, run: {}, tableOpen: false };
  const portrait = descriptor.viewport ?? { width: 390, height: 844 };

  let shotTarget = staff;
  const step = async (n, name, fn, target = staff) => {
    const started = Date.now();
    const at = stamp();
    shotTarget = target;
    let detail = {};
    let error = '';
    try {
      detail = (await fn()) ?? {};
    } catch (cause) {
      error = String(cause?.message ?? cause).split('\n')[0].slice(0, 300);
    }
    const entry = { n, name, at, ms: Date.now() - started, status: error ? 'FAIL' : 'PASS', ...(error ? { error: redact(error) } : {}), detail: redactDeep(detail) };
    if (error) {
      const file = await shot(shotTarget, join(outDir, `${engine.name}-step${n}.png`));
      if (file) entry.screenshot = `${engine.name}-step${n}.png`;
      if (onIdp(shotTarget)) entry.page = redactDeep(await domSummary(shotTarget));
      else entry.mainText = redact((await text(shotTarget, shotTarget.locator('main'))).slice(0, 200));
    }
    result.steps.push(entry);
    log(`${engine.name} step ${n} ${entry.status} ${Date.now() - started} ms · ${name}${error ? ` · ${redact(error)}` : ''}`);
    return entry;
  };

  try {
    await step(1, 'Staff signs in on /staff/ (password, OTP, step-up) and reaches the tables screen', async () => {
      await staffSignIn(staff, state.run);
      if (!(await waitVisible(staffCard(staff), 30000))) throw new Error('4番卓 is not on the tables screen after sign-in');
      return { enrolled: state.run.enrolled === true, stepUp: state.run.stepUp === true, screen: await text(staff, staff.locator('h2')) };
    });

    await step(2, '4番卓 is closed (if left open) and then opened', async () => {
      await staffGrid(staff);
      let reset = false;
      if (await staffCard(staff).getByRole('button', { name: '詳細' }).count()) {
        await staffEnterTable(staff);
        await resetTable(staff, kitchen);
        reset = true;
      }
      const opened = await openTable(staff);
      state.pin = opened.pin;
      state.tableOpen = true;
      return { resetStaleSession: reset, pinRequired: Boolean(opened.pin) };
    });

    await step(3, 'Guest A opens the t04 link and sees the menu', async () => {
      const joined = await joinTable(guestA, TABLE_URL, state.pin);
      if (!(await waitVisible(guestA.getByRole('article', { name: DISH, exact: true }), 20000))) {
        throw new Error(`${DISH} missing from the guest menu`);
      }
      return { join: joined, heading: await text(guestA, guestA.locator('h1')) };
    }, guestA);

    await step(4, 'Guest A adds 日替わり定食 and 生ビール; Guest B sees both lines within 5 s', async () => {
      await addProduct(guestA, DISH);
      await addProduct(guestA, BEER);
      const at = Date.now();
      const joined = await joinTable(guestB, TABLE_URL, state.pin);
      await openCart(guestB);
      const lines = await poll(async () => {
        const count = await cartLines(guestB);
        return count >= 2 ? count : 0;
      }, 5000);
      if (!lines) throw new Error(`Guest B did not show both cart lines within 5 s (${await cartLines(guestB)})`);
      const sync = Date.now() - at;
      const names = await text(guestB, guestB.locator('[role="dialog"]'));
      await guestB.keyboard.press('Escape');
      if (!names.includes(DISH) || !names.includes(BEER)) {
        throw new Error(`Guest B cart does not show both products: ${names.slice(0, 160)}`);
      }
      return { guestBJoin: joined, lines, syncMs: sync, sawDish: true, sawBeer: true };
    }, guestB);

    await step(5, 'Guest A submits the cash order and sees an unpaid order status', async () => {
      await submitCashOrder(guestA);
      const status = await poll(async () => {
        const value = await orderStatus(guestA);
        return (value.includes('店員の受付待ち') || value.includes('受付済み')) && value.includes('未払い') ? value : 0;
      }, 35000);
      if (!status) throw new Error(`order status was "${await orderStatus(guestA)}"`);
      return { status };
    }, guestA);

    await step(6, 'Staff accepts the order if required and /kitchen/ shows it', async () => {
      await staffEnterTable(staff);
      const accept = staff.getByRole('button', { name: '注文を受け付ける' });
      const needsAcceptance = await accept.count() > 0;
      if (needsAcceptance) {
        await accept.first().click();
        const accepted = await poll(async () => !(await staff.getByRole('button', { name: '注文を受け付ける' }).count()), 25000);
        if (!accepted) throw new Error('order was not accepted');
      }
      await kitchen.goto(`${BASE}/kitchen/`, { waitUntil: 'domcontentloaded' });
      await connectStaff(kitchen);
      const card = kitchenCards(kitchen);
      if (!(await waitVisible(card, 30000))) throw new Error('kitchen board does not show the order');
      return { needsAcceptance, kitchenCard: (await text(kitchen, card)).slice(0, 100) };
    });

    await step(7, 'The order is served (kitchen start/ready, then served on /staff/) and Guest A updates within 35 s', async () => {
      const card = kitchenCards(kitchen);
      const start = card.getByRole('button', { name: '調理開始' });
      if (!(await waitVisible(start, 20000))) throw new Error('kitchen start control missing');
      await start.click();
      const ready = kitchenCards(kitchen).getByRole('button', { name: '調理完了' });
      if (!(await waitVisible(ready, 20000))) throw new Error('kitchen ready control missing');
      await ready.click();
      const kdsServed = await kitchenCards(kitchen).getByRole('button', { name: '提供済みにする' }).count();
      await staffEnterTable(staff);
      const serve = staff.getByRole('button', { name: '提供済みにする' });
      if (!(await waitVisible(serve, 25000))) throw new Error('served control missing on /staff/');
      await serve.click();
      const status = await poll(async () => {
        const value = await orderStatus(guestA);
        return value.includes('提供済み') ? value : 0;
      }, 35000);
      if (!status) throw new Error(`guest status was "${await orderStatus(guestA)}"`);
      return { kdsServedControl: kdsServed > 0, status };
    }, guestA);

    await step(8, 'Staff settles the cash and closes 4番卓; Guest A sees the session ended after reload', async () => {
      await staffEnterTable(staff);
      const settled = await settleCash(staff);
      if (!settled.settled) throw new Error(`cash settlement not offered: ${settled.reason ?? 'unknown'}`);
      await staffRefresh(staff);
      await closeTable(staff);
      state.tableOpen = false;
      await guestA.reload({ waitUntil: 'domcontentloaded' });
      const closed = await poll(async () => (await text(guestA, guestA.locator('main'))).includes('ご利用いただけません'), 30000);
      if (!closed) throw new Error(`guest page after close: ${(await text(guestA, guestA.locator('main'))).slice(0, 160)}`);
      return { settled: settled.settled, guestClosed: true };
    }, guestA);

    await step(9, 'カレーライス is sold out for the guest and available again afterwards', async () => {
      await staff.goto(`${BASE}/catalog/`, { waitUntil: 'domcontentloaded' });
      const catalogForm = await text(staff, staff.locator('main'));
      const tokenForm = await staff.getByLabel('テスト用アクセストークン').count();
      const paused = await pauseCurry(staff);
      await ensureStaff(staff);
      const opened = await openTable(staff);
      state.pin = opened.pin ?? state.pin;
      state.tableOpen = true;
      await joinTable(guestA, TABLE_URL, state.pin);
      const row = guestA.getByRole('article', { name: CURRY, exact: true });
      if (!(await waitVisible(row, 20000))) throw new Error('カレーライス missing from the menu');
      const add = row.locator('[data-add-product]');
      const disabled = await add.isDisabled();
      const label = await text(guestA, row);
      if (!disabled) throw new Error(`カレーライス is still orderable: ${label.slice(0, 120)}`);
      const resumed = await resumeCurry(staff);
      return {
        catalogConsole: tokenForm > 0 ? 'token-only (Keycloak session cannot connect it)' : catalogForm.slice(0, 60),
        soldOutVia: 'PUT /v1/admin/.../menu/availability',
        guestAddDisabled: disabled,
        guestLabel: label.includes('現在注文できません') ? '現在注文できません' : label.slice(0, 80),
        paused: paused.changed,
        resumed: resumed.changed,
      };
    }, guestA);

    await step(10, 'Traditional Chinese UI and the 1,100 price', async () => {
      await guestA.getByRole('button', { name: '繁中' }).click();
      if (!(await waitVisible(guestA.getByRole('heading', { name: '菜單' }), 15000))) throw new Error('Traditional Chinese menu heading missing');
      const row = guestA.getByRole('article', { name: DISH, exact: true });
      if (!(await waitVisible(row, 15000))) throw new Error(`${DISH} row missing after the locale switch`);
      const label = await text(guestA, row);
      if (!label.includes(DISH_PRICE)) throw new Error(`${DISH} price is not ${DISH_PRICE}: ${label.slice(0, 120)}`);
      const addNaming = await guestA.getByRole('button', { name: '加入購物車' }).count();
      if (!addNaming) throw new Error('加入購物車 controls missing');
      return { heading: '菜單', price: DISH_PRICE, addControls: addNaming };
    }, guestA);

    await step(11, 'Accessible names of the menu add controls, and where focus lands after adding', async () => {
      const labels = await guestA.evaluate(() => [...document.querySelectorAll('[data-add-product]')].map((button) => ({
        product: button.closest('article')?.getAttribute('aria-label') ?? '',
        name: (button.getAttribute('aria-label') || button.textContent || '').trim(),
        disabled: button.disabled,
      })));
      if (!labels.length) throw new Error('no add control on the menu');
      const unnamed = labels.filter((item) => !item.name || !item.product || !item.name.includes(item.product));
      const target = labels.findIndex((item) => !item.disabled);
      let focus = null;
      if (target >= 0) {
        const buttons = guestA.locator('[data-add-product]');
        await buttons.nth(target).focus();
        await guestA.keyboard.press('Enter');
        await guestA.waitForTimeout(1200);
        const inSheet = await guestA.locator('[role="dialog"]').isVisible().catch(() => false);
        focus = await guestA.evaluate(() => {
          const active = document.activeElement;
          return {
            tag: active?.tagName ?? null,
            name: (active?.getAttribute('aria-label') || active?.textContent || '').trim().slice(0, 40),
            inDialog: Boolean(active?.closest('[role="dialog"], dialog')),
            isBody: active === document.body,
          };
        });
        if (inSheet) { await guestA.keyboard.press('Escape'); await guestA.waitForTimeout(600); }
        if (!focus || focus.isBody) throw new Error(`focus after add landed on ${focus?.tag ?? 'nothing'}`);
      }
      if (unnamed.length) {
        // App bug, not a test bug: MenuList puts one constant aria-label
        // ("カートに追加" / "加入購物車") on every add button, so a screen reader
        // user hears the same control name on every menu row.
        const sample = unnamed.slice(0, 3).map((item) => `"${item.name}" for ${item.product || 'a nameless row'}`).join(', ');
        throw new Error(`${unnamed.length} of ${labels.length} add controls do not name their product: ${sample}`);
      }
      return {
        addControls: labels.length,
        namesContainingProduct: labels.length - unnamed.length,
        distinctNames: [...new Set(labels.map((item) => item.name))],
        focusAfterAdd: focus,
      };
    }, guestA);

    await step(12, 'Landscape rotation without horizontal overflow, and refetch after returning from the background', async () => {
      const measure = (page) => page.evaluate(() => ({
        scrollWidth: document.documentElement.scrollWidth,
        innerWidth: window.innerWidth,
        ok: document.documentElement.scrollWidth <= window.innerWidth,
      }));
      const results = {};
      for (const [name, page] of [['guest', guestA], ['staffTables', staff]]) {
        if (page === staff) {
          await ensureStaff(page);
          await staffGrid(page);
        }
        const values = {};
        for (const [label, size] of [['portrait', portrait], ['landscape', { width: portrait.height, height: portrait.width }], ['portraitAgain', portrait]]) {
          await page.setViewportSize(size);
          await page.waitForTimeout(700);
          values[label] = await measure(page);
        }
        await page.setViewportSize(portrait);
        results[name] = values;
      }
      const bad = Object.entries(results).flatMap(([name, values]) => Object.entries(values)
        .filter(([, value]) => !value.ok).map(([label, value]) => `${name}.${label} ${value.scrollWidth}>${value.innerWidth}`));
      if (bad.length) throw new Error(`horizontal overflow: ${bad.join(', ')}`);

      const calls = [];
      guestA.on('request', (request) => {
        try {
          const path = new URL(request.url()).pathname;
          if (path.startsWith('/v1/')) calls.push({ at: Date.now(), path });
        } catch { /* ignore */ }
      });
      await guestA.evaluate(() => {
        Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'visible' });
        Object.defineProperty(document, 'hidden', { configurable: true, get: () => false });
        document.dispatchEvent(new Event('visibilitychange'));
      });
      await sleep(1500);
      const hiddenAt = Date.now();
      await guestA.evaluate(() => {
        Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' });
        Object.defineProperty(document, 'hidden', { configurable: true, get: () => true });
        document.dispatchEvent(new Event('visibilitychange'));
      });
      await sleep(5000);
      const whileHidden = calls.filter((call) => call.at >= hiddenAt).length;
      if (whileHidden) throw new Error(`the app kept polling while hidden: ${whileHidden} /v1/ requests`);
      const shownAt = Date.now();
      await guestA.evaluate(() => {
        Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'visible' });
        Object.defineProperty(document, 'hidden', { configurable: true, get: () => false });
        document.dispatchEvent(new Event('visibilitychange'));
      });
      const refetch = await poll(async () => calls.find((call) => call.at >= shownAt), 30000, 250);
      if (!refetch) throw new Error('no /v1/ request after returning to the foreground');
      return {
        overflow: results,
        requestsWhileHidden: whileHidden,
        refetchMs: refetch.at - shownAt,
        refetchPath: refetch.path.replace(/[^/]+$/, ':id'),
      };
    }, guestA);
  } finally {
    // Cleanup: 4番卓 closed, カレーライス available, whatever the run did.
    const cleanup = [];
    try {
      const resumed = await resumeCurry(staff);
      cleanup.push({ item: 'karee-raisu available', ok: true, changed: resumed.changed });
    } catch (cause) {
      cleanup.push({ item: 'karee-raisu available', ok: false, error: String(cause?.message ?? cause).slice(0, 160) });
    }
    try {
      await ensureStaff(staff);
      await staffGrid(staff);
      if (await staffCard(staff).getByRole('button', { name: '詳細' }).count()) {
        await staffEnterTable(staff);
        await resetTable(staff, kitchen);
        cleanup.push({ item: 't04 closed', ok: true });
      } else {
        cleanup.push({ item: 't04 closed', ok: true, already: true });
      }
    } catch (cause) {
      cleanup.push({ item: 't04 closed', ok: false, error: String(cause?.message ?? cause).slice(0, 160) });
    }
    result.cleanup = cleanup;
    for (const page of pages) await page.close().catch(() => undefined);
    await browser.close().catch(() => undefined);
  }
}

// ---------------------------------------------------------------------------

const engines = [
  { name: 'chromium-pixel7', device: 'Pixel 7', launch: () => chromium.launch() },
  { name: 'webkit-iphone15', device: 'iPhone 15', launch: () => webkit.launch() },
].filter((engine) => !process.env.E2E_ONLY || engine.name.includes(process.env.E2E_ONLY));

if (!engines.length) {
  writeFileSync(join(outDir, 'report.json'), `${JSON.stringify({ base: BASE, table: TABLE_ID, startedAt: stamp(), engines: [], result: 'FAIL', error: `E2E_ONLY="${process.env.E2E_ONLY ?? ''}" matches no engine` }, null, 2)}\n`);
  console.log(`RESULT: FAIL 1 steps (E2E_ONLY="${process.env.E2E_ONLY ?? ''}" matches no engine)`);
  process.exit(1);
}

const report = { base: BASE, table: TABLE_ID, startedAt: stamp(), enrolled: false, engines: [], result: 'FAIL' };

for (const engine of engines) {
  const entry = { engine: engine.name, device: engine.device, startedAt: stamp(), steps: [], cleanup: [] };
  report.engines.push(entry);
  log(`engine ${engine.name} (${engine.device}) starting`);
  try {
    await runEngine(engine, entry);
  } catch (cause) {
    entry.fatal = redact(String(cause?.message ?? cause).split('\n')[0].slice(0, 300));
    log(`engine ${engine.name} failed to run: ${entry.fatal}`);
  }
  entry.finishedAt = stamp();
  entry.result = entry.steps.length > 0 && entry.steps.every((step) => step.status === 'PASS') && !entry.fatal ? 'PASS' : 'FAIL';
  log(`engine ${engine.name} ${entry.result}`);
}

report.enrolled = Boolean(creds.totpSecret);
const failed = report.engines.reduce((sum, entry) => sum + entry.steps.filter((step) => step.status === 'FAIL').length + (entry.fatal ? 1 : 0)
  + (entry.cleanup ?? []).filter((item) => !item.ok).length, 0);
report.result = failed === 0 ? 'PASS' : 'FAIL';
report.finishedAt = stamp();
writeFileSync(join(outDir, 'report.json'), `${JSON.stringify(report, null, 2)}\n`);
writeFileSync(join(outDir, 'run.log'), `${logLines.join('\n')}\n`);

const rows = report.engines.flatMap((entry) => (entry.steps.length ? entry.steps : [{ n: '-', name: entry.fatal ?? 'no steps', status: 'FAIL', ms: 0, at: entry.startedAt }])
  .map((step) => [entry.engine, String(step.n), step.name, step.status, `${step.ms} ms`, step.at, step.error ?? '']));
const widths = [0, 1, 2, 3, 4, 5].map((column) => Math.max(...rows.map((row) => String(row[column]).length), 0));
console.log('');
console.log(['engine', '#', 'step', 'result', 'time', 'finished (JST)'].map((head, column) => head.padEnd(widths[column])).join('  '));
for (const row of rows) {
  console.log(row.slice(0, 6).map((cell, column) => String(cell).padEnd(widths[column])).join('  ') + (row[6] ? `  ${row[6]}` : ''));
}
console.log('');
console.log(failed === 0 ? 'RESULT: PASS' : `RESULT: FAIL ${failed} steps`);
process.exit(failed === 0 ? 0 : 1);
