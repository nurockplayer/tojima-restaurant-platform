#!/usr/bin/env node
// DS-2 rendered-acceptance harness (P-89 section 5.4 / 5.5). Headless Chrome over CDP, zero npm dependencies.
//
//   node capture.mjs <before|after|topbar> <baseUrl> <credsFile> <outDir> [--combos=ja:375,zh:320] [--skip-ds1] [--only-ds1] [--skip-keyboard] [--skip-degraded]
// D1 topbar mode: node capture.mjs topbar <baseUrl> <credsFile> <outDir> --demo=false --expect-store-name=<text|none> --combos=ja:375,zh:375,ja:320,zh:320
// Use a non-demo operationstest fixture with a seeded guest_display_name (or NULL for "none"); credentials remain in the owner-only file.
//
// Writes <outDir>/report-<label>.json, <outDir>/shots/*.png and <outDir>/summary-<label>.md.
// Needs Node 24 (built-in WebSocket) and Google Chrome at /Applications/Google Chrome.app.
// Run only against an isolated demo (operationstest -demo-shironagasukujira -catalog-fixture) backed by a disposable PostgreSQL.
// The credentials file is read only to log in to staff/KDS/catalog and to accept orders through the staff API; it is never printed.
//
// Layout: this file = CONFIG (top) + flows. lib/page-probes.js = in-page measurement code, lib/cdp.mjs = CDP driver,
// lib/api.mjs = server-side cart/staff helpers, lib/evaluate.mjs = acceptance criteria + markdown summary, compare.mjs = before/after diff.
import { readFileSync, writeFileSync, mkdirSync, readdirSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { createHash } from 'node:crypto';
import { gzipSync } from 'node:zlib';
import { fileURLToPath } from 'node:url';
import { Chrome, sleep } from './lib/cdp.mjs';
import { makeApi } from './lib/api.mjs';
import { evaluateReport, renderSummary } from './lib/evaluate.mjs';

const HERE = dirname(fileURLToPath(import.meta.url));

// =====================================================================================================================
// CONFIG  --  EVERYTHING THAT DEPENDS ON THE (NOT YET WRITTEN) AFTER UI LIVES IN UI_AFTER BELOW.  Adjust only that object.
// =====================================================================================================================
// A selector spec is one of:
//   'css'                                  plain CSS
//   { css, role, name, text, within, withinText, has, lacks, not, inside, outside, enabled, pos, bottomFixed, any, first, last, nth }
//        role   = explicit role attribute or implicit role of the tag (button, link, radio, heading, dialog, ...)
//        name   = regex source tested against the accessible name (aria-label / labelledby / label / text)
//        text   = regex source tested against innerText
//        within = spec | '@sheet' (top-most role=dialog) | '@productRows' | '@<selKey>' (e.g. '@cartBar' = whatever sel.cartBar resolves to);
//                 withinText narrows the scopes to those containing that text (exact heading match preferred)
//        pos    = 'sticky|fixed' (computed position); bottomFixed = element sits at the bottom of the viewport
//   [spec, spec, ...]                      fallback chain: the first spec that matches anything wins
// "selectorHealth" (health in every state of report-<label>.json, and the "selector-health" check) shows how many elements each
// key matched, so when the real UI lands you can see at a glance which guesses need adjusting.  null = does not exist.
// rx.* are regex SOURCES (case-insensitive); alternations cover ja|zh; quoted from P-89 section 3 wording.
// focusProbes: key -> { state, spec } = element whose focus ring is checked for clipping (`clippedBy`) in that state.
// ---------------------------------------------------------------------------------------------------------------------
const UI_AFTER = {
  name: 'after',
  productRowFallback: true,                         // if sel.productRow matches nothing: rows = li/article around buttons whose text has a yen price
  rowActionCss: 'button, [role=button]',            // first ENABLED non-radio control inside a product row = direct add / open option sheet
  rx: {
    storeName: 'シロナガスクジラ',                                                           // DEMO store-name constant shared with DemoBanner (3.9)
    selectRequired: '必須オプションを選択してください|請選擇必選項目',                         // existing copy.selectRequired (3.5)
    degradedNotice: '通信が不安定です|網路連線不穩',                                          // page / sheet notice (3.8)
    degradedTime: '(\\d{1,2}:\\d{2})\\s*(時点|的內容|的内容)',                               // "HH:mm 時点" / "HH:mm 的內容"; group 1 = HH:mm
    degradedBar: '同期待ち|等待同步',                                                         // cart-bar text while degraded
    syncOk: 'テーブルで共有中|同桌共用中',                                                    // cart-bar text while healthy
    oldLatest: '最新の共有内容|最新共用內容|同じテーブルと共有中|與同桌即時共用',                 // BEFORE only: always-on "latest" claims (the defect)
    cookWaiting: '店員の受付待ち|等待店員接單',                                               // cooking track right after confirm (3.7)
    cookAccepted: '受付済み|待製作',                                                          // cooking track after staff accepts
    payUnpaid: '未払い|尚未付款',                                                             // payment track (cash, UNPAID)
    trackCooking: '調理|製作',                                                                // track label (aria-label / heading text) of the cooking list
    trackPayment: 'お支払い|付款|支払',                                                       // ... and of the payment list
    previewHeading: '金額を確認|確認金額|ご注文内容|確認訂單|確認',                              // heading that must receive focus on entering preview (3.6)
    cartHeading: '共有カート|共用購物車|カート|購物車',                                        // heading that must receive focus after auto-cancel / back (3.6)
    menuHeading: 'お品書き|菜單',                                                             // heading that must receive focus after the cart is emptied (3.2)
    orderHeading: '確定した注文|已成立訂單|注文|訂單',                                          // heading that must receive focus after a successful confirm (3.2)
  },
  sel: {
    root: ['main[data-surface="guest"]', '[data-guest-root]', 'main'],                                       // guest root (font-family check)
    topbar: ['[data-guest-topbar], [data-testid="guest-topbar"]', 'main[data-surface="guest"] > header'],   // Shell(): first child header = store/table + locale button
    footer: ['[data-guest-footer]', { css: 'footer', outside: '[role="dialog"]', last: true }],       // footer.workspace-footer (sheet footers live inside the dialog and are excluded)
    localeSwitch: [{ css: 'button, a', name: '^\\s*(繁中|繁體中文|日本語)', outside: '[role="dialog"]', first: true }],
    categoryNav: ['nav[data-category-nav]', '[data-guest-category-nav], [data-testid="category-nav"]', { css: 'nav', pos: 'sticky|fixed', outside: '[role="dialog"]' }],
    categoryNavItem: { within: '@categoryNav', css: 'a, button, [role=tab], [role=link]' },
    categoryHeading: ['h2[data-category-key]', { css: 'h2', outside: 'nav, [role="dialog"]' }],
    cartBar: ['[data-testid="cart-bar"]', '[data-guest-cart-bar], [data-cart-bar]',
      { css: 'div, section, aside, footer, nav, form', pos: 'fixed', bottomFixed: true, lacks: '[role="dialog"]', not: '[role="dialog"]', outside: '[role="dialog"]', text: '[¥￥—]' }],
    cartBarOpen: [{ within: '@cartBar', css: 'button, [role=button], a', includeScope: true, enabled: true, first: true }],  // opens the cart sheet
    productRow: ['article[data-product-key]', '[data-guest-product], [data-product-key], [data-testid="product-row"]', 'li[class*="product" i]', 'article[class*="product" i]'],
    sheetClose: { within: '@sheet', css: 'button, [role=button]', name: '^(閉じる|關閉|close)|(閉じる|關閉)$' },
    sheetTitle: { within: '@sheet', css: 'h1, h2, h3, [slot="title"]' },
    optionGroup: { within: '@sheet', css: 'fieldset' },
    optionLabel: { within: '@sheet', css: 'label', has: 'input[type=radio], input[type=checkbox]' },          // visible label wrapping a radio/checkbox (clicked by the harness)
    optionInput: { within: '@sheet', css: 'input[type=radio], input[type=checkbox], [role=radio], [role=checkbox]', any: true },
    sheetQuantity: [{ within: '@sheet', css: 'select, input[type=number], [role=spinbutton]' }, { within: '@sheet', css: 'button', name: '[+＋]|増やす|增加' }],
    sheetAdd: { within: '@sheet', css: 'button', name: '^\\s*(カートに追加|加入購物車|追加|加入)', first: true },
    cartLine: ['[data-cart-line]', '[data-guest-cart-line], [data-testid="cart-line"]', { within: '@sheet', css: 'li, article', has: 'button', text: '[¥￥]' }],  // optional: line count falls back to number of "−" buttons
    lineRemove: { within: '@sheet', css: 'button', name: '^\\s*(削除|移除)' },
    stepperMinus: { within: '@sheet', css: 'button', name: '[−-]\\s*$|減らす|減少|减少' },
    stepperPlus: { within: '@sheet', css: 'button', name: '[+＋]\\s*$|増やす|增加' },
    previewBtn: { css: 'button', name: '^\\s*(金額を確認する|確認金額)' },
    confirmBtn: { css: 'button', name: '^\\s*(この内容で注文を確定|確認並送出訂單)' },
    backBtn: { css: 'button', name: '(カートへ戻る|返回購物車)' },
    orderCard: ['[data-order-card]', '[data-guest-order], [data-testid="order-card"]', '.order-card', 'article[class*="order" i]'],      // newest order card; falls back to the parent of [aria-current="step"]
    ordersToggle: { css: 'button', name: '注文履歴|已送出訂單', outside: '[role="dialog"]', first: true },
    menuBack: { css: 'button, a', name: 'メニューに戻る|返回菜單', first: true },
    orderReadLang: { css: 'button', name: '^\\s*(繁體中文|日本語)\\s*$', first: true },                              // ORDER_READ language button
    orderReadLink: 'a[href*="/order#"]',
    degradedNotice: { css: '[role="status"], [role="alert"], [role="note"], p, div', text: '通信が不安定です|網路連線不穩', lacks: '[role="status"], [role="alert"], [role="note"], p, div', first: true },   // element carrying the degraded notice text
  },
  focusProbes: {
    categoryItem: { state: 'menu-with-cart', spec: { within: '@categoryNav', css: 'a, button, [role=tab], [role=link]', first: true } },
    productAction: { state: 'menu-with-cart', spec: { css: 'button, [role=button]', within: '@productRows', enabled: true, first: true } },
    sheetClose: { state: 'option-sheet', spec: { within: '@sheet', css: 'button, [role=button]', name: '^(閉じる|關閉|close)|(閉じる|關閉)$', first: true } },
    radio: { state: 'option-sheet', spec: { within: '@sheet', css: 'input[type=radio], input[type=checkbox]', any: true, first: true } },
    quantity: { state: 'option-sheet', spec: [{ within: '@sheet', css: 'select, input[type=number], [role=spinbutton]', first: true }, { within: '@sheet', css: 'button', name: '[+＋]|増やす|增加', first: true }] },
    add: { state: 'option-sheet', spec: { within: '@sheet', css: 'button', name: '^\\s*(カートに追加|加入購物車|追加|加入)', enabled: true, first: true } },
    cartBar: { state: 'menu-with-cart', spec: [{ within: '@cartBar', css: 'button, [role=button], a', includeScope: true, enabled: true, first: true }] },
    stepperMinus: { state: 'cart-sheet', spec: { within: '@sheet', css: 'button', name: '[−-]\\s*$|減らす|減少|减少', enabled: true, first: true } },
    stepperPlus: { state: 'cart-sheet', spec: { within: '@sheet', css: 'button', name: '[+＋]\\s*$|増やす|增加', enabled: true, first: true } },
    confirm: { state: 'confirm-preview', spec: { css: 'button', name: '^\\s*(この内容で注文を確定|確認並送出訂單)', enabled: true, first: true } },
    orderReadLang: { state: 'order-read', spec: { css: 'button', name: '^\\s*(繁體中文|日本語)\\s*$', first: true } },
  },
};

// ---------------------------------------------------------------------------------------------------------------------
// BEFORE (origin/main 712ba28 / 3c47f7a, class-based).  Stable; not part of the director's adjustment surface.
// ---------------------------------------------------------------------------------------------------------------------
const UI_BEFORE = {
  name: 'before',
  productRowFallback: false,
  rowActionCss: 'button.primary',                  // old product card: the single primary add button
  rx: UI_AFTER.rx,
  sel: {
    root: 'main.app-shell', topbar: '.topbar', footer: '.workspace-footer', localeSwitch: '.locale-switch',
    categoryNav: null, categoryNavItem: null, categoryHeading: '.category h3', cartBar: null, cartBarOpen: null,
    productRow: '.product-card',
    sheetClose: null, sheetTitle: null, optionGroup: '.product-card fieldset.option-group', optionLabel: '.product-card label.option', optionInput: '.product-card .option input',
    sheetQuantity: '.product-card .quantity-select select', sheetAdd: '.product-card button.primary',
    cartLine: '.cart-line', lineRemove: { css: '.cart-line button', name: '^(削除|移除)' }, stepperMinus: { css: '.stepper button', name: '[−-]\\s*$' }, stepperPlus: { css: '.stepper button', name: '[+＋]\\s*$' },
    previewBtn: { css: 'button.checkout-button' }, confirmBtn: { css: '.cart-panel button.primary.wide', name: '^(この内容で注文を確定|確認並送出訂單)' },
    backBtn: { css: '.cart-panel button', name: 'カートへ戻る|返回購物車' },
    orderCard: '.order-card', ordersToggle: { css: '.tabs .tab', nth: 1 }, menuBack: { css: '.tabs .tab', nth: 0 },
    orderReadLang: { css: 'main > button', name: '^(繁體中文|日本語)$' }, orderReadLink: 'a[href*="/order#"]',
    degradedNotice: null,
  },
  focusProbes: {
    categoryItem: null,
    productAction: { state: 'menu-with-cart', spec: { css: 'button.primary', within: '@productRows', enabled: true, first: true } },
    sheetClose: null,
    radio: { state: 'option-sheet', spec: { css: '.option input[type=radio]', within: '@productRows', first: true, any: true } },
    quantity: { state: 'option-sheet', spec: { css: '.quantity-select select', within: '@productRows', first: true } },
    add: { state: 'option-sheet', spec: { css: 'button.primary', within: '@productRows', enabled: true, first: true } },
    cartBar: null,
    stepperMinus: { state: 'cart-sheet', spec: { css: '.stepper button', name: '[−-]\\s*$', enabled: true, first: true } },
    stepperPlus: { state: 'cart-sheet', spec: { css: '.stepper button', name: '[+＋]\\s*$', enabled: true, first: true } },
    confirm: { state: 'confirm-preview', spec: { css: '.cart-panel button.primary.wide', name: '^(この内容で注文を確定|確認並送出訂單)', enabled: true } },
    orderReadLang: { state: 'order-read', spec: { css: 'main > button', first: true } },
  },
};

// Timing and sizes (not UI specific).
const TIMING = {
  degradedWithinMs: 7500, degradedProbeMs: 12000,   // UX-5: degraded notice must appear within 7.5 s of going offline
  recoverWithinMs: 5000, recoverProbeMs: 10000,     // UX-5: cleared within 5 s after coming back online
  ordersPollMs: 26000,                              // orders poll is 20 s: wait for the accepted state at most this long
  sheetAnimationMs: 500,
};
const VIEWPORTS = [[375, 812], [1280, 900], [320, 700]];
const LOCALES = ['ja', 'zh'];
const SHEET_TAB_PRESSES = 20;                       // P-89 5.5: 20x Tab and 20x Shift+Tab
// =====================================================================================================================
// END CONFIG
// =====================================================================================================================

const argv = process.argv.slice(2);
const flags = Object.fromEntries(argv.filter((a) => a.startsWith('--')).map((a) => {
  const flag = a.slice(2);
  const separator = flag.indexOf('=');
  return separator < 0 ? [flag, true] : [flag.slice(0, separator), flag.slice(separator + 1)];
}));
const [label, base, credsPath, outRoot] = argv.filter((a) => !a.startsWith('--'));
if (!label || !base || !credsPath || !outRoot) { console.error('usage: node capture.mjs <before|after|topbar> <baseUrl> <credsFile> <outDir> [--combos=ja:375,...] [--skip-ds1|--only-ds1] [--skip-keyboard] [--skip-degraded]'); process.exit(2); }
const UI = (flags.ui || label) === 'before' ? UI_BEFORE : UI_AFTER;
const IS_AFTER = UI === UI_AFTER;
const creds = JSON.parse(readFileSync(credsPath, 'utf8'));
const out = outRoot;
mkdirSync(join(out, 'shots'), { recursive: true });
const api = makeApi(creds);
const guestURL = creds.guest_url;
const homeURL = base.replace(/\/$/, '') + '/';

const report = {
  meta: { label, base, ui: UI.name, at: new Date().toISOString(), node: process.version, flags },
  combos: {}, ds1: { states: {}, summary: null }, js: null, notes: [],
};

const rxOf = (k) => new RegExp(UI.rx[k], 'i');
const compactAudit = (a) => ({ total: a.total, controls: a.controls, fails: a.fails, minFont: a.minFont, small: a.small, disabledRows: a.disabledRows });
const shortStop = (s) => (s.none ? { none: true } : { i: s.i, tag: s.tag, role: s.role, name: s.name, w: s.w, h: s.h, ovNav: s.overlapNav, ovBar: s.overlapBar, ovNavBar: s.overlapNavBar, ovAny: s.overlapAnyFixed, hidNavBar: s.fullyHiddenNavBar, hidAny: s.fullyHiddenAny, occ: s.occluders, centerOcc: s.centerOccluded, off: s.offscreenFraction, sheet: s.insideSheet, pa: s.isProductAction, dis: s.disabled });

// ---------------------------------------------------------------------------------------------------------------------
// Measurement primitives
// ---------------------------------------------------------------------------------------------------------------------
function pageConfig(locale, width, height) { return { ui: { sel: UI.sel, rx: UI.rx, productRowFallback: UI.productRowFallback }, label, locale, width, height }; }

/** Tab through the page (scope 'page': from the document start until the order cycles) or a sheet (scope 'sheet': N x Tab then N x Shift+Tab). */
async function tabTraverse(p, scope) {
  const stops = [];
  const escapes = [];
  let firstProductAction = null, cycleAt = null;
  if (scope === 'page') {
    await p.call('resetFocusStart');
    const seen = new Set();
    for (let i = 1; i <= 160; i++) {
      await p.key('Tab');
      const s = await p.call('stop');
      if (s.none) { stops.push({ i, none: true }); break; }
      s.i = i;
      if (seen.has(s.key)) { cycleAt = i; break; }
      seen.add(s.key); stops.push(s);
      if (firstProductAction == null && s.isProductAction) firstProductAction = i;
    }
  } else {
    // sheet: start from wherever the sheet put the focus
    for (const dir of ['forward', 'backward']) {
      for (let i = 1; i <= SHEET_TAB_PRESSES; i++) {
        await p.key('Tab', { shift: dir === 'backward' });
        const s = await p.call('stop');
        s.i = i; s.dir = dir;
        if (s.none || !s.insideSheet) escapes.push({ dir, i, at: s.none ? 'body/none' : `${s.tag} ${s.name}` });
        stops.push(s);
      }
    }
  }
  const real = stops.filter((s) => !s.none);
  const unique = new Set(real.map((s) => s.key));
  return {
    scope, presses: stops.length, uniqueStops: unique.size, cycleAt,
    tabsToFirstProductAction: firstProductAction,
    firstStop: real[0] ? `${real[0].tag} ${real[0].name}` : null,
    escapes, escapeCount: escapes.length,
    fullyHiddenNavBar: real.filter((s) => s.fullyHiddenNavBar).length,
    fullyHiddenAnyFixed: real.filter((s) => s.fullyHiddenAny).length,
    partiallyHidden: real.filter((s) => s.overlapAnyFixed > 0.05 && !s.fullyHiddenAny).length,
    maxOverlapNavBar: real.reduce((m, s) => Math.max(m, s.overlapNavBar ?? 0), 0),
    maxOverlapAnyFixed: real.reduce((m, s) => Math.max(m, s.overlapAnyFixed ?? 0), 0),
    centerOccluded: real.filter((s) => s.centerOccluded && !s.sheet).length,
    stops: stops.map(shortStop),
  };
}

/** One state = shot + AA audit + target sizes + overflow + DEMO + fonts + rows + sticky + topbar + (optional) Tab traversal. */
async function capture(p, c, name, o = {}) {
  await sleep(o.settle ?? 450);
  const m = await p.call('measure');
  const audit = compactAudit(await p.call('audit'));
  const targets = await p.call('targets');
  const shot = o.noShot ? null : await p.shot(`${c.tag}-${name}`, { full: !!o.full });
  const st = { name, notApplicable: !!o.na, equivalent: o.equiv ?? null, shot, measure: m, audit, targets, extra: o.extra ?? undefined };
  if (o.tab) st.tab = await tabTraverse(p, o.tab);
  if (o.na) st.notApplicableReason = o.na;
  c.states[name] = st;
  const t = st.tab;
  console.log(`  [${c.tag}] ${name}${o.na ? ' (N/A: old equivalent)' : ''}: aaFail=${audit.fails.length} minFont=${audit.minFont} overflow=${m.overflow.scrollWidth}/${m.overflow.deviceWidth}${m.overflow.over ? ' OVER' : ''} targets<44=${targets.fails}/${targets.counted} demo=${m.demo.page}+[${m.demo.inSheet}]${t ? ` tab=${t.scope}:${t.uniqueStops}stops hid=${t.fullyHiddenNavBar}/${t.fullyHiddenAnyFixed} esc=${t.escapeCount} firstProduct=${t.tabsToFirstProductAction}` : ''}`);
  return st;
}

async function focusProbe(p, c, key, extraShot = false, as = key, state) {
  const def = UI.focusProbes[key];
  if (!def) { c.focus[as] = { notApplicable: true, reason: 'element does not exist in this UI' }; return; }
  try {
    await p.keyboardFocus(def.spec);
    const info = await p.call('focusInfo');
    c.focus[as] = { state: state ?? def.state, ...info, clipped: !!(info?.clippedBy || info?.clipPathBy) };
    if (extraShot) await p.shot(`${c.tag}-focus-${as}`);
  } catch (e) { c.focus[as] = { state: state ?? def.state, error: e.message.split('\n')[0], clipped: null }; }
}
const probeShots = (c) => c.locale === 'ja' && c.w !== 320;

async function setLocale(p, locale) {
  if (locale !== 'zh') return;
  await p.click(UI.sel.localeSwitch);
  await p.waitFor(`document.documentElement.lang.startsWith('zh')`, 4000, 'html lang zh').catch(() => {});
  await sleep(400);
}

async function waitProducts(p) { await p.waitFor('__h.productRows().length > 0', 12000, 'product rows'); }
const rowAction = (name) => ({ css: UI.rowActionCss, within: '@productRows', withinText: name, enabled: true, not: '[role=radio]', first: true });
const lineCountApi = () => api.cartLineCount();

async function waitCartLines(n, ms = 6000) {
  const r = await (async () => { const t0 = Date.now(); while (Date.now() - t0 < ms) { if ((await lineCountApi()) === n) return true; await sleep(200); } return false; })();
  return r;
}

/** Close the top sheet with Escape and report focus return + background restoration. */
async function escapeCheck(p, openerMark) {
  const before = await p.call('activeInfo');
  await p.key('Escape');
  const closed = await p.waitUntil(async () => !(await p.evaluate('!!__h.topSheet()')), 2500, 100);
  await sleep(TIMING.sheetAnimationMs);
  const hidden = await p.call('ariaHiddenState');
  const active = await p.call('activeInfo');
  const sameOpener = openerMark ? await p.evaluate('document.activeElement === window.__opener') : null;
  const matches = openerMark ? await p.evaluate('__h.matchesOpener()') : null;
  return { closed: !!closed.value, closeMs: closed.elapsed, focusAfterClose: active, focusIsOpenerNode: sameOpener, focusIsOpener: openerMark ? !!(sameOpener || matches) : null, ariaHiddenAfterClose: hidden, focusBeforeEscape: before };
}
async function markOpener(p, spec) { return p.evaluate(`(() => { window.__opener = __h.first(${JSON.stringify(spec)}); window.__openerDesc = __h.openerDesc(window.__opener); return !!window.__opener; })()`); }

/** Open a sheet from `spec` with the keyboard (focus + Enter) and report whether focus moved into it. */
async function openSheetByKeyboard(p, spec, key = 'Enter') {
  await markOpener(p, spec);
  const t0 = Date.now();
  await p.activate(spec, key);
  const opened = await p.waitUntil(() => p.evaluate('!!__h.topSheet()'), 3500, 100);
  await sleep(TIMING.sheetAnimationMs);
  const active = await p.call('activeInfo');
  const hidden = await p.call('ariaHiddenState');
  return { opened: !!opened.value, openMs: Date.now() - t0, focusAfterOpen: active, focusInsideSheet: !!active?.insideSheet, ariaHiddenWhileOpen: hidden };
}

// ---------------------------------------------------------------------------------------------------------------------
// The guest flow for one locale x viewport
// ---------------------------------------------------------------------------------------------------------------------
async function guestFlow(locale, w, h) {
  const c = { tag: `${locale}-${w}`, locale, w, h, states: {}, focus: {}, keyboard: {}, ux: {}, errors: [], log: [] };
  report.combos[c.tag] = c;
  console.log(`\n== guest ${c.tag} (${UI.name}) ==`);
  const prods = await api.products();
  const plain = prods.plain[0], plain2 = prods.plain[1], reqd = prods.required;
  c.products = { plain: plain.name, plain2: plain2.name, required: reqd.name, rows: prods.all.length };
  await api.resetCart();
  const p = await chrome.newPage({ width: w, height: h, outDir: out, config: pageConfig(locale, w, h) });
  const step = async (name, fn) => { try { return await fn(); } catch (e) { c.errors.push({ step: name, error: e.message.split('\n').slice(0, 3).join(' | ') }); console.log(`  !! ${c.tag} step "${name}" failed: ${e.message.split('\n')[0]}`); return null; } };
  try {
    // ---- 1. entry card (no table in the URL => the PIN/"scan the QR" entry card) ----
    await step('entry-card', async () => {
      await p.goto(homeURL, 1200);
      await setLocale(p, locale);
      await capture(p, c, 'entry-card', { full: true });
    });

    // ---- 2. menu first viewport (empty cart: same population as the P-89 baseline) ----
    await step('menu-first-viewport', async () => {
      await p.goto(guestURL, 800); await waitProducts(p); await setLocale(p, locale);
      await p.evaluate('window.scrollTo(0,0)'); await sleep(250);
      const switchInfo = await p.evaluate(`(() => { const b = __h.q1('localeSwitch'); if (!b) return null; const r = b.getBoundingClientRect(); return { w: Math.round(r.width), h: Math.round(r.height), lang: b.getAttribute('lang'), text: b.innerText.trim() }; })()`);
      c.ux.localeSwitch = switchInfo;
      // Tab count first (needs a pristine focus start), then the rest of the measurements.
      const st = await capture(p, c, 'menu-first-viewport', { tab: 'page', full: false });
      c.ux.tabsToFirstProductActionEmptyCart = st.tab.tabsToFirstProductAction;
      await p.evaluate('window.scrollTo(0,0)');
      await p.shot(`${c.tag}-menu-first-viewport-fullpage`, { full: true });
    });

    // ---- 3. plain product: direct add from the row ----
    await step('add-plain', async () => {
      await p.evaluate('window.scrollTo(0,0)');
      const r = await p.click(rowAction(plain.name));
      c.log.push({ add_plain: r });
      c.ux.addPlainReachedCart = await waitCartLines(1);
      await sleep(600);
    });

    // ---- 4. option product ----
    await optionProductFlow(p, c, reqd, step);

    // ---- 5. menu with a non-empty cart: sticky nav / cart bar interplay ----
    await step('menu-with-cart', async () => {
      await p.evaluate('window.scrollTo(0,0)'); await sleep(500);
      await capture(p, c, 'menu-with-cart', { tab: 'page', na: IS_AFTER ? false : undefined, equiv: IS_AFTER ? undefined : 'same page: old cart panel is in-flow above the menu (no bar)' });
      await p.evaluate('window.scrollTo(0,0)');
      await focusProbe(p, c, 'categoryItem', probeShots(c)); await focusProbe(p, c, 'productAction', probeShots(c)); await focusProbe(p, c, 'cartBar', probeShots(c));
      await p.evaluate('document.activeElement && document.activeElement.blur()');
    });
    await step('scrolled-bottom', async () => {
      await p.evaluate('window.scrollTo(0, document.documentElement.scrollHeight)'); await sleep(700);
      const st = await capture(p, c, 'scrolled-bottom', {});
      st.bottom = await p.evaluate(`(() => { const rows = __h.productRows(); const last = rows[rows.length - 1]; const bar = __h.q1('cartBar'); const nav = __h.q1('categoryNav');
        const lr = last ? last.getBoundingClientRect() : null, br = bar ? bar.getBoundingClientRect() : null, nr = nav ? nav.getBoundingClientRect() : null;
        return { lastRowBottom: lr && Math.round(lr.bottom), barTop: br && Math.round(br.top), navBottom: nr && Math.round(nr.bottom), lastRowClearOfBar: lr && br ? lr.bottom <= br.top + 0.5 : null, navInViewport: nr ? nr.top >= -0.5 && nr.bottom > 0 : null, barInViewport: br ? br.bottom <= innerHeight + 0.5 && br.top < innerHeight : null, innerHeight }; })()`);
      c.ux.scrolledBottom = st.bottom;
    });
    if (IS_AFTER) await step('category-nav-behavior', async () => categoryNavBehavior(p, c));

    // ---- 6. cart sheet (2 lines) ----
    await cartSheetFlow(p, c, plain2, step);

    // ---- 7. UX-5 degraded ----
    if (!flags['skip-degraded']) await step('degraded', async () => degradedFlow(p, c));

    // ---- 8-9. confirm + two-track ----
    await confirmAndTrack(p, c, step);

    // ---- 10. ORDER_READ ----
    await step('order-read', async () => orderRead(p, c));

    // ---- 11. extras: empty-cart focus destination, session expiry (after UI only, one phone viewport per locale unless --extras=all) ----
    if (IS_AFTER && !flags['skip-extras'] && (flags.extras === 'all' || (c.locale === 'ja' && w === 375) || (c.locale === 'zh' && w === 320))) await step('extras', async () => extrasFlow(p, c, plain, reqd));
  } finally {
    c.pageErrors = p.errors.slice(0, 20);
    await p.close();
  }
  return c;
}

async function optionProductFlow(p, c, reqd, step) {
  if (!IS_AFTER) {
    // BEFORE: options live inline in the product card; "add" is disabled until the required group is chosen.
    await step('option-inline', async () => {
      const row = { css: 'label.option', within: '@productRows', withinText: reqd.name, has: 'input[type=radio]', first: true };
      await p.evaluate(`(() => { const r = __h.first({ css: '.product-card', withinText: ${JSON.stringify(reqd.name)} }); r && r.scrollIntoView({ block: 'center', behavior: 'instant' }); })()`);
      await sleep(300);
      const addState = await p.evaluate(`(() => { const r = __h.productRows().find((x) => x.innerText.includes(${JSON.stringify(reqd.name)})); const b = r && r.querySelector('button.primary'); return b ? { disabled: b.disabled, hint: (r.querySelector('.field-hint') || {}).innerText || null } : null; })()`);
      c.ux.requiredOldBehavior = addState;
      await capture(p, c, 'option-sheet', { na: 'old UI has no option sheet; options are inline in the product card', equiv: 'inline fieldset in .product-card (scrolled to the option product)', tab: false });
      const mk = p.mark();
      await capture(p, c, 'required-error', { na: 'old UI disables "add" until the required group is chosen (no error, no focus move)', equiv: 'disabled add button + .field-hint text', noShot: false, extra: addState });
      c.ux.requiredPostsOld = p.since(mk).filter((r) => r.method !== 'GET' && /\/cart/.test(r.url)).length;
      for (const k of ['radio', 'quantity']) await focusProbe(p, c, k, probeShots(c));
      await p.click(row);
      await sleep(400);
      await focusProbe(p, c, 'add', probeShots(c));
      await p.click(rowAction(reqd.name));
      c.ux.addRequiredReachedCart = await waitCartLines(2);
      await sleep(600);
    });
    return;
  }

  // AFTER: option sheet.
  await step('option-sheet', async () => {
    await p.evaluate('window.scrollTo(0,0)');
    const opener = rowAction(reqd.name);
    const kb = await openSheetByKeyboard(p, opener);
    c.keyboard.optionSheetOpen = kb;
    const st = await capture(p, c, 'option-sheet', { tab: 'sheet' });
    st.sheetDemo = st.measure.demo;
    for (const k of ['sheetClose', 'radio', 'quantity', 'add']) await focusProbe(p, c, k, probeShots(c));
    c.ux.optionSheetOverflowWhileOpen = st.measure.overflow.over;

    // ---- UX-3: press add with the required group unselected ----
    await p.evaluate(`(() => { const s = __h.topSheet(); s && s.querySelectorAll('input:checked').forEach(() => {}); })()`);
    const mk = p.mark();
    const clickRes = await p.click(UI.sel.sheetAdd);
    await sleep(900);
    const posts = p.since(mk).filter((r) => r.method !== 'GET' && /\/cart/.test(r.url));
    const stillOpen = await p.evaluate('!!__h.topSheet()');
    const active = await p.call('activeInfo');
    const firstInput = await p.evaluate(`(() => { const s = __h.topSheet(); const g = [...(s ? s.querySelectorAll('fieldset') : [])].find((f) => f.getAttribute('aria-invalid') === 'true' || f.querySelector('[aria-invalid="true"]')) || (s && s.querySelector('fieldset')); const i = g && g.querySelector('input:not([disabled])'); return { groupFound: !!g, focusIsFirstOption: !!i && document.activeElement === i, activeIsInGroup: !!g && g.contains(document.activeElement), optionName: i ? __h.name(i) : null }; })()`);
    const errorText = await p.evaluate(`(() => { const s = __h.topSheet(); return s ? new RegExp(${JSON.stringify(UI.rx.selectRequired)}, 'i').test(s.innerText) : false; })()`);
    c.ux.ux3 = { click: clickRes, cartRequests: posts.map((r) => `${r.method} ${new URL(r.url).pathname.replace(/[0-9a-f]{32}|[0-9a-f-]{36}/g, ':id')}`), cartPostCount: posts.length, sheetStillOpen: stillOpen, errorShown: errorText, invalid: await p.call('invalid'), focus: active, ...firstInput };
    await capture(p, c, 'required-error', { tab: false, extra: c.ux.ux3 });
    if (probeShots(c)) await p.shot(`${c.tag}-required-error-focus`);

    // ---- Esc closes, focus returns to the opener, background restored ----
    c.keyboard.optionSheetEscape = await escapeCheck(p, true);

    // ---- reopen with the mouse, choose the first option, add => closes, focus returns, cart gets the line ----
    await p.evaluate('window.scrollTo(0,0)');
    await markOpener(p, opener);
    await p.click(opener); await sleep(TIMING.sheetAnimationMs + 200);
    await p.click(UI.sel.optionLabel);                      // click the visible label (effective target)
    await sleep(250);
    const mk2 = p.mark();
    await p.click(UI.sel.sheetAdd);
    c.ux.addRequiredReachedCart = await waitCartLines(2);
    const closed = await p.waitUntil(async () => !(await p.evaluate('!!__h.topSheet()')), 3000, 100);
    await sleep(TIMING.sheetAnimationMs);
    c.keyboard.optionSheetAddClosesAndReturnsFocus = { closed: !!closed.value, focus: await p.call('activeInfo'), focusIsOpener: await p.evaluate('document.activeElement === window.__opener || __h.matchesOpener()'), requests: p.since(mk2).filter((r) => r.method !== 'GET' && /\/cart/.test(r.url)).map((r) => `${r.method} ${r.status}`), ariaHiddenAfterClose: await p.call('ariaHiddenState') };
  });
}

async function categoryNavBehavior(p, c) {
  await p.evaluate('window.scrollTo(0,0)'); await sleep(300);
  const items = await p.evaluate(`__h.q('categoryNavItem').map((e) => ({ text: __h.name(e), w: Math.round(e.getBoundingClientRect().width), h: Math.round(e.getBoundingClientRect().height), ariaCurrent: e.getAttribute('aria-current') }))`);
  const res = { items, selections: [] };
  for (let i = 1; i < Math.min(items.length, 3); i++) {
    await p.click({ ...UI.sel.categoryNavItem, nth: i });
    await sleep(1200);
    res.selections.push(await p.evaluate(`(() => { const a = document.activeElement; const nav = __h.q1('categoryNav'); const nb = nav ? nav.getBoundingClientRect().bottom : 0; const r = a.getBoundingClientRect();
      return { index: ${i}, active: a.tagName.toLowerCase() + (a.getAttribute('tabindex') !== null ? '[tabindex=' + a.getAttribute('tabindex') + ']' : ''), activeText: (a.innerText || '').trim().slice(0, 30), isHeading: /^H[1-6]$/.test(a.tagName), tabindexMinus1: a.getAttribute('tabindex') === '-1', headingTop: Math.round(r.top), navBottom: Math.round(nb), headingClearOfNav: r.top >= nb - 0.5,
        ariaCurrent: __h.q('categoryNavItem').map((e) => e.getAttribute('aria-current')), currentCount: __h.q('categoryNavItem').filter((e) => e.getAttribute('aria-current') && e.getAttribute('aria-current') !== 'false').length }; })()`));
  }
  c.ux.categoryNav = res;
}

async function cartSheetFlow(p, c, plain2, step) {
  if (!IS_AFTER) {
    await step('cart-panel', async () => {
      await p.evaluate(`(() => { const a = document.querySelector('.cart-panel'); a && a.scrollIntoView({ block: 'start', behavior: 'instant' }); })()`); await sleep(500);
      await capture(p, c, 'cart-sheet', { na: 'old UI has no cart sheet', equiv: 'in-flow .cart-panel with 2 lines', tab: false });
      await p.click({ css: '.stepper button', name: '[+＋]\\s*$', enabled: true, first: true }).catch(() => {}); await sleep(1200);
      await focusProbe(p, c, 'stepperMinus', probeShots(c)); await focusProbe(p, c, 'stepperPlus', probeShots(c));
      await p.click(UI.sel.previewBtn); await sleep(1500);
      await capture(p, c, 'confirm-preview', { na: 'old UI has no preview sheet', equiv: 'old preview mode of .cart-panel', tab: false });
      await focusProbe(p, c, 'confirm', probeShots(c));
    });
    return;
  }
  await step('cart-sheet', async () => {
    await p.evaluate('window.scrollTo(0,0)'); await sleep(300);
    const kb = await openSheetByKeyboard(p, UI.sel.cartBarOpen);
    c.keyboard.cartSheetOpen = kb;
    const st = await capture(p, c, 'cart-sheet', { tab: 'sheet' });
    c.ux.cartSheetLines = { ui: await p.call('cartLines'), api: await lineCountApi() };
    // Bump the quantity first so both stepper buttons are enabled for the focus probes.
    await p.click({ ...UI.sel.stepperPlus, enabled: true, first: true }).catch(() => {}); await sleep(1100);
    await focusProbe(p, c, 'sheetClose', probeShots(c), 'cartSheetClose', 'cart-sheet');
    await focusProbe(p, c, 'stepperMinus', probeShots(c)); await focusProbe(p, c, 'stepperPlus', probeShots(c));
    await p.click({ ...UI.sel.stepperMinus, enabled: true, first: true }).catch(() => {}); await sleep(1100);   // restore quantities
    c.ux.cartSheetLinesAfterStepper = { api: await lineCountApi() };

    // ---- preview enter / back / auto-cancel focus targets (P-89 3.6) ----
    const pv = {};
    await p.activate(UI.sel.previewBtn);
    await p.waitFor(`__h.q1('confirmBtn') || /${UI.rx.previewHeading}/i.test(document.activeElement.innerText || '')`, 6000).catch(() => {});
    await sleep(500);
    pv.enter = { focus: await p.call('activeInfo') }; pv.enter.ok = !!(pv.enter.focus.isHeading && pv.enter.focus.tabindex === '-1' && pv.enter.focus.insideSheet);
    pv.enter.headingMatches = rxOf('previewHeading').test(pv.enter.focus.headingText || '');
    await capture(p, c, 'confirm-preview', { tab: 'sheet', extra: { previewEnter: pv.enter } });
    await focusProbe(p, c, 'confirm', probeShots(c));
    // back to cart
    await p.activate(UI.sel.backBtn); await sleep(700);
    pv.back = { focus: await p.call('activeInfo') };
    pv.back.isPreviewButton = !!(pv.back.focus && rxOf('previewHeading').test('') === false && /金額を確認する|確認金額/.test(pv.back.focus.name || ''));
    pv.back.isCartHeading = !!(pv.back.focus.isHeading && rxOf('cartHeading').test(pv.back.focus.headingText || ''));
    pv.back.ok = pv.back.isPreviewButton || pv.back.isCartHeading;
    pv.back.insideSheet = pv.back.focus.insideSheet;
    // enter preview again, then change the shared cart from "another guest" => preview auto-cancels => focus on the cart heading
    await p.activate(UI.sel.previewBtn); await sleep(1200);
    const touch = await api.touchCart(plain2.key);
    const gone = await p.waitUntil(async () => !(await p.evaluate(`!!__h.q1('confirmBtn')`)), 7000, 200);
    await sleep(500);
    pv.autoCancel = { touch, previewClosed: !!gone.value, ms: gone.elapsed, focus: await p.call('activeInfo') };
    pv.autoCancel.isCartHeading = !!(pv.autoCancel.focus.isHeading && rxOf('cartHeading').test(pv.autoCancel.focus.headingText || ''));
    pv.autoCancel.insideSheet = pv.autoCancel.focus.insideSheet;
    pv.autoCancel.alertShown = await p.evaluate(`!![...document.querySelectorAll('[role=alert]')].find((e) => __h.visible(e) && e.innerText.trim())`);
    if (touch.addedLine) await api.removeLine(touch.addedLine);
    await sleep(2800);                                          // let the 2.5 s poll pick the restored cart
    pv.cartLinesAfterRestore = await lineCountApi();
    c.keyboard.cartPreview = pv;
    c.keyboard.cartSheetEscape = await escapeCheck(p, true);
  });
}

async function degradedFlow(p, c) {
  // Precondition: menu visible, cart non-empty, sheet closed.
  await p.evaluate('window.scrollTo(0,0)'); await sleep(2800);
  const read = async () => p.evaluate(`(() => { const s = __h.topSheet(); const t = document.body.innerText; const st = s ? s.innerText : ''; const bar = __h.cartBarText();
    const pv = __h.q1('previewBtn'); const cf = __h.q1('confirmBtn');
    return { page: new RegExp(${JSON.stringify(UI.rx.degradedNotice)}, 'i').test(t), inSheet: new RegExp(${JSON.stringify(UI.rx.degradedNotice)}, 'i').test(st), bar,
      barDegraded: !!bar && new RegExp(${JSON.stringify(UI.rx.degradedBar)}, 'i').test(bar), barOk: !!bar && new RegExp(${JSON.stringify(UI.rx.syncOk)}, 'i').test(bar),
      time: (t.match(new RegExp(${JSON.stringify(UI.rx.degradedTime)}, 'i')) || [])[1] || null, oldLatest: new RegExp(${JSON.stringify(UI.rx.oldLatest)}, 'i').test(t),
      previewDisabled: pv ? __h.isDisabled(pv) : null, confirmDisabled: cf ? __h.isDisabled(cf) : null, sheetOpen: !!s }; })()`);
  const isDegraded = async () => { const r = await read(); return IS_AFTER ? (r.page || r.barDegraded || r.inSheet) : false; };
  const isHealthy = async () => { const r = await read(); return !r.page && !r.barDegraded && !r.inSheet; };
  const recover = async () => { await p.setOffline(false); const t1 = Date.now(); const rec = await p.waitUntil(isHealthy, TIMING.recoverProbeMs, 150); return { ms: rec.value ? Date.now() - t1 : null, within5s: !!rec.value && Date.now() - t1 <= TIMING.recoverWithinMs }; };
  c.ux.degradedBaseline = await read();
  const linesBefore = await lineCountApi();

  // ---- A. offline with the sheet CLOSED: notice + cart-bar text within 7.5 s ----
  await p.setOffline(true);
  const t0 = Date.now();
  const det = await p.waitUntil(isDegraded, TIMING.degradedProbeMs, 150);
  const ms = Date.now() - t0;
  await sleep(IS_AFTER ? 600 : Math.max(0, 9500 - ms));
  const closedRead = await read();
  c.ux.ux5 = { degradedAfterMs: IS_AFTER ? (det.value ? det.elapsed : null) : null, within7_5s: IS_AFTER ? !!det.value && det.elapsed <= TIMING.degradedWithinMs : null, closed: closedRead, hhmm: closedRead.time };
  if (!IS_AFTER) c.ux.ux5.oldBehaviorAfter9_5sOffline = { stillClaimsLatest: closedRead.oldLatest, previewEnabled: closedRead.previewDisabled === false };
  await capture(p, c, 'degraded-closed', { na: IS_AFTER ? false : 'old UI has no sync-degraded state: it keeps showing "latest / shared with the table" while offline', equiv: IS_AFTER ? undefined : 'same menu page while offline (oldLatest=true is the defect)', tab: false, extra: closedRead });

  if (!IS_AFTER) { await p.setOffline(false); await sleep(3000); c.ux.ux5.linesAfterRecover = await lineCountApi(); return; }

  // ---- B. open the cart sheet while degraded: notice inside the sheet, preview disabled + reason ----
  await p.click(UI.sel.cartBarOpen); await sleep(TIMING.sheetAnimationMs + 200);
  const openRead = await read();
  c.ux.ux5.open = openRead;
  c.ux.ux5.previewDisabledReason = await p.call('disabledReason', UI.sel.previewBtn);
  c.ux.ux5.cartLinesWhileDegraded = { ui: await p.call('cartLines'), api: await lineCountApi() };
  await capture(p, c, 'degraded-open', { tab: false, extra: openRead });
  const rec = await recover();
  c.ux.ux5.recoveredAfterMs = rec.ms; c.ux.ux5.recoveredWithin5s = rec.within5s;
  c.ux.ux5.afterRecover = await read();
  c.ux.ux5.linesAfterRecover = await lineCountApi();
  c.ux.ux5.linesBefore = linesBefore;
  c.ux.ux5.cartLinesUnchanged = c.ux.ux5.linesAfterRecover === linesBefore && c.ux.ux5.cartLinesWhileDegraded.ui === linesBefore;

  // ---- C. confirm button while degraded (enter the preview first, then lose the connection) ----
  await p.click(UI.sel.previewBtn);
  await p.waitFor(`__h.q1('confirmBtn')`, 6000, 'confirm button').catch(() => {});
  await sleep(500);
  await p.setOffline(true);
  const d2 = await p.waitUntil(isDegraded, TIMING.degradedProbeMs, 150);
  await sleep(500);
  const cfRead = await read();
  c.ux.ux5.confirmWhileDegraded = { degradedAfterMs: d2.value ? d2.elapsed : null, confirmDisabled: cfRead.confirmDisabled, reason: await p.call('disabledReason', UI.sel.confirmBtn), previewStillOpen: !!(await p.evaluate(`!!__h.q1('confirmBtn')`)) };
  await capture(p, c, 'degraded-confirm', { tab: false, extra: cfRead });
  const rec2 = await recover();
  c.ux.ux5.confirmRecoveredAfterMs = rec2.ms;
  await p.click(UI.sel.backBtn).catch(() => {}); await sleep(600);
  c.keyboard.degradedSheetEscape = await escapeCheck(p, false);

  // ---- D. requests hang (no offline event, no response): the 7.5 s watchdog must still flag it ----
  await sleep(2800);
  await p.hangRequests('*/cart*');
  const t3 = Date.now();
  const d3 = await p.waitUntil(isDegraded, TIMING.degradedProbeMs + 3000, 150);
  c.ux.ux5.hang = { degradedAfterMs: d3.value ? Date.now() - t3 : null, within7_5s: !!d3.value && (Date.now() - t3) <= TIMING.degradedWithinMs + 500, note: 'measured from the moment the first cart request was left pending; the last accepted response may be up to 2.5 s older, so <= 7.5 s (+0.5 s timer slack) is expected' };
  await p.releaseRequests();
  const t4 = Date.now();
  const r4 = await p.waitUntil(isHealthy, TIMING.recoverProbeMs, 150);
  c.ux.ux5.hang.recoveredAfterMs = r4.value ? Date.now() - t4 : null;
}

// Extras (focus destinations that need a destructive step): emptying the cart from the sheet, and session expiry mid-sheet.
async function extrasFlow(p, c, plain, reqd) {
  const seed = async () => {
    await api.addLine(plain.key, 1, []);
    const g = reqd.groups.find((x) => x.min_select > 0), o = g.options.find((x) => x.availability === 'AVAILABLE') ?? g.options[0];
    await api.addLine(reqd.key, 1, [{ group_key: g.key, option_key: o.key }]);
  };
  // 1. remove every line inside the cart sheet. The sheet may stay open showing the empty state (focus must then stay inside it, not fall to <body>);
  //    once it is closed (automatically, or by Esc here) the opener (cart bar) is gone => focus goes to the menu heading and no aria-hidden/inert is left behind.
  await p.goto(guestURL, 800); await waitProducts(p); await setLocale(p, c.locale);
  await api.resetCart(); await seed(); await p.waitFor(`__h.q1('cartBar')`, 6000, 'cart bar after seeding');
  await p.evaluate('window.scrollTo(0,0)'); await sleep(300);
  await p.click(UI.sel.cartBarOpen); await sleep(TIMING.sheetAnimationMs + 200);
  for (let i = 0; i < 6; i++) { if (!(await p.evaluate(`!!__h.q1('lineRemove')`))) break; await p.click(UI.sel.lineRemove); await sleep(1000); }
  await sleep(800);
  const autoClosed = !(await p.evaluate('!!__h.topSheet()'));
  const afterRemoval = await p.call('activeInfo');
  if (!autoClosed) { await p.key('Escape'); await p.waitUntil(async () => !(await p.evaluate('!!__h.topSheet()')), 4000, 150); }
  const closed = !(await p.evaluate('!!__h.topSheet()'));
  await sleep(TIMING.sheetAnimationMs + 400);
  const f = await p.call('activeInfo');
  c.ux.emptyCartFocus = { autoClosed, focusAfterRemoval: { desc: afterRemoval.desc, insideSheet: !!afterRemoval.insideSheet, isBody: !!afterRemoval.isBody }, sheetClosed: closed, focus: f, isMenuHeading: !!(f.isHeading && f.tabindex === '-1' && rxOf('menuHeading').test(f.headingText || '')), ariaHiddenAfter: await p.call('ariaHiddenState') };

  // 2. session expiry while the sheet is open => entry card heading gets focus, sheet and aria-hidden gone
  await api.resetCart(); await seed(); await p.waitFor(`__h.q1('cartBar')`, 6000, 'cart bar after re-seeding');
  await p.click(UI.sel.cartBarOpen); await sleep(TIMING.sheetAnimationMs + 200);
  await p.failRequests('*/cart*', 410);
  const gone = await p.waitUntil(async () => !(await p.evaluate('!!__h.topSheet()')) && !(await p.evaluate('__h.productRows().length > 0')), 9000, 200);
  await p.releaseRequests();
  await sleep(TIMING.sheetAnimationMs);
  const f2 = await p.call('activeInfo');
  c.ux.sessionExpiry = { entryShownAndSheetClosed: !!gone.value, focus: f2, isHeading: !!(f2.isHeading && f2.tabindex === '-1'), ariaHiddenAfter: await p.call('ariaHiddenState') };
  await capture(p, c, 'session-expired-entry', { tab: false });
  await api.resetCart();
}

async function confirmAndTrack(p, c, step) {
  await step('confirm', async () => {
    await p.evaluate('window.scrollTo(0,0)'); await sleep(500);
    if (IS_AFTER) {
      if (!(await p.evaluate('!!__h.topSheet()'))) { await p.click(UI.sel.cartBarOpen); await sleep(TIMING.sheetAnimationMs + 200); }
      await p.click(UI.sel.previewBtn);
      await p.waitFor(`__h.q1('confirmBtn')`, 8000, 'confirm button');
      await sleep(600);
    } else {
      await p.evaluate(`(() => { const a = document.querySelector('.cart-panel'); a && a.scrollIntoView({ block: 'start', behavior: 'instant' }); })()`);
      if (!(await p.evaluate(`!!__h.q1('confirmBtn')`))) { await p.click(UI.sel.previewBtn); await p.waitFor(`__h.q1('confirmBtn')`, 8000, 'confirm button'); }
      await sleep(600);
    }
    const mk = p.mark();
    await p.click({ ...UI.sel.confirmBtn, enabled: true });
    // The table session accumulates one order per run/combination: always follow the order THIS confirm created (commit response), never "the first card".
    const findCommit = () => p.since(mk).find((r) => r.method === 'POST' && /\/cart\/commit/.test(r.url) && r.status);
    await p.waitUntil(async () => !!findCommit(), 12000, 150);
    const commit = findCommit();
    c.ux.commitStatus = commit ? commit.status : null;
    try { if (commit) { const body = JSON.parse(await p.responseBody(commit.id)); c.orderID = body.order?.order_id ?? null; c.ux.commitOutcome = body.outcome ?? null; } } catch { /* body not available */ }
    await p.waitUntil(async () => { const t = await p.call('orderTracks', c.orderID); return (t.steps && t.steps.length > 0) || (t.legacyStatus && t.legacyStatus.length > 0); }, 12000, 200);
    await sleep(700);
    c.ux.afterConfirmFocus = await p.call('activeInfo');
    c.ux.afterConfirmFocus.isOrderHeading = !!(c.ux.afterConfirmFocus.isHeading && rxOf('orderHeading').test(c.ux.afterConfirmFocus.headingText || ''));
    c.ux.afterConfirmSheetClosed = !(await p.evaluate('!!__h.topSheet()'));
    c.keyboard.afterConfirmAriaHidden = await p.call('ariaHiddenState');
    const tracks = await p.call('orderTracks', c.orderID);
    c.ux.tracksJustConfirmed = tracks;
    await capture(p, c, 'order-just-confirmed', { tab: 'page', na: IS_AFTER ? false : 'old UI has one joined status line, no two tracks', equiv: IS_AFTER ? undefined : '.guest-order-status single line', extra: { tracks, orderID: c.orderID } });
    const cls = classifyTracks(tracks);
    c.ux.ux4 = { justConfirmed: cls };
  });

  await step('accept', async () => {
    const acc = await api.acceptOrders(c.orderID || undefined);
    c.ux.accepted = acc;
    // wait for the 20 s orders poll to show the accepted state
    const t0 = Date.now();
    const got = await p.waitUntil(async () => { const t = await p.call('orderTracks', c.orderID); const cl = classifyTracks(t); return cl.cookingNow && new RegExp(UI.rx.cookAccepted, 'i').test(cl.cookingNow) ? t : (t.legacyStatus || []).some((s) => new RegExp(UI.rx.cookAccepted, 'i').test(s)) ? t : null; }, TIMING.ordersPollMs, 500);
    const tracks = await p.call('orderTracks', c.orderID);
    c.ux.tracksAfterAccept = tracks;
    const after = classifyTracks(tracks);
    const before = c.ux.ux4?.justConfirmed;
    c.ux.ux4 = { justConfirmed: before, afterAccept: after, acceptedVisibleAfterMs: got.value ? Date.now() - t0 : null };
    c.ux.ux4.cookingWaitingAtConfirm = !!before && (before.cookingNow ? new RegExp(UI.rx.cookWaiting, 'i').test(before.cookingNow) : (before.legacy || '').match(new RegExp(UI.rx.cookWaiting, 'i')) !== null);
    c.ux.ux4.paymentUnpaidAtConfirm = !!before && (before.paymentNow ? new RegExp(UI.rx.payUnpaid, 'i').test(before.paymentNow) : (before.legacy || '').match(new RegExp(UI.rx.payUnpaid, 'i')) !== null);
    c.ux.ux4.cookingAdvancedAfterAccept = !!(after.cookingNow ? new RegExp(UI.rx.cookAccepted, 'i').test(after.cookingNow) : (after.legacy || '').match(new RegExp(UI.rx.cookAccepted, 'i')));
    c.ux.ux4.paymentUnchangedAfterAccept = after.paymentNow ? new RegExp(UI.rx.payUnpaid, 'i').test(after.paymentNow) : ((after.legacy || '').match(new RegExp(UI.rx.payUnpaid, 'i')) !== null);
    await capture(p, c, 'order-accepted', { tab: false, na: IS_AFTER ? false : 'old UI has one joined status line, no two tracks', equiv: IS_AFTER ? undefined : '.guest-order-status single line after staff accepted', extra: { tracks } });
  });
}

/** Split the aria-current="step" markers of the newest order card into cooking / payment. */
function classifyTracks(t) {
  const steps = t.steps || [];
  const cooking = steps.find((s) => new RegExp(UI.rx.trackCooking, 'i').test(s.track));
  const payment = steps.find((s) => new RegExp(UI.rx.trackPayment, 'i').test(s.track));
  return {
    stepCount: steps.length,
    cookingNow: (cooking || (steps.length >= 2 ? steps[0] : null))?.current ?? null,
    paymentNow: (payment || (steps.length >= 2 ? steps[1] : null))?.current ?? null,
    classifiedByLabel: !!(cooking && payment),
    legacy: (t.legacyStatus || [])[0] ?? null,
    liveRegionCount: (t.liveRegions || []).filter((r) => r.role === 'status' || r.live).length,
  };
}

async function orderRead(p, c) {
  const link = await p.call('localStorageOrderLink');
  if (!link) { c.states['order-read'] = { error: 'no order-read grant in localStorage (order not created?)' }; return; }
  await p.goto(link, 1000);
  await p.waitFor('document.querySelector("main")', 6000).catch(() => {});
  await setOrderReadLocale(p, c.locale);
  const st = await capture(p, c, 'order-read', { tab: 'page' });
  st.langButton = await p.evaluate(`(() => { const b = __h.q1('orderReadLang'); if (!b) return null; const r = b.getBoundingClientRect(); const cs = getComputedStyle(b); return { text: b.innerText.trim(), lang: b.getAttribute('lang'), w: Math.round(r.width), h: Math.round(r.height), border: cs.borderTopWidth + ' ' + cs.borderTopStyle + ' ' + cs.borderTopColor, bg: cs.backgroundColor, fontSize: cs.fontSize, mainPadding: getComputedStyle(document.querySelector('main')).padding }; })()`);
  c.ux.orderReadLangButton = st.langButton;
  await focusProbe(p, c, 'orderReadLang', probeShots(c));
}
async function setOrderReadLocale(p, locale) {
  if (locale !== 'zh') return;
  await p.click(UI.sel.orderReadLang).catch(() => {});
  await p.waitFor(`document.documentElement.lang.startsWith('zh')`, 3000).catch(() => {});
  await sleep(500);
}

// ---------------------------------------------------------------------------------------------------------------------
// DS-1 regression (staff / KDS / catalog): the 20 rendered states of the DS-1 acceptance run
// ---------------------------------------------------------------------------------------------------------------------
async function ds1Record(p, name, opts = {}) {
  await sleep(300);
  const shot = await p.shot(`ds1-${name}`, { full: opts.full !== false });
  const audit = compactAudit(await p.call('audit'));
  const overflow = await p.call('overflow');
  const bytes = createHash('sha256').update(readFileSync(join(out, shot))).digest('hex').slice(0, 16);
  report.ds1.states[name] = { shot, sha: bytes, audit, overflow: { scrollWidth: overflow.scrollWidth, innerWidth: overflow.deviceWidth, over: overflow.over, offenders: overflow.offenders } };
  console.log(`  [ds1] ${name}: aaFail=${audit.fails.length} minFont=${audit.minFont} overflow=${overflow.scrollWidth}/${overflow.deviceWidth}`);
}
async function staffLogin(p, path, role) {
  await p.goto(base + path, 800);
  await p.waitFor(`document.querySelector('form input')`, 8000);
  const c = creds.credentials[role];
  await p.evaluate(`(() => { const inputs = [...document.querySelectorAll('form input')].slice(0, 3); const values = ${JSON.stringify([creds.tenant_id, creds.store_id, c.token])};
    const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set; inputs.forEach((el, i) => { set.call(el, values[i]); el.dispatchEvent(new Event('input', { bubbles: true })); }); document.querySelector('form').requestSubmit(); return true; })()`);
  await sleep(2500);
}
async function switchLocaleDs1(p, locale) { if (locale === 'zh') { await p.evaluate(`(() => { const b = document.querySelector('.locale-switch'); if (b) b.click(); return true; })()`); await sleep(600); } }
async function ds1Staff(width, height, locale) {
  const p = await chrome.newPage({ width, height, outDir: out, config: pageConfig(locale, width, height) });
  const tag = `staff-${locale}-${width}`;
  try {
    await staffLogin(p, '/staff', 'cashier');
    await switchLocaleDs1(p, locale);
    await p.waitFor(`document.querySelector('.staff-table-card')`, 8000);
    await ds1Record(p, `${tag}-01-tables`);
    await p.evaluate(`(() => { const card = [...document.querySelectorAll('.staff-table-card')].find((c) => /05/.test(c.textContent)) || document.querySelector('.staff-table-card'); card.querySelector('button').click(); return true; })()`);
    await sleep(2000);
    await ds1Record(p, `${tag}-02-detail`);
    const opened = await p.evaluate(`(async () => { const b = [...document.querySelectorAll('button:not(:disabled)')].find((x) => /^現金/.test(x.textContent.trim())); if (!b) return null; b.click(); for (let i = 0; i < 20; i++) { await new Promise((r) => setTimeout(r, 200)); if (document.querySelector('dialog[open]')) return b.textContent.trim(); } return 'clicked-no-dialog:' + b.textContent.trim(); })()`);
    report.ds1.states[`${tag}-dialog-opened-by`] = opened;
    if (opened) { await ds1Record(p, `${tag}-03-dialog`, { full: false }); await p.key('Escape'); await sleep(600); }
    if (locale === 'ja') {
      const accepted = await p.evaluate(`(async () => { const b = [...document.querySelectorAll('button:not(:disabled)')].find((x) => /注文を受け付ける|確認接單/.test(x.textContent)); if (!b) return false; b.click(); await new Promise((r) => setTimeout(r, 1500)); return true; })()`);
      report.ds1.states[`${tag}-accepted-order`] = accepted;
    }
  } finally { await p.close(); }
}
async function ds1Kds(width, height, locale) {
  const p = await chrome.newPage({ width, height, outDir: out, config: pageConfig(locale, width, height) });
  try { await staffLogin(p, '/kitchen', 'kitchen'); await switchLocaleDs1(p, locale); await sleep(1500); await ds1Record(p, `kds-${locale}-${width}`); } finally { await p.close(); }
}
async function ds1Catalog(width, height, locale) {
  const p = await chrome.newPage({ width, height, outDir: out, config: pageConfig(locale, width, height) });
  try { await staffLogin(p, '/catalog', 'catalog_admin'); await switchLocaleDs1(p, locale); await sleep(1500); await ds1Record(p, `catalog-${locale}-${width}`); } finally { await p.close(); }
}
async function ds1Regression() {
  console.log('\n== DS-1 regression: staff / KDS / catalog ==');
  for (const [w, h] of [[1280, 900], [375, 812]]) for (const l of LOCALES) await ds1Staff(w, h, l).catch((e) => { report.ds1.states[`staff-${l}-${w}-error`] = e.message; console.log('  !! staff', l, w, e.message.split('\n')[0]); });
  for (const [w, h] of [[1280, 900], [375, 812]]) for (const l of LOCALES) {
    await ds1Kds(w, h, l).catch((e) => { report.ds1.states[`kds-${l}-${w}-error`] = e.message; console.log('  !! kds', l, w, e.message.split('\n')[0]); });
    await ds1Catalog(w, h, l).catch((e) => { report.ds1.states[`catalog-${l}-${w}-error`] = e.message; console.log('  !! catalog', l, w, e.message.split('\n')[0]); });
  }
  const states = Object.entries(report.ds1.states).filter(([, v]) => v && v.audit);
  report.ds1.summary = {
    states: states.length, expected: 20,
    aaFailures: states.reduce((n, [, v]) => n + v.audit.fails.length, 0),
    overflowStates: states.filter(([, v]) => v.overflow.over).map(([k]) => k),
    minFont: Math.min(...states.map(([, v]) => v.audit.minFont ?? 99)),
    missing: ['staff', 'kds', 'catalog'].length && states.length < 20 ? `only ${states.length} of 20 states recorded` : null,
  };
}

// ---------------------------------------------------------------------------------------------------------------------
async function jsWeight() {
  const p = await chrome.newPage({ width: 375, height: 812, outDir: out, config: pageConfig('ja', 375, 812) });
  try {
    await p.goto(guestURL, 500); await waitProducts(p);
    const names = await p.evaluate(`performance.getEntriesByType('resource').filter((e) => /\\.js(\\?|$)/.test(e.name)).map((e) => e.name)`);
    const files = [];
    for (const url of [...new Set(names)]) { const buf = Buffer.from(await (await fetch(url)).arrayBuffer()); files.push({ name: url.split('/').pop(), decoded: buf.length, gzip9: gzipSync(buf, { level: 9 }).length, gzip6: gzipSync(buf).length }); }
    report.js = { guestChunks: files, decodedTotal: files.reduce((n, f) => n + f.decoded, 0),
      gzipTotalViteKB: Math.round(files.reduce((n, f) => n + f.gzip6, 0) / 10) / 100,   // default zlib level, KB = 1000 B: how Vite prints sizes (baseline 99.7)
      gzipTotalKB: Math.round(files.reduce((n, f) => n + f.gzip9, 0) / 10) / 100,
      note: 'chunks the guest page actually loads (Resource Timing), gzip recomputed locally; compare gzipTotalViteKB with the 99.7 KB baseline / 125 KB budget' };
    console.log(`  js: ${files.length} chunks, decoded ${report.js.decodedTotal} B, gzip ${report.js.gzipTotalViteKB} KB (vite-style)`);
  } finally { await p.close(); }
}

async function prepareTopbarFixture() {
  if (flags.demo !== 'false') throw new Error('topbar mode requires --demo=false');
  if (typeof flags['expect-store-name'] !== 'string') throw new Error('topbar mode requires --expect-store-name=<text|none>');
  if (typeof flags.combos !== 'string') throw new Error('topbar mode requires --combos=ja:375,zh:375,ja:320,zh:320');
  const manager = creds.credentials?.manager;
  if (!manager?.token) throw new Error('topbar mode requires manager credentials in the credentials file');
  const apiBase = (creds.base_url || base).replace(/\/$/, '');
  const scope = `/v1/staff/tenants/${encodeURIComponent(creds.tenant_id)}/stores/${encodeURIComponent(creds.store_id)}`;
  const request = async (path, init) => {
    const response = await fetch(apiBase + path, init);
    const body = await response.text();
    if (!response.ok) throw new Error(`${init.method} ${path} returned ${response.status}: ${body.slice(0, 240)}`);
    return body ? JSON.parse(body) : null;
  };
  const headers = { authorization: `Bearer ${manager.token}`, 'content-type': 'application/json' };
  await request(`${scope}/qr-mode`, { method: 'PUT', headers, body: JSON.stringify({ mode: 'STAFF_OPEN' }) });
  await request(`${scope}/tables/${encodeURIComponent(creds.table_id)}/sessions`, { method: 'POST', headers, body: '{}' });
}

async function topbarFlow() {
  await prepareTopbarFixture();
  const expected = flags['expect-store-name'] === 'none' ? null : flags['expect-store-name'];
  const combos = flags.combos.split(',').map((item) => {
    const [locale, widthText] = item.split(':');
    const width = Number(widthText);
    if (!['ja', 'zh'].includes(locale) || ![320, 375].includes(width)) throw new Error(`invalid topbar combo: ${item}`);
    return { locale, width, height: width === 375 ? 812 : 700, tag: `${locale}-${width}` };
  });
  for (const combo of combos) {
    const c = { ...combo, states: {}, errors: [] };
    report.combos[c.tag] = c;
    const p = await chrome.newPage({ width: c.width, height: c.height, outDir: out, config: pageConfig(c.locale, c.width, c.height) });
    try {
      const url = `${base.replace(/\/$/, '')}/?table=${encodeURIComponent(creds.table_locator)}`;
      await p.goto(url, 900);
      await setLocale(p, c.locale);
      await p.waitFor(`document.querySelector('[data-surface="guest"] .entry-card')`, 8000, 'guest entry card');
      const entry = await captureTopbarState(p, c, 'entry-card', expected, true);
      await p.click({ css: 'button', name: c.locale === 'ja' ? '^参加する$' : '^加入桌次$' });
      await p.waitFor(`document.querySelector('[data-surface="guest"] h1')?.textContent.trim() === ${JSON.stringify(c.locale === 'ja' ? 'お品書き' : '菜單')}`, 10000, 'guest menu');
      await sleep(700);
      await p.evaluate('window.scrollTo(0,0)'); await sleep(250);   // same first-viewport baseline as the DS-2 flow
      await captureTopbarState(p, c, 'menu-first-viewport', expected, false);
      c.passed = entry.passed && c.states['menu-first-viewport'].passed;
      c.pageErrors = p.errors.slice(0, 20);
    } catch (error) {
      c.errors.push(error.message.split('\n')[0]);
      c.passed = false;
      console.log(`  !! ${c.tag} topbar capture failed: ${error.message.split('\n')[0]}`);
    } finally { await p.close(); }
  }
  const failed = Object.values(report.combos).filter((combo) => !combo.passed);
  report.evaluation = { mode: 'topbar', expectedStoreName: expected ?? 'none', combos: combos.length, passed: failed.length === 0, failed: failed.map((combo) => combo.tag) };
  report.topbarSummary = renderTopbarSummary(report);
  if (failed.length) exitCode = 1;
}

async function captureTopbarState(p, c, name, expected, entryCard) {
  const topbar = await p.call('topbarInfo');
  const measure = await p.call('measure');
  const audit = compactAudit(await p.call('audit'));
  const overflow = await p.call('overflow');
  const clipping = await p.evaluate(`(() => { const root = document.querySelector('[data-surface="guest"] header'); if (!root) return ['missing topbar']; const bad = []; for (const el of [root, ...root.querySelectorAll('*')]) { const cs = getComputedStyle(el), tag = el.tagName.toLowerCase(), clippedX = el.scrollWidth > el.clientWidth + 1, clippedY = el.scrollHeight > el.clientHeight + 1, lineClamped = (cs.lineClamp && cs.lineClamp !== 'none') || (cs.webkitLineClamp && cs.webkitLineClamp !== 'none'); if (cs.textOverflow === 'ellipsis' && clippedX) bad.push(tag + ':ellipsis-x'); if ((lineClamped || /(hidden|clip)/.test(cs.overflowY)) && clippedY) bad.push(tag + ':clipped-y'); if (/(hidden|clip)/.test(cs.overflowX) && clippedX) bad.push(tag + ':clipped-x'); } return [...new Set(bad)]; })()`);
  const slot = await p.evaluate(`(() => { const el = document.querySelector('[data-store-slot]'); return el ? { kind: el.getAttribute('data-store-slot'), text: el.innerText.trim() } : null; })()`);
  const slotOK = expected === null ? slot === null : slot?.kind === 'api' && slot.text === expected;
  const firstTextOK = expected === null ? topbar.firstText !== expected : topbar.firstText === expected;
  const passed = !!topbar.found && slotOK && (expected === null ? !slot : firstTextOK)
    && !measure.overflow.over && !overflow.over && audit.fails.length === 0 && clipping.length === 0
    && topbar.platformNamesInTopbar === 0 && topbar.poweredByTojima === 1;
  const shot = await p.shot(`${c.tag}-${name}`, { full: entryCard });
  const state = { name, shot, topbar, slot, overflow: { scrollWidth: overflow.scrollWidth, deviceWidth: overflow.deviceWidth, over: overflow.over }, audit, clipping, passed };
  c.states[name] = state;
  console.log(`  [${c.tag}] ${name}: name=${slot?.text ?? 'none'} aaFail=${audit.fails.length} overflow=${overflow.scrollWidth}/${overflow.deviceWidth}${overflow.over ? ' OVER' : ''} clipped=${clipping.length} passed=${passed}`);
  return state;
}

function renderTopbarSummary(r) {
  const lines = ['# D1 guest store display name topbar capture', '', `Expected store name: ${r.evaluation.expectedStoreName}`, '', '| Combo | Entry card | Menu | Result |', '| --- | --- | --- | --- |'];
  for (const [tag, combo] of Object.entries(r.combos)) lines.push(`| ${tag} | ${combo.states?.['entry-card']?.passed ? 'PASS' : 'FAIL'} | ${combo.states?.['menu-first-viewport']?.passed ? 'PASS' : 'FAIL'} | ${combo.passed ? 'PASS' : 'FAIL'} |`);
  lines.push('', `Overall: ${r.evaluation.passed ? 'PASS' : 'FAIL'}`, '');
  return lines.join('\n');
}

// ---------------------------------------------------------------------------------------------------------------------
const chrome = new Chrome();
let exitCode = 0;
try {
  await chrome.start();
  report.meta.chrome = chrome.version;
  if (label === 'topbar') await topbarFlow();
  else {
    const only = typeof flags.combos === 'string' ? flags.combos.split(',').map((s) => s.split(':')) : null;
    if (!flags['only-ds1']) {
      for (const l of LOCALES) for (const [w, h] of VIEWPORTS) {
        if (only && !only.some(([ol, ow]) => ol === l && Number(ow) === w)) continue;
        try { await guestFlow(l, w, h); } catch (e) { report.combos[`${l}-${w}`] ??= {}; report.combos[`${l}-${w}`].fatal = e.stack || String(e); console.error(e); exitCode = 1; }
      }
      await jsWeight().catch((e) => report.notes.push('jsWeight: ' + e.message));
    }
    if (!flags['skip-ds1']) await ds1Regression();
  }
} catch (e) {
  report.error = String(e.stack || e); console.error(e); exitCode = 1;
} finally {
  if (label !== 'topbar') report.evaluation = evaluateReport(report, UI);
  writeFileSync(join(out, `report-${label}.json`), JSON.stringify(report, null, 2));
  writeFileSync(join(out, `summary-${label}.md`), label === 'topbar' ? report.topbarSummary ?? '# D1 guest store display name topbar capture\n\nFailed before capture.\n' : renderSummary(report, UI));
  await chrome.stop();
  console.log(`\nwrote ${join(out, `report-${label}.json`)} and summary-${label}.md`);
  process.exit(exitCode);
}
