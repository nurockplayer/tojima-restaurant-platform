// In-page probes for the DS-2 acceptance harness. Injected into every document with
// Page.addScriptToEvaluateOnNewDocument (after `window.__cfg = { ui, ... }`).
// Plain script, no imports. Everything hangs off window.__h so Node can call __h.xxx(...) via Runtime.evaluate.
(() => {
  if (window.__h) return;
  const H = (window.__h = {});
  const cfg = () => window.__cfg || {};
  const ui = () => cfg().ui || { sel: {}, rx: {} };

  // ---------- generic helpers ----------
  const mkRx = (src, flags = 'i') => (src instanceof RegExp ? src : new RegExp(src, flags));
  H.rx = (key) => mkRx(ui().rx[key]);
  H.cs = (el) => getComputedStyle(el);
  H.rendered = (el) => !!el && el.isConnected && el.getClientRects().length > 0;
  H.visible = (el) => {
    if (!H.rendered(el)) return false;
    const cs = getComputedStyle(el);
    if (cs.visibility === 'hidden' || cs.display === 'none') return false;
    const r = el.getBoundingClientRect();
    return r.width > 0 && r.height > 0;
  };
  H.role = (el) => {
    const r = el.getAttribute('role');
    if (r) return r.split(/\s+/)[0];
    const t = el.tagName.toLowerCase();
    if (t === 'button') return 'button';
    if (t === 'a' && el.hasAttribute('href')) return 'link';
    if (t === 'input') {
      const ty = (el.getAttribute('type') || 'text').toLowerCase();
      if (ty === 'radio') return 'radio';
      if (ty === 'checkbox') return 'checkbox';
      if (ty === 'button' || ty === 'submit' || ty === 'reset') return 'button';
      if (ty === 'range') return 'slider';
      if (ty === 'number') return 'spinbutton';
      return 'textbox';
    }
    if (t === 'select') return 'combobox';
    if (t === 'textarea') return 'textbox';
    if (/^h[1-6]$/.test(t)) return 'heading';
    const m = { dialog: 'dialog', nav: 'navigation', li: 'listitem', ul: 'list', ol: 'list', fieldset: 'group', aside: 'complementary', header: 'banner', footer: 'contentinfo', main: 'main', article: 'article', img: 'img', label: '' };
    return m[t] || '';
  };
  H.name = (el) => {
    const lb = el.getAttribute('aria-labelledby');
    if (lb) {
      const s = lb.split(/\s+/).map((id) => document.getElementById(id)?.textContent?.trim() || '').filter(Boolean).join(' ');
      if (s) return s;
    }
    const al = el.getAttribute('aria-label');
    if (al) return al.trim();
    if (el.labels && el.labels.length) return [...el.labels].map((l) => l.textContent.trim()).join(' ');
    if (el.tagName === 'IMG') return el.alt || '';
    if (el.tagName === 'INPUT' && /^(button|submit|reset)$/i.test(el.type)) return el.value;
    if (el.tagName === 'FIELDSET') { const lg = el.querySelector(':scope > legend'); if (lg) return lg.textContent.replace(/\s+/g, ' ').trim(); }
    const t = (el.innerText ?? el.textContent ?? '').replace(/\s+/g, ' ').trim();
    return t || el.getAttribute('title') || '';
  };
  H.desc = (el) => {
    if (!el) return null;
    const cls = typeof el.className === 'string' ? el.className : el.getAttribute('class') || '';
    return `${el.tagName.toLowerCase()}${el.id ? '#' + el.id : ''}${cls ? '.' + cls.trim().split(/\s+/).slice(0, 2).join('.') : ''}`.slice(0, 60);
  };
  H.isDisabled = (el) => !!(el.disabled || el.getAttribute('aria-disabled') === 'true' || el.closest('fieldset:disabled') || el.closest('[inert]'));
  H.rect = (el) => { if (!el) return null; const r = el.getBoundingClientRect(); return { x: round(r.left), y: round(r.top), w: round(r.width), h: round(r.height), top: round(r.top), bottom: round(r.bottom), left: round(r.left), right: round(r.right) }; };
  const round = (n) => Math.round(n * 10) / 10;
  const docTop = (el) => Math.round(el.getBoundingClientRect().top + scrollY);

  // ---------- selector specs ----------
  // spec: string (CSS) | { css, role, name, text, within ('@sheet' | '@productRows' | '@<selKey>' | spec), inside, not, any, enabled, pos, bottomFixed, first, nth, last } | [spec, ...] (fallback chain: first non-empty wins)
  H.qa = (spec, root = document) => {
    if (spec == null) return [];
    if (Array.isArray(spec)) { for (const s of spec) { const r = H.qa(s, root); if (r.length) return r; } return []; }
    if (typeof spec === 'string') spec = { css: spec };
    let pool;
    if (spec.within) {
      let scopes = spec.within === '@productRows' ? H.productRows() : spec.within === '@sheet' ? (H.topSheet() ? [H.topSheet()] : [])
        : (typeof spec.within === 'string' && spec.within[0] === '@') ? H.q(spec.within.slice(1), root) : H.qa(spec.within, root);
      if (spec.withinText) {
        const exact = scopes.filter((s) => [...s.querySelectorAll('h1,h2,h3,h4,h5,h6,strong,b,[data-product-name]')].some((e) => e.textContent.trim() === spec.withinText));
        scopes = exact.length ? exact : scopes.filter((s) => (s.innerText || s.textContent || '').includes(spec.withinText));
      }
      const seen = new Set();
      pool = [];
      for (const sc of scopes) for (const el of sc.querySelectorAll(spec.css || '*')) if (!seen.has(el)) { seen.add(el); pool.push(el); }
      if (spec.includeScope) for (const sc of scopes) if ((!spec.css || sc.matches(spec.css)) && !seen.has(sc)) pool.push(sc);
    } else pool = [...root.querySelectorAll(spec.css || '*')];
    let out = pool.filter((el) => {
      if (!spec.any && !H.visible(el)) return false;
      if (spec.role && H.role(el) !== spec.role) return false;
      if (spec.name && !mkRx(spec.name).test(H.name(el))) return false;
      if (spec.text && !mkRx(spec.text).test(el.innerText || el.textContent || '')) return false;
      if (spec.not && el.matches(spec.not)) return false;
      if (spec.has && !el.querySelector(spec.has)) return false;
      if (spec.lacks && el.querySelector(spec.lacks)) return false;
      if (spec.inside && !el.closest(spec.inside)) return false;
      if (spec.outside && el.closest(spec.outside)) return false;
      if (spec.enabled === true && H.isDisabled(el)) return false;
      if (spec.enabled === false && !H.isDisabled(el)) return false;
      if (spec.pos && !spec.pos.split('|').includes(getComputedStyle(el).position)) return false;
      if (spec.bottomFixed) { const r = el.getBoundingClientRect(); if (!(r.bottom >= innerHeight - 2 && r.top > innerHeight * 0.4)) return false; }
      if (spec.tabindex != null && el.getAttribute('tabindex') !== String(spec.tabindex)) return false;
      return true;
    });
    if (spec.last) out = out.slice(-1);
    if (typeof spec.nth === 'number') out = out.slice(spec.nth, spec.nth + 1);
    if (spec.first) out = out.slice(0, 1);
    return out;
  };
  H.first = (spec, root) => H.qa(spec, root)[0] || null;
  H.sel = (key) => ui().sel[key];
  H.q = (key, root) => H.qa(ui().sel[key], root);
  H.q1 = (key, root) => H.q(key, root)[0] || null;
  H.count = (spec) => H.qa(spec).length;

  H.sheets = () => H.qa({ css: '[role="dialog"], dialog[open]' }).filter((d) => !d.closest('[data-harness-ignore]'));
  H.topSheet = () => { const s = H.sheets(); return s.length ? s[s.length - 1] : null; };

  // point to click: scroll into view (instant) and report whether the centre hits the element.
  H.point = (spec) => {
    const el = H.first(spec);
    if (!el) return null;
    el.scrollIntoView({ block: 'center', inline: 'center', behavior: 'instant' });
    const r = el.getBoundingClientRect();
    const x = r.left + r.width / 2, y = r.top + r.height / 2;
    const hit = document.elementFromPoint(x, y);
    const ok = !!hit && (hit === el || el.contains(hit) || (hit.closest && hit.closest('label') && hit.closest('label').contains(el)));
    return { x, y, ok, hit: H.desc(hit), target: H.desc(el), w: r.width, h: r.height };
  };
  H.softClick = (spec) => { const el = H.first(spec); if (!el) return false; el.click(); return true; };

  // ---------- AA contrast audit (DS-1 logic, plus scope info) ----------
  H.lum = (c) => {
    const m = c.match(/[\d.]+/g).map(Number);
    const f = (v) => { v /= 255; return v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; };
    return 0.2126 * f(m[0]) + 0.7152 * f(m[1]) + 0.0722 * f(m[2]);
  };
  H.alpha = (c) => { const m = c.match(/[\d.]+/g); return m && m.length > 3 ? Number(m[3]) : 1; };
  H.bg = (el) => { for (let e = el; e; e = e.parentElement) { const b = getComputedStyle(e).backgroundColor; if (H.alpha(b) > 0.5) return b; } return 'rgb(255,255,255)'; };
  H.audit = () => {
    const rows = [];
    const seen = new Set();
    for (const el of document.querySelectorAll('body *')) {
      if (!el.childNodes.length || ![...el.childNodes].some((n) => n.nodeType === 3 && n.textContent.trim())) continue;
      const cs = getComputedStyle(el);
      const r = el.getBoundingClientRect();
      if (cs.visibility === 'hidden' || cs.display === 'none' || r.width === 0 || r.height === 0 || Number(cs.opacity) === 0) continue;
      if (el.closest('[hidden],[inert]')) continue;
      if (parseFloat(cs.fontSize) === 0) continue;
      const fg = cs.color, bg = H.bg(el);
      const a = H.lum(fg), b = H.lum(bg);
      const ratio = (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
      const size = parseFloat(cs.fontSize), bold = Number(cs.fontWeight) >= 700;
      const large = size >= 24 || (bold && size >= 18.66);
      const disabled = el.closest('button:disabled,[aria-disabled="true"],input:disabled,select:disabled,fieldset:disabled');
      const need = large ? 3 : 4.5;
      const key = el.className + '|' + fg + '|' + bg + '|' + size + '|' + need + '|' + (disabled ? 'd' : 'e');
      if (seen.has(key)) continue;
      seen.add(key);
      rows.push({ text: el.textContent.trim().slice(0, 30), cls: String(el.className).slice(0, 60), fg, bg, size, ratio: Math.round(ratio * 100) / 100, need, disabled: !!disabled, inSheet: !!el.closest('[role="dialog"],dialog[open]'), pass: ratio >= need || !!disabled });
    }
    for (const el of document.querySelectorAll('input:not([type=hidden]):not([type=checkbox]):not([type=radio]), select, textarea')) {
      const cs = getComputedStyle(el);
      const r = el.getBoundingClientRect();
      if (cs.visibility === 'hidden' || cs.display === 'none' || r.width === 0 || el.closest('[hidden],[inert]')) continue;
      const text = el.tagName === 'SELECT' ? (el.selectedOptions[0]?.textContent ?? '') : (el.value || el.placeholder || '');
      if (!text.trim()) continue;
      const fg = el.value ? cs.color : (getComputedStyle(el, '::placeholder').color || cs.color), bg = H.bg(el);
      const a = H.lum(fg), b = H.lum(bg);
      const ratio = (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
      const size = parseFloat(cs.fontSize);
      const disabled = el.disabled || !!el.closest('fieldset:disabled');
      rows.push({ text: '[control] ' + text.trim().slice(0, 24), cls: el.tagName + '.' + String(el.className).slice(0, 40), fg, bg, size, ratio: Math.round(ratio * 100) / 100, need: 4.5, disabled, pass: ratio >= 4.5 || disabled, control: true, inSheet: !!el.closest('[role="dialog"],dialog[open]') });
    }
    return {
      total: rows.length, controls: rows.filter((r) => r.control).length,
      fails: rows.filter((r) => !r.pass),
      minFont: rows.length ? Math.min(...rows.map((r) => r.size)) : null,
      small: rows.filter((r) => r.size < 12).map((r) => r.size + 'px ' + r.text),
      disabledRows: rows.filter((r) => r.disabled).length,
    };
  };

  // ---------- horizontal overflow ----------
  H.overflow = () => {
    // Mobile emulation grows the layout viewport (innerWidth) when content overflows, so compare with the emulated device width.
    const vw = (cfg().width || innerWidth);
    const de = document.documentElement;
    const offenders = [];
    const inScroller = (el) => {
      // an ancestor that clips/scrolls horizontally and itself fits the viewport makes the overflow intentional
      for (let a = el.parentElement; a && a !== document.body && a !== de; a = a.parentElement) {
        const s = getComputedStyle(a);
        if (/(auto|scroll|hidden|clip)/.test(s.overflowX)) { const ar = a.getBoundingClientRect(); if (ar.right <= vw + 1 && ar.left >= -1) return true; }
      }
      return false;
    };
    for (const el of document.querySelectorAll('body *')) {
      if (offenders.length >= 8) break;
      const cs = getComputedStyle(el);
      if (cs.display === 'none') continue;
      const r = el.getBoundingClientRect();
      if (r.width === 0 && r.height === 0) continue;
      // (a) the box itself sticks out; (b) the box fits but its (visible) content overflows it, e.g. an unbreakable string
      let kind = null, right = r.right;
      if (cs.visibility !== 'hidden' && (r.right > vw + 1 || r.left < -1)) kind = 'box';
      else if (/visible/.test(cs.overflowX) && el.scrollWidth > el.clientWidth + 1 && el.clientWidth > 0 && r.left + el.scrollWidth > vw + 1 && cs.position !== 'fixed') { kind = 'content'; right = r.left + el.scrollWidth; }
      if (!kind || inScroller(el)) continue;
      offenders.push({ el: H.desc(el), kind, left: Math.round(r.left), right: Math.round(right), text: (el.textContent || '').trim().slice(0, 24) });
    }
    return { scrollWidth: de.scrollWidth, bodyScrollWidth: document.body.scrollWidth, innerWidth: innerWidth, deviceWidth: vw, over: Math.max(de.scrollWidth, document.body.scrollWidth, innerWidth) > vw + 0.5, offenders };
  };

  // ---------- interactive targets ----------
  const INTERACTIVE = 'a[href], button, input:not([type=hidden]), select, textarea, summary, [role=button], [role=link], [role=tab], [role=radio], [role=checkbox], [role=switch], [role=menuitem], [role=option], [role=combobox], [role=spinbutton], [tabindex]:not([tabindex="-1"]), [contenteditable=true]';
  const visuallyHiddenSelf = (el) => {
    const cs = getComputedStyle(el), r = el.getBoundingClientRect();
    const tiny = r.width <= 1.5 || r.height <= 1.5;
    const clip = cs.clip && /rect\(\s*0(px)?,?\s*0(px)?,?\s*0(px)?,?\s*0(px)?\s*\)/.test(cs.clip);
    const clipPath = /inset\(\s*(50%|100%)/.test(cs.clipPath || '');
    const transparent = Number(cs.opacity) === 0;
    return { tiny, clip, clipPath, transparent, hidden: tiny || clip || clipPath || (transparent && cs.position === 'absolute') };
  };
  H.targets = (opts) => {
    opts = opts || {};
    const sheet = H.topSheet();
    const sheetOpen = !!sheet;
    const rows = [];
    const srOnlyList = [];
    const seen = new Set();
    for (const el of document.querySelectorAll(INTERACTIVE)) {
      if (seen.has(el)) continue; seen.add(el);
      if (!H.rendered(el)) continue; // display:none (or in closed <details>): not exposed, not counted
      const cs = getComputedStyle(el);
      if (cs.visibility === 'hidden') continue;
      const r = el.getBoundingClientRect();
      const vh = visuallyHiddenSelf(el);
      const role = H.role(el);
      const isChoice = el.tagName === 'INPUT' && /^(radio|checkbox)$/i.test(el.type);
      let label = null;
      if (isChoice) label = (el.labels && el.labels[0]) || el.closest('label');
      const labelRect = label ? label.getBoundingClientRect() : null;
      let eff = r, effective = 'self';
      if (label && labelRect.width > 0) { eff = labelRect; effective = 'label'; }
      const insideSheet = !!(sheet && sheet.contains(el));
      const inertBg = sheetOpen && !insideSheet; // background under an open modal sheet
      const ariaHiddenAncestor = !!el.closest('[aria-hidden="true"]');
      const disabled = H.isDisabled(el);
      const srOnly = vh.hidden && !(isChoice && effective === 'label'); // a hidden radio/checkbox wrapped in a visible label is a styled custom control, not an AT-only element
      const focusable = !disabled && (el.tabIndex >= 0);
      const row = {
        tag: el.tagName.toLowerCase(), role, name: H.name(el).slice(0, 40), cls: String(el.className || '').slice(0, 40),
        self: { w: round(r.width), h: round(r.height) },
        effective: { w: round(eff.width), h: round(eff.height), source: effective },
        labelRect: label ? { w: round(labelRect.width), h: round(labelRect.height) } : null,
        inputVisuallyHidden: isChoice ? vh.hidden : undefined,
        disabled, srOnly, inertBackground: inertBg, ariaHiddenAncestor, insideSheet,
        inline: el.tagName === 'A' && !!el.closest('p, li') && [...el.parentElement.childNodes].some((n) => n !== el && n.nodeType === 3 && n.textContent.trim()),
        meets44: eff.width >= 43.5 && eff.height >= 43.5,
      };
      row.treatment = srOnly ? 'excluded:sr-only (not pointer-targetable; listed)' : disabled ? 'measured, excluded from failures: disabled' : inertBg ? 'measured, excluded from failures: inert background under open sheet' : 'counted';
      row.counted = !srOnly && !disabled && !inertBg;
      row.fail = row.counted && !row.meets44;
      if (srOnly) srOnlyList.push({ tag: row.tag, role, name: row.name, focusable, rect: { w: round(r.width), h: round(r.height) } });
      rows.push(row);
    }
    // Assistive-only text that is not itself interactive (visually hidden live regions etc.).
    const assistive = [];
    for (const el of document.querySelectorAll('body *')) {
      if (assistive.length >= 40) break;
      if (!(el.textContent || '').trim() || el.children.length > 3) continue;
      const cs = getComputedStyle(el);
      if (cs.display === 'none' || cs.visibility === 'hidden') continue;
      if (el.matches(INTERACTIVE) || /^(OPTION|OPTGROUP|SCRIPT|STYLE)$/.test(el.tagName)) continue;
      const vh = visuallyHiddenSelf(el);
      if (vh.hidden && (vh.tiny || vh.clip || vh.clipPath)) assistive.push({ tag: el.tagName.toLowerCase(), cls: String(el.className || '').slice(0, 40), role: el.getAttribute('role') || '', text: (el.textContent || '').trim().slice(0, 50), live: el.getAttribute('aria-live') || (el.getAttribute('role') === 'status' ? 'status' : '') });
    }
    const counted = rows.filter((r) => r.counted);
    const fails = rows.filter((r) => r.fail);
    return {
      total: rows.length, counted: counted.length, fails: fails.length,
      failList: fails.map((r) => ({ tag: r.tag, role: r.role, name: r.name, cls: r.cls, effective: r.effective, self: r.self, inline: r.inline })),
      minEffective: counted.length ? { w: Math.min(...counted.map((r) => r.effective.w)), h: Math.min(...counted.map((r) => r.effective.h)) } : null,
      disabledCount: rows.filter((r) => r.disabled).length,
      disabledList: rows.filter((r) => r.disabled).map((r) => ({ tag: r.tag, name: r.name, effective: r.effective })).slice(0, 30),
      srOnlyInteractive: srOnlyList, assistiveText: assistive,
      choiceControls: rows.filter((r) => r.tag === 'input' && (r.role === 'radio' || r.role === 'checkbox')).map((r) => ({ role: r.role, name: r.name, input: r.self, effective: r.effective, inputVisuallyHidden: r.inputVisuallyHidden, disabled: r.disabled })),
      sheetOpen, rows: opts.rows ? rows : undefined,
      policy: 'Counted = rendered, enabled, not sr-only, not inert background under an open sheet. Radio/checkbox wrapped in (or labelled by) a <label>: effective area = label rect, input rect recorded separately. sr-only/visually hidden elements are listed (srOnlyInteractive / assistiveText) and excluded; disabled controls are measured and listed (disabledList) but excluded from failures; inline links are counted unless disabled.',
    };
  };

  // ---------- fixed / sticky inventory ----------
  H.stickyList = () => {
    const out = [];
    for (const el of document.querySelectorAll('body *')) {
      const cs = getComputedStyle(el);
      if (cs.position !== 'sticky' && cs.position !== 'fixed') continue;
      if (!H.visible(el)) continue;
      const r = el.getBoundingClientRect();
      out.push({ el: H.desc(el), position: cs.position, rect: { top: Math.round(r.top), bottom: Math.round(r.bottom), w: Math.round(r.width), h: Math.round(r.height) }, top: cs.top, bottom: cs.bottom, role: H.role(el) });
    }
    return out;
  };

  // ---------- product rows ----------
  H.productRows = () => {
    let rows = H.q('productRow').filter((r) => !r.closest('[role="dialog"], dialog'));
    if (!rows.length && ui().productRowFallback) {
      const set = new Set();
      for (const b of document.querySelectorAll('main button, #root button')) {
        if (b.closest('nav, header, footer, [role="dialog"], dialog') || !H.visible(b)) continue;
        const row = b.closest('li, article, [role="listitem"]');
        if (row && /[¥￥]/.test(row.innerText)) set.add(row);
      }
      rows = [...set];
    }
    return rows;
  };
  H.rows = () => {
    const rows = H.productRows();
    const vh = innerHeight;
    const heights = rows.map((r) => r.getBoundingClientRect().height).sort((a, b) => a - b);
    const fully = rows.filter((r) => { const b = r.getBoundingClientRect(); return b.top >= 0 && b.bottom <= vh; }).length;
    const partly = rows.filter((r) => { const b = r.getBoundingClientRect(); return b.top < vh && b.bottom > 0; }).length;
    return {
      total: rows.length, fullyInFirstViewport: fully, partlyInFirstViewport: partly,
      firstTop: rows[0] ? docTop(rows[0]) : null,
      height: { min: Math.round(heights[0] ?? 0), median: Math.round(heights[Math.floor(heights.length / 2)] ?? 0), max: Math.round(heights.at(-1) ?? 0) },
      heights: heights.map(Math.round),
      withMedia: rows.filter((r) => r.querySelector('img')).length,
      imgInMain: document.querySelectorAll('main img').length,
    };
  };

  // ---------- topbar / footer / platform wording / fonts / DEMO ----------
  const PLATFORM_RX = /東島|TABLE ORDER|TOJIMA KITCHEN|OPEN TABLE|桌邊點餐/g;
  H.demoNotes = () => {
    const cands = [...document.querySelectorAll('[role="note"], aside, [aria-label="DEMO"]')].filter((el) => H.visible(el) && /DEMO/.test((el.getAttribute('aria-label') || '') + ' ' + (el.innerText || el.textContent || '')));
    // keep the innermost matches so a wrapper does not double count
    const inner = cands.filter((el) => !cands.some((o) => o !== el && el.contains(o)));
    const sheets = H.sheets();
    const inSheet = sheets.map((s) => inner.filter((n) => s.contains(n)).length);
    const page = inner.filter((n) => !sheets.some((s) => s.contains(n))).length;
    return { page, inSheet, total: inner.length, texts: inner.map((n) => (n.innerText || n.textContent || '').replace(/\s+/g, ' ').trim().slice(0, 80)), sheetsOpen: sheets.length };
  };
  H.topbarInfo = () => {
    const tb = H.q1('topbar');
    const text = tb ? (tb.innerText || '').replace(/\s+/g, ' ').trim() : null;
    const body = (document.body.innerText || '');
    const firstText = (() => {
      if (!tb) return null;
      const w = document.createTreeWalker(tb, NodeFilter.SHOW_TEXT);
      for (let n = w.nextNode(); n; n = w.nextNode()) { const t = n.textContent.trim(); if (t && n.parentElement && H.visible(n.parentElement)) return t; }
      return null;
    })();
    const storeRx = H.rx('storeName');
    const platformInTopbar = text ? (text.match(PLATFORM_RX) || []).length : null;
    const footer = H.q1('footer');
    return {
      found: !!tb, text, firstText, storeNameFirst: firstText ? storeRx.test(firstText) : false, storeNameInTopbar: text ? storeRx.test(text) : false,
      platformNamesInTopbar: platformInTopbar,
      platformNamesInPage: (body.match(PLATFORM_RX) || []).length,
      platformList: [...new Set(body.match(PLATFORM_RX) || [])],
      poweredByTojima: (body.match(/Powered by Tojima/gi) || []).length,
      footerText: footer ? (footer.innerText || '').replace(/\s+/g, ' ').trim().slice(0, 120) : null,
      h1: [...document.querySelectorAll('h1')].filter(H.visible).map((h) => h.innerText.trim().slice(0, 60)),
      eyebrowLike: [...document.querySelectorAll('.eyebrow')].filter(H.visible).map((e) => e.innerText.trim()),
      htmlLang: document.documentElement.lang, dataSurface: document.documentElement.getAttribute('data-surface'),
    };
  };
  H.fonts = () => {
    const root = H.q1('root') || document.querySelector('main') || document.body;
    const sheet = H.topSheet();
    const rf = getComputedStyle(root).fontFamily, bf = getComputedStyle(document.body).fontFamily, sf = sheet ? getComputedStyle(sheet).fontFamily : null;
    return {
      root: rf.slice(0, 160), body: bf.slice(0, 160), sheet: sf ? sf.slice(0, 160) : null,
      rootStartsSystemUi: /^\s*(system-ui|-apple-system|ui-sans-serif)/i.test(rf),
      sheetStartsSystemUi: sf ? /^\s*(system-ui|-apple-system|ui-sans-serif)/i.test(sf) : null,
      rootHasPingFangTC: /PingFang TC/i.test(rf), sheetHasPingFangTC: sf ? /PingFang TC/i.test(sf) : null,
      rootFontSize: getComputedStyle(root).fontSize,
    };
  };
  H.measure = () => {
    const nav = H.q1('categoryNav'), bar = H.q1('cartBar');
    const vh = innerHeight;
    const inView = (el) => { if (!el) return null; const r = el.getBoundingClientRect(); return r.top >= -0.5 && r.bottom <= vh + 0.5 && r.bottom > 0; };
    return {
      viewport: { w: innerWidth, h: innerHeight, dpr: devicePixelRatio, visibility: document.visibilityState, hasFocus: document.hasFocus() },
      scroll: { x: Math.round(scrollX), y: Math.round(scrollY), height: document.documentElement.scrollHeight, atBottom: Math.ceil(scrollY + innerHeight) >= document.documentElement.scrollHeight - 1 },
      overflow: H.overflow(),
      demo: H.demoNotes(),
      topbar: H.topbarInfo(),
      truncated: H.truncated(),
      fonts: H.fonts(),
      sticky: H.stickyList(),
      rows: H.rows(),
      categoryNav: nav ? { rect: H.rect(nav), inViewport: inView(nav), position: getComputedStyle(nav).position, itemCount: H.qa({ within: '@categoryNav', css: 'a, button, [role=tab], [role=link]' }).length } : null,
      cartBar: bar ? { rect: H.rect(bar), inViewport: inView(bar), position: getComputedStyle(bar).position, text: (bar.innerText || '').replace(/\s+/g, ' ').trim().slice(0, 100) } : null,
      sheet: (() => { const s = H.topSheet(); return s ? { rect: H.rect(s), count: H.sheets().length, heading: (s.querySelector('h1,h2,h3') || {}).innerText || null, labelledBy: s.getAttribute('aria-labelledby') || null, ariaModal: s.getAttribute('aria-modal') } : null; })(),
      health: H.health(),
    };
  };
  H.health = () => {
    const out = {};
    for (const [k, v] of Object.entries(ui().sel)) { try { out[k] = v == null ? null : H.qa(v).length; } catch (e) { out[k] = 'ERR ' + e.message; } }
    return out;
  };

  // ---------- focus ----------
  H.focusInfo = (el) => {
    const focused = el || document.activeElement;
    if (!focused || focused === document.body) return null;
    // A visually-hidden radio/checkbox draws its ring on the wrapping label (or a [data-focus-visible] child); measure that element.
    let e = focused;
    const fr = focused.getBoundingClientRect();
    if (focused.tagName === 'INPUT' && /^(radio|checkbox)$/i.test(focused.type) && (fr.width <= 2 || fr.height <= 2 || getComputedStyle(focused).opacity === '0')) {
      const label = (focused.labels && focused.labels[0]) || focused.closest('label');
      const cands = [label, focused.closest('[data-focus-visible="true"]'), ...(label ? label.querySelectorAll('[data-focus-visible="true"]') : [])].filter(Boolean);
      const ringy = cands.find((c) => { const s = getComputedStyle(c); return (s.outlineStyle !== 'none' && parseFloat(s.outlineWidth) > 0) || (s.boxShadow && s.boxShadow !== 'none'); });
      e = ringy || label || focused;
    }
    const cs = getComputedStyle(e), r = e.getBoundingClientRect();
    const ow = parseFloat(cs.outlineWidth) || 0, oo = parseFloat(cs.outlineOffset) || 0;
    const grow = Math.max(0, ow + oo);
    const ring = { l: r.left - grow, t: r.top - grow, r: r.right + grow, b: r.bottom + grow };
    let clippedBy = null;
    for (let a = e.parentElement; a; a = a.parentElement) {
      const s = getComputedStyle(a);
      if (s.overflowX === 'visible' && s.overflowY === 'visible') continue;
      const ar = a.getBoundingClientRect();
      if (ring.l < ar.left - 0.5 || ring.r > ar.right + 0.5 || ring.t < ar.top - 0.5 || ring.b > ar.bottom + 0.5) { clippedBy = H.desc(a); break; }
    }
    // also treat clip-path on the element / ancestors as potential clipping
    let clipPathBy = null;
    for (let a = e; a; a = a.parentElement) { const cp = getComputedStyle(a).clipPath; if (cp && cp !== 'none') { clipPathBy = H.desc(a); break; } }
    const hasOutline = cs.outlineStyle !== 'none' && ow > 0;
    const hasShadow = cs.boxShadow && cs.boxShadow !== 'none';
    return {
      tag: focused.tagName, role: H.role(focused), name: H.name(focused).slice(0, 30), cls: String(focused.className).slice(0, 50), ringOn: e === focused ? 'self' : H.desc(e),
      outline: cs.outlineStyle + ' ' + cs.outlineWidth + ' ' + cs.outlineColor, offset: cs.outlineOffset,
      hasVisibleRing: hasOutline || !!hasShadow, focusVisible: focused.matches(':focus-visible'),
      w: round(r.width), h: round(r.height), clippedBy, clipPathBy,
    };
  };
  // Tab stop analysis (overlap with category nav / cart bar / any fixed-sticky overlay).
  const GRID = 10;
  H.stop = () => {
    const el = document.activeElement;
    if (!el || el === document.body || el === document.documentElement) return { none: true };
    const r = el.getBoundingClientRect();
    const nav = H.q1('categoryNav'), bar = H.q1('cartBar');
    const sheet0 = H.topSheet();
    const inSheet0 = !!(sheet0 && sheet0.contains(el));
    // The element's own container is not an occluder; inside a modal sheet the page behind cannot cover anything.
    const navR = nav && !nav.contains(el) && !inSheet0 ? nav.getBoundingClientRect() : null;
    const barR = bar && !bar.contains(el) && !inSheet0 ? bar.getBoundingClientRect() : null;
    const generic = [];
    for (const o of (inSheet0 ? [] : document.querySelectorAll('body *'))) {
      const cs = getComputedStyle(o);
      if ((cs.position !== 'fixed' && cs.position !== 'sticky') || !H.visible(o)) continue;
      if (o.contains(el) || el.contains(o)) continue;
      generic.push({ d: H.desc(o), r: o.getBoundingClientRect() });
    }
    const inside = (rect, x, y) => rect && x >= rect.left && x <= rect.right && y >= rect.top && y <= rect.bottom;
    let n = 0, cNav = 0, cBar = 0, cNavBar = 0, cAny = 0, off = 0;
    const occluders = new Set();
    for (let i = 0; i < GRID; i++) for (let j = 0; j < GRID; j++) {
      const x = r.left + (r.width * (i + 0.5)) / GRID, y = r.top + (r.height * (j + 0.5)) / GRID;
      n++;
      if (x < 0 || y < 0 || x > innerWidth || y > innerHeight) off++;
      const a = inside(navR, x, y), b = inside(barR, x, y);
      if (a) cNav++; if (b) cBar++; if (a || b) cNavBar++;
      let any = a || b;
      for (const g of generic) if (inside(g.r, x, y)) { any = true; occluders.add(g.d); }
      if (any) cAny++;
    }
    const cx = r.left + r.width / 2, cy = r.top + r.height / 2;
    const top = document.elementFromPoint(Math.min(Math.max(cx, 0), innerWidth - 1), Math.min(Math.max(cy, 0), innerHeight - 1));
    const centerOccluded = !!top && !(top === el || el.contains(top) || top.contains(el));
    const sheet = H.topSheet();
    return {
      tag: el.tagName.toLowerCase(), role: H.role(el), name: H.name(el).slice(0, 40), cls: String(el.className || '').slice(0, 30), key: H.desc(el) + '|' + H.name(el).slice(0, 20) + '|' + Math.round(r.left) + ',' + Math.round(r.top + scrollY),
      w: round(r.width), h: round(r.height), top: round(r.top), bottom: round(r.bottom), inViewport: off < n, offscreenFraction: round(off / n),
      overlapNav: navR ? round(cNav / n) : null, overlapBar: barR ? round(cBar / n) : null, overlapNavBar: (navR || barR) ? round(cNavBar / n) : null, overlapAnyFixed: round(cAny / n),
      fullyHiddenNavBar: (navR || barR) ? cNavBar / n >= 0.999 : false, fullyHiddenAny: cAny / n >= 0.999,
      occluders: [...occluders].slice(0, 3), centerOccluded, insideSheet: !!(sheet && sheet.contains(el)), scrollY: Math.round(scrollY),
      isProductAction: H.isProductAction(el), disabled: H.isDisabled(el),
    };
  };
  H.isProductAction = (el) => {
    if (!el || !el.matches('button, a[href], [role=button]') || H.isDisabled(el)) return false;
    return H.productRows().some((row) => row.contains(el));
  };
  // Put the sequential-focus starting point at the very top of the document.
  H.resetFocusStart = () => {
    if (document.activeElement && document.activeElement !== document.body) document.activeElement.blur();
    window.scrollTo(0, 0);
    document.body.setAttribute('tabindex', '-1'); document.body.focus({ preventScroll: true }); document.body.removeAttribute('tabindex');
    getSelection().removeAllRanges();
    return true;
  };

  // ---------- assorted state readers ----------
  H.ariaHiddenState = () => {
    // Report which body-level branches are hidden from AT / inert; used for the "background inert while open, cleared after close" check.
    const sheet = H.topSheet();
    const kids = [...document.body.children].filter((c) => !['SCRIPT', 'STYLE', 'META', 'LINK'].includes(c.tagName));
    const branches = kids.map((c) => ({
      el: H.desc(c), hasSheet: !!sheet && c.contains(sheet), ariaHidden: c.getAttribute('aria-hidden') === 'true', inert: c.hasAttribute('inert') || c.inert === true,
      visible: H.visible(c), hasContent: (c.innerText || '').trim().length > 0,
    }));
    const anyDeepHidden = [...document.querySelectorAll('[aria-hidden="true"], [inert]')].filter((e) => !e.closest('[data-harness-ignore]') && (e.id === 'root' || e.parentElement === document.body || e.parentElement?.id === 'root')).map((e) => H.desc(e));
    const bodyStyle = getComputedStyle(document.body);
    return {
      sheetOpen: !!sheet, branches, hiddenBranches: anyDeepHidden,
      backgroundBranchesNotHidden: kids.filter((c) => !(sheet && c.contains(sheet)) && (c.innerText || '').trim() && H.visible(c) && c.getAttribute('aria-hidden') !== 'true' && !c.hasAttribute('inert')
        && ![...c.children].filter((k) => (k.innerText || '').trim()).every((k) => k.getAttribute('aria-hidden') === 'true' || k.hasAttribute('inert'))).map((c) => H.desc(c)),
      bodyPointerEvents: bodyStyle.pointerEvents, bodyOverflow: bodyStyle.overflow, htmlOverflow: getComputedStyle(document.documentElement).overflow,
      anyAriaHiddenLeft: [...document.querySelectorAll('body > [aria-hidden="true"], #root[aria-hidden="true"], #root > [aria-hidden="true"], [inert]')].filter((e) => (e.innerText || '').trim() || e.id === 'root').length,
    };
  };
  H.activeInfo = () => {
    const e = document.activeElement;
    if (!e) return null;
    const sheet = H.topSheet();
    return {
      desc: H.desc(e), tag: e.tagName.toLowerCase(), role: H.role(e), name: H.name(e).slice(0, 50), text: (e.innerText || e.value || '').replace(/\s+/g, ' ').trim().slice(0, 40), tabindex: e.getAttribute('tabindex'),
      isBody: e === document.body, insideSheet: !!(sheet && sheet.contains(e)), sheetOpen: !!sheet,
      isHeading: /^H[1-6]$/.test(e.tagName), headingText: /^H[1-6]$/.test(e.tagName) ? e.innerText.trim().slice(0, 40) : null,
    };
  };
  H.textIs = (rxKey, root) => { const t = (root ? root.innerText : document.body.innerText) || ''; return H.rx(rxKey).test(t); };
  H.bodyText = () => document.body.innerText || '';
  H.cartLines = () => H.q('cartLine').length || H.q('stepperMinus').length;
  H.localeState = () => ({ lang: document.documentElement.lang });
  H.openerDesc = (el) => { if (!el) return null; const row = el.closest('li, article, [data-product-key], [data-guest-cart-bar], nav'); return { tag: el.tagName, name: H.name(el).slice(0, 40), row: row ? (row.innerText || '').replace(/\s+/g, ' ').slice(0, 30) : '' }; };
  H.matchesOpener = () => { const d = window.__openerDesc, e = document.activeElement; if (!d || !e) return false; const n = H.openerDesc(e); return !!n && n.tag === d.tag && n.name === d.name && n.row === d.row; };
  H.orderTracks = (orderID) => {
    const cards = H.q('orderCard');
    // the table session accumulates orders: pick the card of the order just confirmed (id attribute or id text), else the first card
    const mine = orderID ? cards.find((e) => Object.values(e.dataset).includes(orderID) || (e.innerText || '').includes(orderID)) : null;
    const card = mine || cards[0] || (document.querySelector('[aria-current="step"]') && document.querySelector('[aria-current="step"]').closest('article, li, section')) || null;
    if (!card) return { cards: 0, card: null };
    const steps = [...card.querySelectorAll('[aria-current="step"]')];
    const info = steps.map((s) => {
      const list = s.closest('ol, ul') || s.parentElement;
      const heading = (list && list.getAttribute('aria-label')) || (list && list.getAttribute('aria-labelledby') && document.getElementById(list.getAttribute('aria-labelledby'))?.textContent) || (list && list.previousElementSibling && list.previousElementSibling.textContent) || '';
      return { current: (s.innerText || s.textContent || '').replace(/\s+/g, ' ').trim().slice(0, 40), track: heading.replace(/\s+/g, ' ').trim().slice(0, 30), listItems: list ? list.querySelectorAll('li').length : null, tag: s.tagName.toLowerCase() };
    });
    const liveRegions = [...card.querySelectorAll('[role="status"], [aria-live]')].map((e) => ({ role: e.getAttribute('role'), live: e.getAttribute('aria-live'), text: (e.textContent || '').replace(/\s+/g, ' ').trim().slice(0, 60), visible: H.visible(e) }));
    const legacyStatus = [...card.querySelectorAll('.guest-order-status')].map((e) => (e.textContent || '').replace(/\s+/g, ' ').trim().slice(0, 60));
    return { cards: cards.length, orderIdText: (card.innerText || '').match(/[a-f0-9]{32}/)?.[0] || null, steps: info, liveRegions, legacyStatus, text: (card.innerText || '').replace(/\s+/g, ' ').slice(0, 240) };
  };
  // visible elements whose text is cut off (single-line ellipsis / clipped overflow): the text is in the DOM but not readable
  H.truncated = () => {
    const out = [];
    for (const el of document.querySelectorAll('body *')) {
      if (el.closest('[inert],[aria-hidden="true"]') || !H.visible(el)) continue;
      const cs = getComputedStyle(el);
      if (!(el.scrollWidth > el.clientWidth + 1 && el.clientWidth >= 8)) continue;   // clientWidth < 8: sr-only / visually hidden text, intentional
      if (!(cs.textOverflow === 'ellipsis' || cs.overflowX === 'hidden' || cs.overflowX === 'clip')) continue;
      if (cs.overflowX === 'auto' || cs.overflowX === 'scroll') continue;
      if (![...el.childNodes].some((n) => n.nodeType === 3 && n.textContent.trim())) continue;
      out.push({ el: H.desc(el), text: (el.textContent || '').replace(/\s+/g, ' ').trim().slice(0, 50), clientW: el.clientWidth, scrollW: el.scrollWidth });
    }
    return out.slice(0, 8);
  };
  H.cartBarText = () => { const b = H.q1('cartBar'); return b ? (b.innerText || '').replace(/\s+/g, ' ').trim() : null; };
  H.invalid = () => [...document.querySelectorAll('[aria-invalid="true"]')].filter(H.visible).map((e) => ({ desc: H.desc(e), describedby: e.getAttribute('aria-describedby'), describedText: (e.getAttribute('aria-describedby') || '').split(/\s+/).map((id) => document.getElementById(id)?.textContent?.trim()).filter(Boolean).join(' | ').slice(0, 80) }));
  H.disabledReason = (spec) => {
    const el = H.first(spec);
    if (!el) return null;
    const ids = (el.getAttribute('aria-describedby') || '').split(/\s+/).filter(Boolean);
    return { found: true, disabled: H.isDisabled(el), ariaDisabled: el.getAttribute('aria-disabled'), nativeDisabled: !!el.disabled, describedby: ids, describedText: ids.map((id) => document.getElementById(id)?.textContent?.trim()).filter(Boolean).join(' | ').slice(0, 160) };
  };
  H.localStorageOrderLink = () => {
    try {
      const raw = JSON.parse(localStorage.getItem('tojima.my-orders.v1') || '[]');
      const g = raw[0];
      if (!g) return null;
      const u = new URL('/order', location.origin);
      u.hash = new URLSearchParams({ order: g.order_id, grant: g.order_read_token, expires: g.expires_at }).toString();
      return u.toString();
    } catch { return null; }
  };
  return true;
})();
