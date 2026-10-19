// DS-2 P-89 §5.6: Playwright WebKit (not iOS Safari) check at 390x844, ja/zh.
// node webkit-check.mjs <credsFile> <outDir>
import { webkit } from 'playwright-core';
import { readFileSync, mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

const [credsFile, out] = process.argv.slice(2);
const creds = JSON.parse(readFileSync(credsFile, 'utf8'));
mkdirSync(out, { recursive: true });
const report = { engine: 'playwright-webkit-2370', viewport: '390x844', runs: [] };

const inDialog = (page) => page.evaluate(() => !!document.activeElement?.closest('[role="dialog"]'));
const activeInfo = (page) => page.evaluate(() => {
  const a = document.activeElement;
  return { tag: a?.tagName, text: (a?.getAttribute('aria-label') || a?.textContent || '').trim().slice(0, 40), inDialog: !!a?.closest('[role="dialog"]') };
});
async function cycle(page, key, n = 20) {
  let escaped = 0;
  for (let i = 0; i < n; i++) { await page.keyboard.press(key); if (!(await inDialog(page))) escaped++; }
  return escaped;
}
async function bgLock(page) {
  // Mobile WebKit has no wheel events: scroll the sheet's own scroll area, then try to scroll the page.
  return page.evaluate(async () => {
    const before = window.scrollY;
    const dialog = document.querySelector('[role="dialog"]');
    const scrollers = [...dialog.querySelectorAll('*')].filter((el) => el.scrollHeight > el.clientHeight + 1 && /(auto|scroll)/.test(getComputedStyle(el).overflowY));
    const inner = scrollers[0];
    if (inner) { inner.scrollTop = inner.scrollHeight; await new Promise((r) => setTimeout(r, 300)); }
    const afterInner = window.scrollY;
    window.scrollBy(0, 400); await new Promise((r) => setTimeout(r, 300));
    const afterPageScroll = window.scrollY;
    window.scrollTo(0, before);
    return {
      pageScrollBefore: before, afterInnerScroll: afterInner, afterWindowScrollBy: afterPageScroll,
      backgroundMovedByInnerScroll: before !== afterInner,
      innerScroller: inner ? { scrollTop: inner.scrollTop, scrollHeight: inner.scrollHeight, clientHeight: inner.clientHeight } : null,
      htmlOverflow: getComputedStyle(document.documentElement).overflow, bodyOverflow: getComputedStyle(document.body).overflow,
      overflowX: document.documentElement.scrollWidth > window.innerWidth,
    };
  });
}

const browser = await webkit.launch();
for (const locale of ['ja', 'zh']) {
  const run = { locale, steps: {} };
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 3, isMobile: true, hasTouch: true, locale: locale === 'ja' ? 'ja-JP' : 'zh-TW' });
  const page = await ctx.newPage();
  const shot = (name) => page.screenshot({ path: join(out, `${locale}-${name}.png`) });
  try {
    await page.goto(creds.guest_url);
    await page.locator('[data-add-product]:not([disabled])').first().waitFor({ timeout: 20000 });
    if (locale === 'zh') { await page.getByRole('button', { name: '繁中' }).click(); await page.waitForTimeout(500); }
    await shot('01-menu');
    run.steps.menu = { stickyNav: await page.evaluate(() => getComputedStyle(document.querySelector('[data-category-nav]')).position), overflowX: await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth) };

    // find a product that opens an option sheet; option-less products add directly
    const adds = page.locator('[data-add-product]:not([disabled])');
    const n = await adds.count();
    let opened = false;
    for (let i = 0; i < n && !opened; i++) {
      await adds.nth(i).scrollIntoViewIfNeeded();
      await adds.nth(i).focus(); await page.keyboard.press('Enter');
      await page.waitForTimeout(900);
      opened = await page.locator('[role="dialog"]').isVisible().catch(() => false);
    }
    run.steps.optionSheet = { opened, focusOnOpen: await activeInfo(page) };
    await shot('02-option-sheet');
    run.steps.optionSheet.scroll = await bgLock(page);
    await shot('03-option-sheet-scrolled');
    run.steps.optionSheet.tabEscapes = await cycle(page, 'Tab');
    run.steps.optionSheet.shiftTabEscapes = await cycle(page, 'Shift+Tab');
    // satisfy required groups, then add
    const groups = page.locator('[role="dialog"] fieldset');
    for (let g = 0; g < await groups.count(); g++) {
      const first = groups.nth(g).locator('input:not([disabled])').first();
      if (await first.count()) await first.check({ force: true }).catch(() => undefined);
    }
    await page.locator('[role="dialog"]').getByRole('button', { name: locale === 'ja' ? 'カートに追加' : '加入購物車' }).click();
    await page.locator('[role="dialog"]').waitFor({ state: 'detached', timeout: 10000 }).catch(() => undefined);
    run.steps.optionSheet.closedAfterAdd = !(await page.locator('[role="dialog"]').isVisible().catch(() => false));
    run.steps.optionSheet.focusAfterClose = await activeInfo(page);

    // add one option-less product so the cart has 2 lines
    for (let i = 0; i < n; i++) {
      await adds.nth(i).click(); await page.waitForTimeout(900);
      if (await page.locator('[role="dialog"]').isVisible().catch(() => false)) { await page.keyboard.press('Escape'); await page.waitForTimeout(500); continue; }
      break;
    }
    await page.waitForTimeout(1500);
    await page.evaluate(() => window.scrollTo(0, document.body.scrollHeight)); await page.waitForTimeout(400);
    await shot('04-menu-bottom-cartbar');
    run.steps.bottom = await page.evaluate(() => {
      const r = (s) => document.querySelector(s)?.getBoundingClientRect();
      const nav = r('[data-category-nav]'), bar = r('[data-testid="cart-bar"]');
      return { navTop: nav?.top, barBottom: bar?.bottom, innerHeight: window.innerHeight, navVisible: !!nav && nav.top >= 0 && nav.bottom <= innerHeight, barVisible: !!bar && bar.top >= 0 && bar.bottom <= innerHeight };
    });

    // cart sheet
    await page.locator('[data-testid="cart-bar"] button').focus(); await page.keyboard.press('Enter');
    await page.locator('[role="dialog"]').waitFor({ timeout: 10000 });
    await page.waitForTimeout(600);
    run.steps.cartSheet = { focusOnOpen: await activeInfo(page), lines: await page.locator('[data-cart-line]').count() };
    await shot('05-cart-sheet');
    run.steps.cartSheet.scroll = await bgLock(page);
    run.steps.cartSheet.tabEscapes = await cycle(page, 'Tab');
    run.steps.cartSheet.shiftTabEscapes = await cycle(page, 'Shift+Tab');

    // preview
    await page.locator('[role="dialog"]').getByRole('button', { name: locale === 'ja' ? '金額を確認する' : '確認金額' }).click();
    await page.waitForTimeout(1500);
    run.steps.preview = { focus: await activeInfo(page) };
    await shot('06-preview');
    run.steps.preview.tabEscapes = await cycle(page, 'Tab');
    await page.keyboard.press('Escape'); await page.waitForTimeout(800);
    run.steps.preview.closedByEsc = !(await page.locator('[role="dialog"]').isVisible().catch(() => false));
    run.steps.preview.focusAfterEsc = await activeInfo(page);
    run.steps.preview.leftoverInert = await page.evaluate(() => document.querySelectorAll('[inert], [aria-hidden="true"]:not(svg):not([aria-hidden] *)').length);
    await shot('07-after-esc');
  } catch (e) { run.error = String(e).split('\n')[0]; await shot('error'); }
  report.runs.push(run);
  await ctx.close();
}
await browser.close();
writeFileSync(join(out, 'webkit-report.json'), JSON.stringify(report, null, 2));
console.log(JSON.stringify(report, null, 2));
