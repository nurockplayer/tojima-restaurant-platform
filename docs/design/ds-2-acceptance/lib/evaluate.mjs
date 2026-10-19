// Acceptance criteria (P-89 section 5.4 / 5.5) evaluated over a finished report, plus the markdown summary.
// For "before" the same measurements are compared with the P-89 section 2 baseline instead of the section 5 thresholds.

const T5 = { recover: 5000 };
const T = { rows375: 3, rows320: 2, medianMax: 120, tabMax: 8, minTarget: 44, minFont: 12, jsGzipMaxKB: 125, sheetEscapes: 0 };

// P-89 section 2 baseline (3c47f7a).
export const BASELINE = {
  firstTop: { 'ja-375': 809, 'zh-375': 783, 'ja-320': 832, 'zh-320': 804 },
  fully: { 'ja-375': 0, 'zh-375': 0, 'ja-320': 0, 'zh-320': 0, 'ja-1280': 2, 'zh-1280': 2 },
  median: { 'ja-375': 207, 'zh-375': 205 },
  max: { 'ja-375': 335, 'zh-375': 329, 'ja-320': 353, 'zh-320': 347 },
  tab: 8, orderReadButton: { w: 76, h: 29 }, aaFailures: 0, jsDecoded: 315412, jsGzipKBvite: 99.7,
  sticky1280: ['aside'], // only the cart aside is sticky at 1280
};

const nonNA = (st) => st && st.measure && !st.notApplicable;
const states = (report, filter = () => true) => {
  const out = [];
  for (const [ctag, c] of Object.entries(report.combos)) for (const [name, st] of Object.entries(c.states || {})) if (nonNA(st) && filter(name, st, c)) out.push({ combo: ctag, c, name, st });
  return out;
};
const allMeasured = (report) => {
  const out = [];
  for (const [ctag, c] of Object.entries(report.combos)) for (const [name, st] of Object.entries(c.states || {})) if (st && st.measure) out.push({ combo: ctag, c, name, st });
  return out;
};
const chk = (id, title, pass, detail, section = '') => ({ id, section, title, pass, detail });
const list = (xs, n = 6) => (xs.length <= n ? xs.join('; ') : xs.slice(0, n).join('; ') + `; ... (+${xs.length - n})`);
const isSheetState = (name) => ['option-sheet', 'required-error', 'cart-sheet', 'confirm-preview', 'degraded-open'].includes(name);

export function evaluateReport(report, UI) {
  const after = UI.name === 'after';
  const ev = { mode: UI.name, checks: [], baseline: null };
  const C = ev.checks;
  const ms = after ? states(report) : allMeasured(report); // before: measure every state (N/A ones carry the old equivalent)

  // ---------- generic (both sides) ----------
  const aa = ms.filter((s) => s.st.audit.fails.length);
  const dsAA = Object.entries(report.ds1?.states || {}).filter(([, v]) => v?.audit?.fails?.length).map(([k, v]) => `${k}:${v.audit.fails.length}`);
  C.push(chk('aa', 'Computed-style AA contrast failures = 0 (guest states)', aa.length === 0, aa.length ? list(aa.map((s) => `${s.combo}/${s.name}:${s.st.audit.fails.length} (${s.st.audit.fails[0].text} ${s.st.audit.fails[0].ratio}<${s.st.audit.fails[0].need})`)) : `${ms.length} states, 0 failures`, '5.4-1'));
  const small = ms.filter((s) => (s.st.audit.minFont ?? 99) < T.minFont);
  C.push(chk('font', `Minimum rendered font size >= ${T.minFont}px`, small.length === 0, small.length ? list(small.map((s) => `${s.combo}/${s.name}:${s.st.audit.minFont}px`)) : `min ${Math.min(...ms.map((s) => s.st.audit.minFont ?? 99))}px`, '5.4-1'));
  const over = ms.filter((s) => s.st.measure.overflow.over);
  C.push(chk('overflow', 'No horizontal overflow (also with a sheet open)', over.length === 0, over.length ? list(over.map((s) => `${s.combo}/${s.name}:${s.st.measure.overflow.scrollWidth}>${s.st.measure.overflow.deviceWidth} ${(s.st.measure.overflow.offenders[0] || {}).el || ''} ${(s.st.measure.overflow.offenders[0] || {}).kind || ''}`)) : `${ms.length} states incl. ${ms.filter((s) => s.st.measure.sheet).length} with a sheet open`, '5.4-1'));

  // ---------- rows / first viewport ----------
  const firstVp = states(report, (n) => n === 'menu-first-viewport');
  const rowsMsg = [];
  let rowsPass = true;
  for (const s of firstVp) {
    const r = s.st.measure.rows; const w = s.c.w;
    const need = w === 375 ? T.rows375 : w === 320 ? T.rows320 : null;
    rowsMsg.push(`${s.combo}: top ${r.firstTop}px fully ${r.fullyInFirstViewport} median ${r.height.median}px`);
    if (need != null && r.fullyInFirstViewport < need) rowsPass = false;
    if (r.height.median > T.medianMax) rowsPass = false;
  }
  C.push(chk('rows', `First viewport: >=${T.rows375} fully visible rows @375, >=${T.rows320} @320; median row height <= ${T.medianMax}px (all rows)`, after ? rowsPass : null, list(rowsMsg, 12), '5.4-2'));

  // ---------- fixed nav / cart bar / tab ----------
  if (after) {
    const bottom = states(report, (n) => n === 'scrolled-bottom');
    const badBottom = bottom.filter((s) => !(s.st.measure.categoryNav?.inViewport && s.st.measure.cartBar?.inViewport));
    C.push(chk('bottom', 'Scrolled to the bottom: category nav and cart bar stay inside the viewport', bottom.length > 0 && badBottom.length === 0, bottom.length ? (badBottom.length ? list(badBottom.map((s) => `${s.combo} nav=${s.st.measure.categoryNav?.inViewport} bar=${s.st.measure.cartBar?.inViewport}`)) : `${bottom.length} combos ok`) : 'no scrolled-bottom state recorded', '5.4-3'));
    const tabStates = states(report, (n, st) => st.tab && st.tab.scope === 'page');
    const hidden = tabStates.reduce((n, s) => n + s.st.tab.fullyHiddenNavBar, 0);
    const hiddenAny = tabStates.reduce((n, s) => n + s.st.tab.fullyHiddenAnyFixed, 0);
    C.push(chk('tab-hidden', 'Tab traversal: focus stops fully hidden behind the category nav / cart bar = 0', tabStates.length > 0 && hidden === 0, `nav/bar: ${hidden}; any fixed/sticky overlay: ${hiddenAny}; ${tabStates.length} page traversals (${tabStates.reduce((n, s) => n + s.st.tab.uniqueStops, 0)} stops)`, '5.4-3'));
    const tabs = states(report, (n) => n === 'menu-first-viewport' || n === 'menu-with-cart').map((s) => ({ s, v: s.st.tab?.tabsToFirstProductAction }));
    const badTab = tabs.filter((x) => x.v == null || x.v > T.tabMax);
    C.push(chk('tab-count', `Tab presses from page start to first product action <= ${T.tabMax} (baseline 8)`, tabs.length > 0 && badTab.length === 0, list(tabs.map((x) => `${x.s.combo}/${x.s.name}:${x.v}`), 12), '5.4-3'));
  }

  // ---------- target sizes ----------
  const tfail = ms.filter((s) => s.st.targets.fails > 0);
  const realTrunc = (s) => (s.st.measure.truncated || []).filter((x) => x.clientW >= 8);   // clientW < 8 = sr-only text
  const trunc = ms.filter((s) => realTrunc(s).length > 0);
  C.push(chk('truncation', 'No visible text is cut off by ellipsis / clipping (cart-bar sync text must show the HH:mm)', after ? trunc.length === 0 : null, trunc.length ? list(trunc.map((s) => `${s.combo}/${s.name}: ` + realTrunc(s).slice(0, 2).map((x) => `"${x.text}" ${x.clientW}<${x.scrollW}`).join(', ')), 8) : `${ms.length} states, none`, '5.4-7'));
  C.push(chk('targets', 'Every operable element >= 44x44px (label-wrapped radio/checkbox: label rect; sr-only, disabled and inert background listed, not counted)', after ? tfail.length === 0 : null,
    tfail.length ? (() => { const d = new Map(); for (const s of tfail) for (const f of s.st.targets.failList) { const k = `${f.tag}[${f.name}] ${f.effective.w}x${f.effective.h}`; if (!d.has(k)) d.set(k, new Set()); d.get(k).add(s.name); } return `distinct failing elements: ` + [...d].slice(0, 12).map(([k, v]) => `${k} in ${[...v].join('/')}`).join('; ') + ` || per state: ` + list(tfail.map((s) => `${s.combo}/${s.name}:${s.st.targets.fails}/${s.st.targets.counted}`), 8); })() : `${ms.length} states, 0 undersized`, '5.4-4'));

  // ---------- DEMO / platform wording / fonts ----------
  const menuStates = ms.filter((s) => ['menu-first-viewport', 'menu-with-cart', 'scrolled-bottom'].includes(s.name));
  const demoPage = menuStates.filter((s) => s.st.measure.demo.page !== 1);
  const sheetStates = ms.filter((s) => isSheetState(s.name) && s.st.measure.sheet);
  const sheetDemoBad = sheetStates.filter((s) => !(s.st.measure.demo.inSheet.length >= 1 && s.st.measure.demo.inSheet.every((n) => n === 1)));
  const orState = ms.filter((s) => s.name === 'order-read');
  const orDemoBad = orState.filter((s) => s.st.measure.demo.page !== 1);
  C.push(chk('demo', 'DEMO: 1 page banner, 1 DemoNotice in every open ordering sheet, 1 on ORDER_READ', after ? demoPage.length === 0 && sheetDemoBad.length === 0 && orDemoBad.length === 0 && sheetStates.length > 0 : null,
    `page banner !=1 in: ${demoPage.map((s) => s.combo + '/' + s.name).join(',') || 'none'}; sheets: ${sheetStates.length} checked, bad: ${sheetDemoBad.map((s) => s.combo + '/' + s.name + '=' + JSON.stringify(s.st.measure.demo.inSheet)).join(',') || 'none'}; order-read bad: ${orDemoBad.map((s) => s.combo).join(',') || 'none'}`, '5.4-5'));
  const tb = menuStates.filter((s) => s.name === 'menu-first-viewport');
  const platBad = tb.filter((s) => s.st.measure.topbar.platformNamesInTopbar !== 0);
  C.push(chk('topbar-platform', 'Topbar contains 0 platform names (東島 / TABLE ORDER / TOJIMA KITCHEN / OPEN TABLE)', after ? platBad.length === 0 : null, `topbar counts: ${tb.map((s) => s.combo + '=' + s.st.measure.topbar.platformNamesInTopbar).join(', ')}; whole page: ${tb.map((s) => s.combo + '=' + s.st.measure.topbar.platformNamesInPage + '[' + s.st.measure.topbar.platformList.join('/') + ']').join(', ')}`, '5.4-5'));
  const pw = tb.filter((s) => s.st.measure.topbar.poweredByTojima !== 1);
  C.push(chk('footer', 'Footer shows "Powered by Tojima" exactly once', after ? pw.length === 0 : null, tb.map((s) => `${s.combo}=${s.st.measure.topbar.poweredByTojima}`).join(', '), '5.4-5'));
  const sn = tb.filter((s) => !s.st.measure.topbar.storeNameFirst);
  C.push(chk('store-name', 'Store name is the first topbar text', after ? sn.length === 0 : null, tb.map((s) => `${s.combo}:"${(s.st.measure.topbar.firstText || '').slice(0, 24)}"`).join(', '), '5.4-5'));
  const fontBad = [];
  for (const s of tb) { const f = s.st.measure.fonts; if (!f.rootStartsSystemUi) fontBad.push(`${s.combo} root: ${f.root.slice(0, 40)}`); if (s.c.locale === 'zh' && !f.rootHasPingFangTC) fontBad.push(`${s.combo} root lacks PingFang TC`); }
  for (const s of sheetStates) { const f = s.st.measure.fonts; if (f.sheet && !f.sheetStartsSystemUi) fontBad.push(`${s.combo}/${s.name} sheet: ${f.sheet.slice(0, 40)}`); if (s.c.locale === 'zh' && f.sheet && !f.sheetHasPingFangTC) fontBad.push(`${s.combo}/${s.name} sheet lacks PingFang TC`); }
  C.push(chk('fonts', 'Guest root and sheet font-family starts with system-ui; zh contains PingFang TC', after ? fontBad.length === 0 : null, fontBad.length ? list(fontBad) : tb.map((s) => `${s.combo}: ${s.st.measure.fonts.root.slice(0, 36)}`).join(' | '), '5.4-5'));
  const surf = tb.filter((s) => s.st.measure.topbar.dataSurface !== 'guest');
  C.push(chk('data-surface', 'html carries data-surface="guest" while the guest app is mounted', after ? surf.length === 0 : null, tb.map((s) => `${s.combo}=${s.st.measure.topbar.dataSurface}`).join(', '), '3.11'));

  // ---------- UX-3 / UX-5 / UX-4 ----------
  const combos = Object.entries(report.combos);
  if (after) {
    const u3 = combos.map(([t, c]) => [t, c.ux?.ux3]);
    const u3bad = u3.filter(([, u]) => !u || u.cartPostCount !== 0 || !u.errorShown || !u.invalid?.length || !u.focusIsFirstOption || !u.sheetStillOpen);
    C.push(chk('ux3', 'UX-3: add with required group unselected => 0 cart POST/PUT/DELETE, error shown, aria-invalid, focus on the first option of that group, sheet stays open', combos.length > 0 && u3bad.length === 0, u3.map(([t, u]) => `${t}: posts=${u?.cartPostCount} err=${u?.errorShown} invalid=${u?.invalid?.length} focusFirst=${u?.focusIsFirstOption} describedby=${!!u?.invalid?.[0]?.describedby}`).join(' | '), '5.4-6'));
    const u5 = combos.map(([t, c]) => [t, c.ux?.ux5]);
    const u5bad = u5.filter(([, u]) => !u || !u.within7_5s || !u.hhmm || !u.open?.previewDisabled || !u.open?.inSheet || !u.cartLinesUnchanged || !u.recoveredWithin5s || !u.closed?.barDegraded);
    C.push(chk('ux5', 'UX-5: offline => degraded notice with HH:mm within 7.5 s (page, sheet, cart bar), preview disabled, cart lines unchanged, cleared <= 5 s after online', combos.length > 0 && u5bad.length === 0, u5.map(([t, u]) => `${t}: ${u?.degradedAfterMs}ms hhmm=${u?.hhmm} bar="${(u?.closed?.bar || '').slice(0, 30)}" sheetNotice=${u?.open?.inSheet} previewDisabled=${u?.open?.previewDisabled} (reason: ${(u?.previewDisabledReason?.describedText || '-').slice(0, 24)}) lines ${u?.linesBefore}->${u?.linesAfterRecover} recovered ${u?.recoveredAfterMs}ms`).join(' | '), '5.4-6'));
    const u5c = u5.filter(([, u]) => !u || u.confirmWhileDegraded?.confirmDisabled !== true || !(u.confirmWhileDegraded?.reason?.describedby || []).length);
    C.push(chk('ux5-confirm', 'UX-5: "confirm order" is disabled while degraded and the reason is linked with aria-describedby', combos.length > 0 && u5c.length === 0, u5.map(([t, u]) => `${t}: disabled=${u?.confirmWhileDegraded?.confirmDisabled} describedby=${(u?.confirmWhileDegraded?.reason?.describedby || []).join(',') || '-'} reason="${(u?.confirmWhileDegraded?.reason?.describedText || '').slice(0, 30)}"`).join(' | '), '5.4-6'));
    const u5h = u5.filter(([, u]) => !u?.hang?.within7_5s || u.hang.recoveredAfterMs == null || u.hang.recoveredAfterMs > T5.recover);
    C.push(chk('ux5-watchdog', 'UX-5 watchdog: pending cart requests (no offline event, no response) still flip to degraded within 7.5 s; cleared after they complete', combos.length > 0 && u5h.length === 0, u5.map(([t, u]) => `${t}: degraded ${u?.hang?.degradedAfterMs}ms, cleared ${u?.hang?.recoveredAfterMs}ms`).join(' | '), '5.4-6'));
    const u4 = combos.map(([t, c]) => [t, c.ux?.ux4]);
    const u4bad = u4.filter(([, u]) => !u || !u.cookingWaitingAtConfirm || !u.paymentUnpaidAtConfirm || !u.cookingAdvancedAfterAccept || !u.paymentUnchangedAfterAccept || u.justConfirmed?.stepCount < 2);
    C.push(chk('ux4', 'UX-4: right after confirm cooking = waiting for staff and payment = unpaid; after staff accepts only the cooking track advances', combos.length > 0 && u4bad.length === 0, u4.map(([t, u]) => `${t}: ${u?.justConfirmed?.cookingNow} / ${u?.justConfirmed?.paymentNow} -> ${u?.afterAccept?.cookingNow} / ${u?.afterAccept?.paymentNow} (${u?.acceptedVisibleAfterMs}ms, live regions ${u?.justConfirmed?.liveRegionCount})`).join(' | '), '5.4-6'));
  }

  // ---------- focus ring clipping ----------
  const probeKeys = ['categoryItem', 'productAction', 'sheetClose', 'cartSheetClose', 'radio', 'quantity', 'add', 'cartBar', 'stepperMinus', 'stepperPlus', 'confirm', 'orderReadLang'];
  const clipped = [], unverified = [];
  for (const [t, c] of combos) for (const k of probeKeys) { const f = c.focus?.[k]; if (!f || f.notApplicable) continue; if (f.error) unverified.push(`${t}/${k}: ${f.error}`); else { if (f.clipped) clipped.push(`${t}/${k} by ${f.clippedBy || f.clipPathBy}`); if (!f.hasVisibleRing) clipped.push(`${t}/${k} no visible ring`); if (!f.focusVisible) unverified.push(`${t}/${k} not :focus-visible`); } }
  C.push(chk('focus-ring', 'Focus ring visible and not clipped (clippedBy empty): category item, product action, sheet close, radio, quantity, add, cart bar, stepper +/-, confirm, ORDER_READ language button', after ? clipped.length === 0 && unverified.length === 0 : null, `clipped/no ring: ${clipped.length ? list(clipped) : 'none'}; unverified: ${unverified.length ? list(unverified) : 'none'}`, '5.4-7'));

  // ---------- keyboard (5.5) ----------
  if (after) {
    const kb = [];
    for (const [t, c] of combos) {
      const k = c.keyboard || {};
      const o = k.optionSheetOpen, oe = k.optionSheetEscape, cs = k.cartSheetOpen, ce = k.cartSheetEscape, pv = k.cartPreview;
      const sheetTabs = ['option-sheet', 'cart-sheet', 'confirm-preview'].map((n) => [n, c.states?.[n]?.tab]);
      const bad = (cond, msg) => { if (!cond) kb.push(`${t}: ${msg}`); };
      bad(o?.opened && o.focusInsideSheet, 'option sheet: focus not inside after Enter');
      bad(cs?.opened && cs.focusInsideSheet, 'cart sheet: focus not inside after Enter');
      for (const [n, tab] of sheetTabs) bad(tab && tab.escapeCount === 0 && tab.presses >= 2 * 20, `${n}: ${tab ? tab.escapeCount + ' escapes of ' + tab.presses : 'no traversal'}`);
      bad(o?.ariaHiddenWhileOpen?.backgroundBranchesNotHidden?.length === 0, `option sheet: background not aria-hidden/inert (${o?.ariaHiddenWhileOpen?.backgroundBranchesNotHidden})`);
      bad(oe?.closed && oe.focusIsOpener, `option sheet Esc: closed=${oe?.closed} focusIsOpener=${oe?.focusIsOpener}`);
      bad(oe?.ariaHiddenAfterClose?.anyAriaHiddenLeft === 0, `option sheet: aria-hidden/inert left after close (${oe?.ariaHiddenAfterClose?.anyAriaHiddenLeft})`);
      bad(ce?.closed && ce.focusIsOpener, `cart sheet Esc: closed=${ce?.closed} focusIsOpener=${ce?.focusIsOpener}`);
      bad(ce?.ariaHiddenAfterClose?.anyAriaHiddenLeft === 0, `cart sheet: aria-hidden/inert left after close (${ce?.ariaHiddenAfterClose?.anyAriaHiddenLeft})`);
      bad(pv?.enter?.ok, 'preview enter: focus not on a tabindex=-1 heading inside the sheet');
      bad(pv?.back?.ok && pv.back.insideSheet, 'preview back: focus not on the preview button / cart heading');
      bad(pv?.autoCancel?.previewClosed && pv.autoCancel.isCartHeading && pv.autoCancel.insideSheet, 'preview auto-cancel: focus not on cart heading');
      bad(k.optionSheetAddClosesAndReturnsFocus?.closed && k.optionSheetAddClosesAndReturnsFocus?.focusIsOpener, 'option add: sheet should close and focus return to opener');
      bad(c.ux?.afterConfirmSheetClosed && c.ux?.afterConfirmFocus?.isOrderHeading, `after confirm: sheet closed=${c.ux?.afterConfirmSheetClosed} order heading focused=${c.ux?.afterConfirmFocus?.isOrderHeading} (${c.ux?.afterConfirmFocus?.desc})`);
      bad(k.afterConfirmAriaHidden?.anyAriaHiddenLeft === 0, 'after confirm: aria-hidden/inert left on the page');
    }
    C.push(chk('keyboard', 'Keyboard (5.5): Enter opens sheet with focus inside; 20x Tab / 20x Shift+Tab stay inside; Esc closes and returns focus; background aria-hidden/inert while open and cleared after; preview focus targets', combos.length > 0 && kb.length === 0, kb.length ? list(kb, 10) : `${combos.length} combos ok`, '5.5'));
    const ex = combos.filter(([, c]) => c.ux?.emptyCartFocus || c.ux?.sessionExpiry);
    const exBad = [];
    for (const [t, c] of ex) {
      const a = c.ux.emptyCartFocus, b = c.ux.sessionExpiry;
      if (!(a?.sheetClosed && a.isMenuHeading)) exBad.push(`${t}: empty cart -> focus ${a?.focus?.desc} (sheetClosed=${a?.sheetClosed})`);
      if (a && !a.autoClosed && !a.focusAfterRemoval?.insideSheet) exBad.push(`${t}: emptying the cart inside the sheet moved focus out of it (${a.focusAfterRemoval?.desc})`);
      if (a && a.ariaHiddenAfter?.anyAriaHiddenLeft !== 0) exBad.push(`${t}: aria-hidden left after emptying`);
      if (!(b?.entryShownAndSheetClosed && b.isHeading)) exBad.push(`${t}: session expiry -> focus ${b?.focus?.desc} (entry shown=${b?.entryShownAndSheetClosed})`);
      if (b && b.ariaHiddenAfter?.anyAriaHiddenLeft !== 0) exBad.push(`${t}: aria-hidden left after session expiry`);
    }
    C.push(chk('focus-destinations', 'Focus destinations when the opener disappears (3.2): empty cart -> menu heading; session expiry -> entry-card heading; no aria-hidden left behind', ex.length > 0 ? exBad.length === 0 : null, ex.length ? (exBad.length ? list(exBad) : `${ex.map(([t]) => t).join(', ')} ok`) : 'extras not run', '3.2'));
    const nav = combos.map(([t, c]) => [t, c.ux?.categoryNav]);
    const navBad = nav.filter(([, n]) => !n || !n.selections.length || n.selections.some((x) => !x.isHeading || !x.tabindexMinus1 || x.currentCount !== 1 || !x.headingClearOfNav) || n.items.some((i) => i.h < 44));
    C.push(chk('category-nav', 'Category nav (3.3): items >= 44px, selecting scrolls to a tabindex=-1 heading that is focused, not hidden behind the nav, exactly one aria-current', nav.length > 0 && navBad.length === 0, nav.map(([t, n]) => `${t}: ${n ? n.items.length + ' items, ' + n.selections.map((x) => `${x.active}${x.headingClearOfNav ? '' : ' HIDDEN'} cur=${x.currentCount}`).join('; ') : 'n/a'}`).join(' | '), '3.3'));
  }

  // ---------- ORDER_READ ----------
  const orb = combos.map(([t, c]) => [t, c.ux?.orderReadLangButton]);
  const orBad = orb.filter(([, b]) => !b || b.w < 44 || b.h < 44);
  C.push(chk('order-read-button', 'ORDER_READ language button >= 44x44 (baseline 76x29)', after ? orBad.length === 0 : null, orb.map(([t, b]) => `${t}: ${b ? b.w + 'x' + b.h + ' lang=' + b.lang : 'n/a'}`).join(', '), '5.4-4'));

  // ---------- capacity / DS-1 regression ----------
  const kb = report.js?.gzipTotalKB;
  C.push(chk('js', `Guest JS gzip total <= ${T.jsGzipMaxKB} KB (baseline 99.7 KB, Vite accounting)`, after ? (report.js && report.js.guestChunks.length ? report.js.gzipTotalViteKB <= T.jsGzipMaxKB : null) : null, report.js ? `${report.js.gzipTotalViteKB} KB (level-6, KB=1000; harness level-9 estimate ${kb} KB), decoded ${report.js.decodedTotal} B in ${report.js.guestChunks.length} chunks` : 'not measured', '5.3'));
  const ds = report.ds1?.summary;
  C.push(chk('ds1', 'DS-1 regression: staff / KDS / catalog = 20 states, AA failures 0, horizontal overflow 0', ds ? ds.states >= 20 && ds.aaFailures === 0 && ds.overflowStates.length === 0 : null, ds ? `${ds.states}/20 states, AA failures ${ds.aaFailures}${dsAA.length ? ' (' + dsAA.join(',') + ')' : ''}, overflow: ${ds.overflowStates.join(',') || 'none'}, min font ${ds.minFont}px` : 'skipped', '5.4-8'));

  // ---------- selector health (after only) ----------
  if (after) {
    const zero = {};
    for (const s of ms) for (const [k, v] of Object.entries(s.st.measure.health || {})) { zero[k] ??= { max: 0 }; if (typeof v === 'number') zero[k].max = Math.max(zero[k].max, v); }
    const never = Object.entries(zero).filter(([, v]) => v.max === 0).map(([k]) => k);
    ev.selectorsNeverMatched = never;
    C.push(chk('selector-health', 'Selector health: config keys that never matched anything in any state (adjust UI_AFTER.sel)', never.length === 0 ? true : 'warn', never.join(', ') || 'all keys matched at least once', 'info'));
  }
  const errs = combos.flatMap(([t, c]) => (c.errors || []).map((e) => `${t}/${e.step}: ${e.error}`));
  C.push(chk('flow-errors', 'Harness steps that threw (a failed step means states after it may be missing)', errs.length === 0, errs.length ? list(errs, 10) : 'none', 'info'));

  // ---------- baseline comparison (before) ----------
  if (!after) {
    const rows = [];
    const g = (t, n) => report.combos[t]?.states?.[n]?.measure;
    const ok = (a, b) => (a === b ? 'OK' : 'DIFF');
    for (const t of ['ja-375', 'zh-375', 'ja-320', 'zh-320']) {
      const m = g(t, 'menu-first-viewport'); if (!m) continue;
      rows.push({ item: `${t} first product top`, expected: BASELINE.firstTop[t], got: m.rows.firstTop, res: ok(BASELINE.firstTop[t], m.rows.firstTop) });
      rows.push({ item: `${t} fully visible rows`, expected: BASELINE.fully[t], got: m.rows.fullyInFirstViewport, res: ok(BASELINE.fully[t], m.rows.fullyInFirstViewport) });
      if (BASELINE.max[t]) rows.push({ item: `${t} max card height`, expected: BASELINE.max[t], got: m.rows.height.max, res: ok(BASELINE.max[t], m.rows.height.max) });
      if (BASELINE.median[t]) rows.push({ item: `${t} median card height`, expected: BASELINE.median[t], got: m.rows.height.median, res: ok(BASELINE.median[t], m.rows.height.median) });
      const tabs = report.combos[t]?.ux?.tabsToFirstProductActionEmptyCart;
      rows.push({ item: `${t} Tab to first add`, expected: BASELINE.tab, got: tabs, res: ok(BASELINE.tab, tabs) });
      rows.push({ item: `${t} sticky/fixed elements`, expected: 0, got: m.sticky.length, res: ok(0, m.sticky.length) });
    }
    for (const t of ['ja-1280', 'zh-1280']) { const m = g(t, 'menu-first-viewport'); if (m) { rows.push({ item: `${t} fully visible rows`, expected: BASELINE.fully[t], got: m.rows.fullyInFirstViewport, res: ok(BASELINE.fully[t], m.rows.fullyInFirstViewport) }); rows.push({ item: `${t} sticky elements`, expected: 'cart aside only', got: m.sticky.map((s) => s.el).join(','), res: m.sticky.length === 1 && /cart-panel|aside/.test(m.sticky[0].el) ? 'OK' : 'DIFF' }); } }
    const orb0 = report.combos['ja-375']?.ux?.orderReadLangButton;
    if (orb0) rows.push({ item: 'ORDER_READ button', expected: '76x29', got: `${orb0.w}x${orb0.h}`, res: ok('76x29', `${orb0.w}x${orb0.h}`) });
    rows.push({ item: 'AA failures (guest states)', expected: 0, got: aa.reduce((n, s) => n + s.st.audit.fails.length, 0), res: ok(0, aa.reduce((n, s) => n + s.st.audit.fails.length, 0)) });
    if (report.js) rows.push({ item: 'guest JS decoded bytes', expected: BASELINE.jsDecoded, got: report.js.decodedTotal, res: ok(BASELINE.jsDecoded, report.js.decodedTotal) });
    if (report.js) rows.push({ item: 'guest JS gzip KB (Vite accounting)', expected: BASELINE.jsGzipKBvite, got: report.js.gzipTotalViteKB, res: Math.abs(report.js.gzipTotalViteKB - BASELINE.jsGzipKBvite) < 0.3 ? 'OK' : 'DIFF' });
    ev.baseline = rows;
  }
  return ev;
}

// ---------------------------------------------------------------------------------------------------------------------
const tick = (p) => (p === true ? 'PASS' : p === false ? 'FAIL' : p === 'warn' ? 'CHECK' : 'n/a');

export function renderSummary(report, UI) {
  const ev = report.evaluation || evaluateReport(report, UI);
  const L = [];
  L.push(`# DS-2 rendered acceptance: ${report.meta.label}`);
  L.push('');
  L.push(`- UI config: \`${UI.name}\`; base \`${report.meta.base}\`; run ${report.meta.at}; ${report.meta.chrome || ''}; node ${report.meta.node}`);
  L.push(`- Viewports ja/zh x 375x812, 1280x900, 320x700 (DPR 2). Screenshots: \`shots/\`. Raw evidence: \`report-${report.meta.label}.json\``);
  L.push('');
  L.push('## Per-viewport key numbers');
  L.push('');
  L.push('| combo | first product top | fully visible rows | median / max row | Tab to first action | AA fails | overflow | undersized targets | sticky/fixed (menu) |');
  L.push('|---|---|---|---|---|---|---|---|---|');
  for (const [t, c] of Object.entries(report.combos)) {
    const m = c.states?.['menu-first-viewport']?.measure;
    const all = Object.values(c.states || {}).filter((s) => s && s.audit);
    const aaN = all.reduce((n, s) => n + s.audit.fails.length, 0);
    const ov = all.filter((s) => s.measure.overflow.over).length;
    const tg = all.filter((s) => !s.notApplicable).reduce((n, s) => n + s.targets.fails, 0);
    L.push(`| ${t} | ${m ? m.rows.firstTop + 'px' : '-'} | ${m ? m.rows.fullyInFirstViewport + '/' + m.rows.total : '-'} | ${m ? m.rows.height.median + ' / ' + m.rows.height.max + 'px' : '-'} | ${c.ux?.tabsToFirstProductActionEmptyCart ?? '-'} | ${aaN} | ${ov} | ${tg} | ${m ? m.sticky.map((s) => s.el.split('.').slice(0, 2).join('.') + ' ' + s.position).join(', ') || '0' : '-'} |`);
  }
  L.push('');
  if (ev.baseline) {
    L.push('## Sanity check against P-89 section 2 baseline (3c47f7a)');
    L.push('');
    L.push('| item | P-89 section 2 | measured | |');
    L.push('|---|---|---|---|');
    for (const r of ev.baseline) L.push(`| ${r.item} | ${r.expected} | ${r.got} | ${r.res} |`);
    L.push('');
  }
  L.push('## Acceptance checks');
  L.push('');
  L.push('| check | result | evidence |');
  L.push('|---|---|---|');
  for (const c of ev.checks) L.push(`| ${c.title} (${c.section}) | ${tick(c.pass)} | ${String(c.detail).replace(/\|/g, '/').slice(0, 700)} |`);
  L.push('');
  const na = [];
  for (const [t, c] of Object.entries(report.combos)) for (const [n, s] of Object.entries(c.states || {})) if (s?.notApplicable && t.endsWith('-375') && t.startsWith('ja')) na.push(`- \`${n}\`: ${s.notApplicableReason}${s.equivalent ? ` (closest old equivalent: ${s.equivalent})` : ''}`);
  if (na.length) { L.push('## States that do not exist in this UI (recorded as notApplicable, with the closest old equivalent measured)'); L.push(''); L.push(...na); L.push(''); }
  const ds = report.ds1?.summary;
  if (ds) { L.push('## DS-1 regression (staff / KDS / catalog)'); L.push(''); L.push(`${ds.states}/20 states, AA failures ${ds.aaFailures}, horizontal overflow states: ${ds.overflowStates.join(', ') || 'none'}, min font ${ds.minFont}px. Compare screenshots with \`node compare.mjs <before-dir> <after-dir>\`.`); L.push(''); }
  L.push('## Undersized targets (first viewport state, ja-375)');
  const t375 = report.combos['ja-375']?.states?.['menu-first-viewport']?.targets;
  if (t375) { L.push(''); L.push(`${t375.fails}/${t375.counted} counted targets below 44x44. Choice controls: ` + t375.choiceControls.map((x) => `${x.role} input ${x.input.w}x${x.input.h} / effective(label) ${x.effective.w}x${x.effective.h}`).join('; ')); }
  return L.join('\n') + '\n';
}
