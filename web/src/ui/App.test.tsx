// @vitest-environment jsdom
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Cart } from '../api/types';
import { checkoutNavigation } from '../api/checkout';
import { App, displayTableLocator, isCurrentMenuRead } from './App';

const table = { mode: 'DIRECT_SCAN', current_session_id: 'session-1', open: true };
const grant = { table_join_token: 'memory-only-grant', expires_at: '2030-01-01T00:00:00Z', session: { id: 'session-1', mode: 'DIRECT_SCAN' } };
const guestMenu = { currency: 'JPY', price_mode: 'TAX_INCLUSIVE', categories: [{ key: 'food', name: 'お食事', position: 1, products: [] }] };
const cart = {
  session_id: 'session-1', generation: 1, version: 0,
  lines: [{ line_id: 'line-hidden', product_key: 'removed-from-menu', product_name: 'Previously added item', product_available: false, quantity: 1, options: [] }],
};
let root: Root | undefined;
let host: HTMLDivElement | undefined;

afterEach(async () => {
  if (root) await act(async () => root?.unmount());
  root = undefined;
  host?.remove();
  host = undefined;
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  vi.useRealTimers();
  sessionStorage.clear();
  setVisibilityState('visible');
});

describe('guest app mount', () => {
  it('queues a forced menu read after a periodic read when commit is rejected by a new hold', async () => {
    vi.useFakeTimers();
    window.history.replaceState({}, '', '/?table=forced-menu-refresh');
    const oldMenu = deferred<Response>();
    const before: Cart = { session_id: 'session-1', generation: 3, version: 2, lines: [{ line_id: 'line-1', product_key: 'meal', product_name: '定食', product_available: true, quantity: 1, options: [] }] };
    const after: Cart = { ...before, version: 3, lines: [{ ...before.lines[0]!, product_available: false, unavailable_code: 'COMPANY_EMERGENCY_HOLD' }] };
    const preview = { session_id: 'session-1', generation: 3, version: 2, fingerprint: 'preview-1', quote: { lines: [{ line_id: 'line-1', product_key: 'meal', name: '定食', base_gross_jpy: '900', quantity: 1, options: [] }], tax: { currency: 'JPY', price_mode: 'INCLUSIVE', total_gross_jpy: '900', total_net_jpy: '818', total_tax_jpy: '82', buckets: [] } } };
    let menuReads = 0;
    let activeMenuReads = 0;
    let maxActiveMenuReads = 0;
    const menu = (held: boolean) => ({ currency: 'JPY', price_mode: 'TAX_INCLUSIVE', categories: [{ key: 'food', name: '料理', position: 1, products: [{ key: 'meal', name: '定食', description: '', effective_price_minor: '900', position: 1, availability: 'AVAILABLE', selectable: !held, company_emergency_hold_active: held, option_groups: [] }] }] });
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith('/v1/guest/tables/forced-menu-refresh')) return json(table);
      if (url.endsWith('/v1/guest/tables/forced-menu-refresh/join')) return json(grant);
      if (url.endsWith('/v1/guest/tables/forced-menu-refresh/menu?amounts=string')) {
        menuReads++;
        activeMenuReads++;
        maxActiveMenuReads = Math.max(maxActiveMenuReads, activeMenuReads);
        try {
          const response = menuReads === 2 ? await oldMenu.promise : json(menu(menuReads >= 3));
          return response;
        } finally { activeMenuReads--; }
      }
      if (url.endsWith('/v1/guest/sessions/session-1/cart/preview')) return json(preview);
      if (url.endsWith('/v1/guest/sessions/session-1/cart/commit')) return json({ error: 'unavailable', cart: after }, 409);
      if (url.endsWith('/v1/guest/sessions/session-1/cart')) return json(before);
      if (url.endsWith('/v1/guest/sessions/session-1/orders')) return json({ orders: [] });
      if (url.endsWith('/v1/guest/sessions/session-1/operations')) return json({ orders: [], cash_enabled: true });
      throw new Error(`Unexpected API request: ${url}`);
    }));
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
    host = document.createElement('div'); document.body.append(host); root = createRoot(host);

    await act(async () => { root?.render(<App demo />); for (let i = 0; i < 30; i++) await Promise.resolve(); });
    expect(menuReads).toBe(1);
    await openCartSheet(host);
    const previewButton = getButton(host, '金額を確認する');
    await act(async () => { previewButton.click(); for (let i = 0; i < 20; i++) await Promise.resolve(); });
    expect(document.body.textContent).toContain('¥900');

    await act(async () => { await vi.advanceTimersByTimeAsync(15_000); });
    expect(menuReads).toBe(2);
    expect(activeMenuReads).toBe(1);
    const confirmButton = getButton(host, 'この内容で注文を確定');
    await act(async () => { confirmButton.click(); for (let i = 0; i < 30; i++) await Promise.resolve(); });
    expect(menuReads).toBe(2);

    await act(async () => { oldMenu.resolve(json(menu(false))); for (let i = 0; i < 40; i++) await Promise.resolve(); });
    expect(menuReads).toBe(3);
    expect(activeMenuReads).toBe(0);
    expect(maxActiveMenuReads).toBe(1);
    expect(host.textContent).toContain('販売停止中');
    expect(document.body.textContent).toContain('注文できない商品があります');
    expect(host.querySelector('[data-product-key] button')?.hasAttribute('disabled')).toBe(true);
  });

  it('revalidates the menu only while visible and reflects ON then OFF without clearing store PAUSED state', async () => {
    vi.useFakeTimers();
    setVisibilityState('hidden');
    window.history.replaceState({}, '', '/?table=menu-refresh');
    let menuReads = 0;
    const currentCart: Cart = { session_id: 'session-1', generation: 1, version: 0, lines: [] };
    const menu = (held: boolean, availability: string) => ({
      currency: 'JPY', price_mode: 'TAX_INCLUSIVE', categories: [{ key: 'food', name: '料理', position: 1, products: [{
        key: 'meal', name: '定食', description: '', effective_price_minor: '900', position: 1,
        availability, selectable: availability === 'AVAILABLE' && !held, company_emergency_hold_active: held, option_groups: [],
      }] }],
    });
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith('/v1/guest/tables/menu-refresh')) return json(table);
      if (url.endsWith('/v1/guest/tables/menu-refresh/join')) return json(grant);
      if (url.endsWith('/v1/guest/tables/menu-refresh/menu?amounts=string')) {
        menuReads++;
        return json(menu(menuReads === 2, menuReads === 3 ? 'PAUSED' : 'AVAILABLE'));
      }
      if (url.endsWith('/v1/guest/sessions/session-1/cart')) return json(currentCart);
      if (url.endsWith('/v1/guest/sessions/session-1/orders')) return json({ orders: [] });
      if (url.endsWith('/v1/guest/sessions/session-1/operations')) return json({ orders: [], cash_enabled: true });
      throw new Error(`Unexpected API request: ${url}`);
    }));
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
    host = document.createElement('div'); document.body.append(host); root = createRoot(host);

    await act(async () => {
      root?.render(<App />);
      for (let i = 0; i < 30; i++) await Promise.resolve();
    });
    expect(menuReads).toBe(1);

    await act(async () => { await vi.advanceTimersByTimeAsync(30_000); });
    expect(menuReads).toBe(1);

    setVisibilityState('visible');
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')); await vi.advanceTimersByTimeAsync(15_000); });
    expect(menuReads).toBe(2);
    expect(host.textContent).toContain('販売停止中');
    expect(host.querySelector('[data-product-key] button')?.hasAttribute('disabled')).toBe(true);

    await act(async () => { await vi.advanceTimersByTimeAsync(15_000); });
    expect(menuReads).toBe(3);
    const productCard = host.querySelector('[data-product-key]');
    expect(productCard?.textContent).not.toContain('販売停止中');
    expect(productCard?.textContent).toContain('現在注文できません');
    expect(productCard?.querySelector('button')?.hasAttribute('disabled')).toBe(true);
  });

  it('rejects an older menu response when the active session or grant changes', () => {
    expect(isCurrentMenuRead(1, 2, 'session-a', 'session-b', 'grant-a', 'grant-b')).toBe(false);
    expect(isCurrentMenuRead(2, 2, 'session-b', 'session-b', 'grant-b', 'grant-b')).toBe(true);
    expect(isCurrentMenuRead(2, 3, 'session-b', 'session-b', 'grant-b', 'grant-b')).toBe(false);
  });

  it('shows a company hold in both menu and retained cart, blocks additions and quote, and still allows removal', async () => {
    const locator = 'held-table';
    window.history.replaceState({}, '', `/?table=${locator}`);
    let currentCart: Cart = { session_id: 'session-1', generation: 3, version: 2, lines: [{ line_id: 'held-line', product_key: 'meal', product_name: '定食', product_available: false, unavailable_code: 'COMPANY_EMERGENCY_HOLD', quantity: 1, options: [] }] };
    const requested: string[] = [];
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input); requested.push(`${init.method ?? 'GET'} ${url}`);
      if (url.endsWith(`/v1/guest/tables/${locator}`)) return json({ mode: 'DIRECT_SCAN', current_session_id: 'session-1', open: true });
      if (url.endsWith(`/v1/guest/tables/${locator}/join`)) return json(grant);
      if (url.endsWith(`/v1/guest/tables/${locator}/menu?amounts=string`)) return json({ currency: 'JPY', price_mode: 'TAX_INCLUSIVE', categories: [{ key: 'food', name: '料理', position: 1, products: [{ key: 'meal', name: '定食', description: '', effective_price_minor: '900', position: 1, availability: 'AVAILABLE', selectable: false, company_emergency_hold_active: true, option_groups: [] }] }] });
      if (url.includes('/v1/guest/sessions/session-1/cart/lines/held-line') && init.method === 'DELETE') { currentCart = { ...currentCart, version: 3, lines: [] }; return json(currentCart); }
      if (url.endsWith('/v1/guest/sessions/session-1/cart')) return json(currentCart);
      if (url.endsWith('/v1/guest/sessions/session-1/orders')) return json({ orders: [] });
      if (url.endsWith('/v1/guest/sessions/session-1/operations')) return json({ orders: [], cash_enabled: true });
      throw new Error(`Unexpected API request: ${url}`);
    }));
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
    host = document.createElement('div'); document.body.append(host); root = createRoot(host);

    await act(async () => { root?.render(<App />); await new Promise((resolve) => setTimeout(resolve, 0)); });

    expect(host.textContent).toContain('販売停止中');
    expect(host.querySelector('[data-product-key] button')?.hasAttribute('disabled')).toBe(true);
    expect(host.textContent).toContain('定食');
    await openCartSheet(host);
    expect(document.body.querySelector('[data-cart-line]')?.textContent).toContain('販売停止中');
    const previewButton = within(document.body).queryByRole('button', { name: '金額を確認する' });
    expect(previewButton?.hasAttribute('disabled')).toBe(true);

    await clickButton(document.body, '削除');
    expect(document.body.querySelector('[data-cart-line]')).toBeNull();
    expect(requested.some((call) => call.includes('/cart/preview'))).toBe(false);
    expect(requested.some((call) => call.startsWith('DELETE ') && call.includes('/cart/lines/held-line'))).toBe(true);
  });

  it('invalidates an existing preview and reloads the held cart and menu after a hold rejection', async () => {
    const locator = 'hold-after-preview';
    window.history.replaceState({}, '', `/?table=${locator}`);
    const availableCart: Cart = { session_id: 'session-1', generation: 3, version: 2, lines: [{ line_id: 'held-line', product_key: 'meal', product_name: '定食', product_available: true, quantity: 1, options: [] }] };
    const heldCart: Cart = { ...availableCart, version: 3, lines: [{ ...availableCart.lines[0]!, product_available: false, unavailable_code: 'COMPANY_EMERGENCY_HOLD' }] };
    let menuReads = 0;
    const preview = { session_id: 'session-1', generation: 3, version: 2, fingerprint: 'old-preview', quote: { lines: [{ line_id: 'held-line', product_key: 'meal', name: '定食', base_gross_jpy: '900', quantity: 1, options: [] }], tax: { currency: 'JPY', price_mode: 'INCLUSIVE', total_gross_jpy: '900', total_net_jpy: '818', total_tax_jpy: '82', buckets: [] } } };
    const requested: string[] = [];
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input); requested.push(`${init.method ?? 'GET'} ${url}`);
      if (url.endsWith(`/v1/guest/tables/${locator}`)) return json({ mode: 'DIRECT_SCAN', current_session_id: 'session-1', open: true });
      if (url.endsWith(`/v1/guest/tables/${locator}/join`)) return json(grant);
      if (url.endsWith(`/v1/guest/tables/${locator}/menu?amounts=string`)) {
        menuReads++;
        return json({ currency: 'JPY', price_mode: 'TAX_INCLUSIVE', categories: [{ key: 'food', name: '料理', position: 1, products: [{ key: 'meal', name: '定食', description: '', effective_price_minor: '900', position: 1, availability: 'AVAILABLE', selectable: menuReads === 1, company_emergency_hold_active: menuReads > 1, option_groups: [] }] }] });
      }
      if (url.endsWith('/v1/guest/sessions/session-1/cart/preview')) return json(preview);
      if (url.endsWith('/v1/guest/sessions/session-1/cart/commit')) return json({ error: 'unavailable', cart: heldCart }, 409);
      if (url.endsWith('/v1/guest/sessions/session-1/cart')) return json(availableCart);
      if (url.endsWith('/v1/guest/sessions/session-1/orders')) return json({ orders: [] });
      if (url.endsWith('/v1/guest/sessions/session-1/operations')) return json({ orders: [], cash_enabled: true });
      throw new Error(`Unexpected API request: ${url}`);
    }));
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
    host = document.createElement('div'); document.body.append(host); root = createRoot(host);

    await act(async () => { root?.render(<App />); await new Promise((resolve) => setTimeout(resolve, 0)); });
    await openCartSheet(host);
    await clickButton(host, '金額を確認する');
    expect(document.body.textContent).toContain('¥900');
    const cashOption = within(document.body).getByRole('radio', { name: /店頭で現金/ });
    await act(async () => { cashOption?.click(); await new Promise((resolve) => setTimeout(resolve, 0)); });
    await clickButton(host, 'この内容で注文を確定');

    expect(document.body.textContent).toContain('販売停止中');
    expect(document.body.textContent).not.toContain('この内容で注文を確定');
    expect(document.body.textContent).toContain('注文できない商品があります');
    expect(menuReads).toBeGreaterThan(1);
    expect(requested.some((call) => call.endsWith('/cart/commit'))).toBe(true);
  });

  it('keeps hook order while the real table entry flow resolves into the shared cart', async () => {
    window.history.replaceState({}, '', '/?table=table-1');
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith('/v1/guest/tables/table-1')) return json(table);
      if (url.endsWith('/v1/guest/tables/table-1/join')) return json(grant);
      if (url.endsWith('/v1/guest/tables/table-1/menu?amounts=string')) return json(guestMenu);
      if (url.endsWith('/v1/guest/sessions/session-1/cart')) return json(cart);
      if (url.endsWith('/v1/guest/sessions/session-1/orders')) return json({ orders: [{
        order_id: 'order-1', session_id: 'session-1', generation: 1, payment_method: 'CASH', requires_staff_acceptance: true,
        quote: { lines: [{ line_id: 'line-1', product_key: 'meal', name: 'カレー', base_gross_jpy: '1100', quantity: 1, options: [{ group_key: 'rice', option_key: 'large', name: '大盛り', price_gross_jpy: '110' }] }], tax: { currency: 'JPY', price_mode: 'INCLUSIVE', total_gross_jpy: '1210', total_net_jpy: '1100', total_tax_jpy: '110', buckets: [] } },
        created_at: '2026-10-18T00:00:00Z',
      }] });
      if (url.endsWith('/v1/guest/sessions/session-1/operations')) return json({ orders: [{ id: 'order-1', display_state: 'STANDARD', payment_status: 'UNPAID', fulfillment_status: 'WAITING', requires_staff_acceptance: true, staff_accepted: false, cash_mode: 'PREPAID', total_gross_jpy: '1210', effective_total_gross_jpy: '1210', effective_lines: [{ product_name: 'カレー', quantity: 1, line_total_jpy: '1210', option_names: ['大盛り'] }], adjusted: false, refunds: [], refunded_total_jpy: '0' }], cash_enabled: true });
      throw new Error(`Unexpected API request: ${url}`);
    });
    vi.stubGlobal('fetch', fetchMock);
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
    host = document.createElement('div');
    document.body.append(host);
    root = createRoot(host);

    await act(async () => {
      root?.render(<App />);
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    await openCartSheet(host!);
    expect(host.textContent).toContain('東島');
    expect(host.textContent).toContain('共有カート');
    expect(host.textContent).toContain('お食事');
    expect(document.body.textContent).toContain('Previously added item');
    expect(host.textContent).toContain('テーブルの QR から参加');
    expect(host.textContent).not.toContain('DIRECT_SCAN');
    const requested = fetchMock.mock.calls.map(([url]) => String(url));
    expect(requested).toContain('/v1/guest/tables/table-1');
    expect(requested).toContain('/v1/guest/tables/table-1/join');
    expect(requested).toContain('/v1/guest/tables/table-1/menu?amounts=string');
    expect(requested).toContain('/v1/guest/sessions/session-1/cart');
    expect(requested).toContain('/v1/guest/sessions/session-1/orders');
    expect(sessionStorage.getItem('tojima.table-join.session-1')).toBe('memory-only-grant');

    await clickButton(host!, '閉じる');
    await act(async () => {
      getButton(host!, '注文履歴').click();
    });
    expect(host.textContent).toContain('カレー × 1');
    expect(host.textContent).toContain('¥1,210');
  });

  it('keeps demo pricing labeled, tolerates omitted option groups, and commits only the synthetic cash method', async () => {
    const demoLocator = 'demo-shironagasukujira-table-05';
    window.history.replaceState({}, '', `/?table=${demoLocator}`);
    const initialCart = { session_id: 'session-1', generation: 1, version: 0, lines: [] };
    const addedCart = { ...initialCart, version: 1, lines: [{ line_id: 'line-1', product_key: 'meal', product_name: 'シロナガスクジラ定食', product_available: true, quantity: 1, options: [] }] };
    const preview = { session_id: 'session-1', generation: 1, version: 1, fingerprint: 'demo-preview', quote: { lines: [{ line_id: 'line-1', product_key: 'meal', name: 'シロナガスクジラ定食', base_gross_jpy: '960', quantity: 1, options: [] }], tax: { currency: 'JPY', price_mode: 'INCLUSIVE', total_gross_jpy: '960', total_net_jpy: '873', total_tax_jpy: '87', buckets: [] } } };
    const committed = { outcome: 'COMMITTED', order: { order_id: 'demo-order', session_id: 'session-1', generation: 1, payment_method: 'CASH', requires_staff_acceptance: false, quote: preview.quote, created_at: '2026-10-18T00:00:00Z' }, cart: { ...addedCart, generation: 2, version: 0, lines: [] } };
    const menu = { currency: 'JPY', price_mode: 'TAX_INCLUSIVE', categories: [{ key: 'food', name: '定食', position: 0, products: [{ key: 'meal', name: 'シロナガスクジラ定食', description: 'DEMO / 歴史資料の参考価格・現行店内価格未確認', effective_price_minor: '960', position: 0, availability: 'AVAILABLE', selectable: true }] }] };
    const writes: Array<{ url: string; method: string; body: string }> = [];
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input);
      if (url.endsWith(`/v1/guest/tables/${demoLocator}`)) return json(table);
      if (url.endsWith(`/v1/guest/tables/${demoLocator}/join`)) return json(grant);
      if (url.endsWith(`/v1/guest/tables/${demoLocator}/menu?amounts=string`)) return json(menu);
      if (url.endsWith('/v1/guest/sessions/session-1/cart')) return json(initialCart);
      if (url.endsWith('/v1/guest/sessions/session-1/orders')) return json({ orders: [committed.order] });
      if (url.endsWith('/v1/guest/sessions/session-1/operations')) return json({ orders: [], cash_enabled: true });
      if (url.endsWith('/cart/lines') && init.method === 'POST') { writes.push({ url, method: init.method, body: String(init.body) }); return json(addedCart); }
      if (url.endsWith('/cart/preview') && init.method === 'POST') return json(preview);
      if (url.endsWith('/cart/commit') && init.method === 'POST') { writes.push({ url, method: init.method, body: String(init.body) }); return json(committed); }
      throw new Error(`Unexpected API request: ${url}`);
    }));
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
    host = document.createElement('div'); document.body.append(host); root = createRoot(host);

    await act(async () => { root?.render(<App demo />); await new Promise((resolve) => setTimeout(resolve, 0)); });
    expect(host.textContent).toContain('博多餃子酒場 シロナガスクジラ');
    expect(host.textContent).toContain('参考価格 · 現行店内価格未確認');
    expect(host.textContent).toContain('実店舗への注文・支払いは行われません');
    expect(host.textContent).toContain('歴史資料の参考価格');
    expect(host.textContent).toContain('シロナガスクジラ定食');
    expect(host.textContent).toContain('テーブル 05');
    expect(host.textContent).not.toContain(demoLocator);
    await clickButton(host, 'カートに追加');
    await openCartSheet(host);
    await clickButton(host, '金額を確認する');
    expect(document.body.textContent).not.toContain('クレジットカード');
    expect(document.body.textContent).toContain('現金（デモ・テスト専用）');
    await clickButton(host, 'この内容で注文を確定');

    expect(JSON.parse(writes.find((write) => write.url.endsWith('/cart/commit'))!.body).payment_method).toBe('CASH');
    expect(writes.some((write) => write.url.endsWith('/checkout-context'))).toBe(false);
  });

  it('shortens the demo table locator on the join card while preserving normal locators', async () => {
    const demoLocator = 'demo-shironagasukujira-table-05';
    window.history.replaceState({}, '', `/?table=${demoLocator}`);
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith(`/v1/guest/tables/${demoLocator}`)) return json({ mode: 'SESSION_PIN', open: false });
      throw new Error(`Unexpected API request: ${url}`);
    }));
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
    host = document.createElement('div'); document.body.append(host); root = createRoot(host);

    await act(async () => { root?.render(<App demo />); await new Promise((resolve) => setTimeout(resolve, 0)); });

    expect(host.querySelector('.table-chip strong')?.textContent).toBe('05');
    expect(host.textContent).not.toContain(demoLocator);
    expect(displayTableLocator(demoLocator, false)).toBe(demoLocator);
  });

  it.each([
    { name: 'STAFF_OPEN PAY_LATER', mode: 'STAFF_OPEN', cashMode: 'PAY_LATER', fulfillment: 'ACCEPTED', requires: false, expected: '受付済み · 未払い' },
    { name: 'STAFF_OPEN PREPAID', mode: 'STAFF_OPEN', cashMode: 'PREPAID', fulfillment: 'WAITING', requires: false, expected: '調理開始待ち · 未払い' },
    { name: 'SESSION_PIN PAY_LATER', mode: 'SESSION_PIN', cashMode: 'PAY_LATER', fulfillment: 'ACCEPTED', requires: false, expected: '受付済み · 未払い' },
    { name: 'SESSION_PIN PREPAID', mode: 'SESSION_PIN', cashMode: 'PREPAID', fulfillment: 'WAITING', requires: false, expected: '調理開始待ち · 未払い' },
    { name: 'DIRECT_SCAN requires staff acceptance', mode: 'DIRECT_SCAN', cashMode: undefined, fulfillment: 'WAITING', requires: true, expected: '店員の受付待ち · 未払い' },
  ])('uses server acceptance requirements for $name', async (scenario) => {
    const mode = scenario.mode as 'STAFF_OPEN' | 'SESSION_PIN' | 'DIRECT_SCAN';
    const order = { order_id: 'order-1', session_id: 'session-1', generation: 1, payment_method: scenario.cashMode ? 'CASH' : 'STRIPE', requires_staff_acceptance: scenario.requires, total_gross_jpy: '1100', created_at: '2026-10-18T00:00:00Z' };
    const guestOperation = { display_state: 'STANDARD', id: 'order-1', payment_status: 'UNPAID', fulfillment_status: scenario.fulfillment, requires_staff_acceptance: scenario.requires, staff_accepted: false, ...(scenario.cashMode ? { cash_mode: scenario.cashMode } : {}) };
    await mountGuestStatus(mode, order, guestOperation);
    await act(async () => {
      getButton(host!, '注文履歴').click();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(host?.querySelector('[data-order-status]')?.textContent, host?.textContent).toBe(scenario.expected);
    if (!scenario.requires) expect(host?.querySelector('[data-order-status]')?.textContent).not.toContain('店員の受付待ち');
    if (scenario.cashMode === 'PREPAID') {
      await clickButton(host!, '繁中');
      expect(host?.querySelector('[data-order-status]')?.textContent).toBe('等待開始製作 · 尚未付款');
    }
  });

  it.each([
    ['REQUESTED', '返金処理中', '退款處理中'], ['PROCESSING', '返金処理中', '退款處理中'],
    ['FAILED', '返金を確認中です。店舗にお問い合わせください', '退款確認中，請洽店家'], ['SUCCEEDED', '返金済み ¥330', '已退款 ¥330'],
  ] as const)('shows %s refund and current adjusted content in history', async (refundState, jaMessage, zhMessage) => {
    const operation = { id: 'order-1', display_state: 'STANDARD', payment_status: 'PAID', fulfillment_status: 'SERVED', requires_staff_acceptance: false, staff_accepted: true, total_gross_jpy: '660', effective_total_gross_jpy: '330', effective_lines: [{ product_name: 'ほうじ茶', quantity: 1, line_total_jpy: '330', option_names: ['茶葉多め'] }], adjusted: true, refunds: [{ status: refundState, amount_jpy: '330', method: 'STRIPE' }], refunded_total_jpy: refundState === 'SUCCEEDED' ? '330' : '0' };
    await mountGuestStatus('DIRECT_SCAN', stripeOrder('order-1'), operation);
    await clickButton(host!, '注文履歴');
    expect(host?.textContent).toContain('ほうじ茶 × 1'); expect(host?.textContent).toContain('¥330'); expect(host?.textContent).toContain('¥660');
    expect(host?.textContent).toContain('茶葉多め');
    expect(host?.textContent).toContain('店舗により注文内容が変更されました'); expect(host?.textContent).toContain(jaMessage);
    if (refundState !== 'SUCCEEDED') expect(host?.textContent).not.toContain('返金済み');
    await clickButton(host!, '繁中'); expect(host?.textContent).toContain(zhMessage);
  });

  it.each(['ja', 'zh'] as const)('distinguishes same-product variants in adjusted history (%s)', async (locale) => {
    const operation = { id: 'order-1', display_state: 'STANDARD', payment_status: 'PAID', fulfillment_status: 'SERVED', requires_staff_acceptance: false, staff_accepted: true, total_gross_jpy: '990', effective_total_gross_jpy: '660', effective_lines: [
      { product_name: 'ほうじ茶', quantity: 1, line_total_jpy: '330', option_names: ['無糖'] },
      { product_name: 'ほうじ茶', quantity: 1, line_total_jpy: '330', option_names: ['甘さ控えめ'] },
    ], adjusted: true, refunds: [], refunded_total_jpy: '0' };
    await mountGuestStatus('DIRECT_SCAN', stripeOrder('order-1'), operation);
    if (locale === 'zh') await clickButton(host!, '繁中');
    await clickButton(host!, locale === 'ja' ? '注文履歴' : '已送出訂單');
    expect(host?.textContent).toContain('ほうじ茶 × 1');
    expect(host?.textContent).toContain('無糖'); expect(host?.textContent).toContain('甘さ控えめ');
    expect(host?.textContent).toContain('¥660'); expect(host?.textContent).toContain('¥990');
  });

  it.each(['ja', 'zh'] as const)('retains confirmed adjustment and refund details during a stale refresh, then recovers (%s)', async (locale) => {
    let reads = 0;
    const operation = { id: 'order-1', display_state: 'STANDARD', payment_status: 'PAID', fulfillment_status: 'SERVED', requires_staff_acceptance: false, staff_accepted: true, total_gross_jpy: '660', effective_total_gross_jpy: '330', effective_lines: [{ product_name: 'ほうじ茶', quantity: 1, line_total_jpy: '330', option_names: ['茶葉多め'] }], adjusted: true, refunds: [{ status: 'PROCESSING', amount_jpy: '330', method: 'STRIPE' }], refunded_total_jpy: '0' };
    await mountGuestStatus('DIRECT_SCAN', stripeOrder('order-1'), operation, async () => {
      reads++;
      if (reads === 2) throw new TypeError('offline');
      return json({ orders: [reads === 1 ? operation : { ...operation, refunds: [{ status: 'SUCCEEDED', amount_jpy: '330', method: 'STRIPE' }], refunded_total_jpy: '330' }], cash_enabled: true });
    });
    await clickButton(host!, '注文履歴');
    if (locale === 'zh') await clickButton(host!, '繁中');
    const pending = locale === 'ja' ? '返金処理中' : '退款處理中';
    const success = locale === 'ja' ? '返金済み ¥330' : '已退款 ¥330';
    expect(host?.textContent).toContain(pending); expect(host?.textContent).toContain('¥330'); expect(host?.textContent).toContain('¥660'); expect(host?.textContent).toContain('茶葉多め');
    vi.useFakeTimers();
    await act(async () => { setVisibilityState('hidden'); document.dispatchEvent(new Event('visibilitychange')); setVisibilityState('visible'); document.dispatchEvent(new Event('visibilitychange')); });
    await act(async () => { await vi.advanceTimersByTimeAsync(20_000); });
    expect(reads).toBe(2); expect(host?.textContent).toContain(pending); expect(host?.textContent).toContain('¥330'); expect(host?.textContent).toContain('¥660'); expect(host?.textContent).toContain('茶葉多め');
    expect(host?.textContent).toContain(locale === 'ja' ? '最新の状況を確認できません。表示は前回確認時の内容です。' : '無法確認最新狀態，目前顯示上次確認的內容。');
    await act(async () => { await vi.advanceTimersByTimeAsync(20_000); });
    expect(reads).toBe(3); expect(host?.textContent).toContain(success); expect(host?.textContent).toContain('¥330'); expect(host?.textContent).toContain('茶葉多め');
  });

  it('does not present the immutable order amount as current when guest operations are unavailable', async () => {
    await mountGuestStatus('DIRECT_SCAN', { ...stripeOrder('order-1'), total_gross_jpy: '660' }, undefined, async () => json({ error: 'temporarily_unavailable' }, 503));
    await clickButton(host!, '注文履歴');
    expect(host?.textContent).toContain('最新の注文内容と金額を確認できません。');
    expect(host?.textContent).not.toContain('¥660');
  });

  it.each([['ja', 'この注文は取り消されました', '返金済み ¥1,100'], ['zh', '此訂單已取消', '已退款 ¥1,100']] as const)('shows refunded whole-order cancellation in history (%s)', async (locale, message, refunded) => {
    await mountGuestStatus('DIRECT_SCAN', stripeOrder('order-1'), { id: 'order-1', display_state: 'CANCELLED', payment_status: 'UNKNOWN', fulfillment_status: 'CANCELLED', requires_staff_acceptance: false, staff_accepted: true, total_gross_jpy: '1100', effective_total_gross_jpy: '0', effective_lines: [], adjusted: true, refunds: [{ status: 'SUCCEEDED', amount_jpy: '1100', method: 'STRIPE' }], refunded_total_jpy: '1100' });
    if (locale === 'zh') await clickButton(host!, '繁中');
    await clickButton(host!, locale === 'ja' ? '注文履歴' : '已送出訂單');
    expect(host?.querySelector('[data-order-status]')?.textContent).toBe(message);
    expect(host?.textContent).toContain(refunded);
    expect(host?.querySelector('[data-checkout-panel]')).toBeNull();
  });

  it('shows a VOIDED guest order as cancelled without implying payment succeeded', async () => {
    await mountGuestStatus('DIRECT_SCAN', stripeOrder('order-1'), { id: 'order-1', display_state: 'CANCELLED', payment_status: 'VOIDED', fulfillment_status: 'CANCELLED', requires_staff_acceptance: false, staff_accepted: true, total_gross_jpy: '1100', effective_total_gross_jpy: '0', effective_lines: [], adjusted: true, refunds: [], refunded_total_jpy: '0' });
    await clickButton(host!, '注文履歴');
    expect(host?.querySelector('[data-order-status]')?.textContent).toBe('この注文は取り消されました');
    expect(host?.textContent).not.toContain('支払済み');
    expect(host?.querySelector('[data-checkout-panel]')).toBeNull();
  });

  it('shows paid review in both history languages and follows the ordinary session poll through release', async () => {
    const operation = { id: 'order-1', display_state: 'PAID_UNDER_REVIEW', payment_status: 'PAID', fulfillment_status: 'WAITING', requires_staff_acceptance: false, staff_accepted: true };
    await mountGuestStatus('DIRECT_SCAN', stripeOrder('order-1'), operation);
    await clickButton(host!, '注文履歴');
    expect(host?.querySelector('[data-order-status]')?.textContent).toBe('お支払いを確認しました。店舗でご注文を確認中です');
    expect(host?.textContent).not.toMatch(/調理中|返金/);
    await clickButton(host!, '繁中');
    expect(host?.querySelector('[data-order-status]')?.textContent).toBe('已確認付款，店家正在確認您的訂單');
    expect(host?.textContent).not.toMatch(/製作中|退款/);
    // Reschedule the existing visible history interval onto the fake clock.
    vi.useFakeTimers();
    await act(async () => { setVisibilityState('hidden'); document.dispatchEvent(new Event('visibilitychange')); setVisibilityState('visible'); document.dispatchEvent(new Event('visibilitychange')); });
    const fetcher = vi.mocked(fetch);
    const before = fetcher.mock.calls.filter(([url]) => String(url).endsWith('/operations')).length;
    await act(async () => { await vi.advanceTimersByTimeAsync(20_001); });
    expect(fetcher.mock.calls.filter(([url]) => String(url).endsWith('/operations')).length).toBeGreaterThan(before);
    expect(host?.querySelector('[data-order-status]')?.textContent).toBe('已確認付款，店家正在確認您的訂單');
    operation.display_state = 'STANDARD'; operation.fulfillment_status = 'PREPARING';
    await act(async () => { await vi.advanceTimersByTimeAsync(20_001); });
    expect(host?.querySelector('[data-order-status]')?.textContent).toBe('製作中 · 已付款');
  });

  it.each(['FUTURE_SUCCESS', 'UNKNOWN', undefined])('keeps unsupported history display state %s neutral despite paid/served raw states', async (display_state) => {
    await mountGuestStatus('DIRECT_SCAN', stripeOrder('order-1'), { id: 'order-1', display_state, payment_status: 'PAID', fulfillment_status: 'SERVED', requires_staff_acceptance: false, staff_accepted: true });
    await clickButton(host!, '注文履歴');
    expect(host?.querySelector('[data-order-status]')?.textContent).toBe('店舗に確認してください');
    expect(host?.querySelector('[data-checkout-panel]')).toBeNull();
    await clickButton(host!, '繁中');
    expect(host?.querySelector('[data-order-status]')?.textContent).toBe('請洽服務人員確認');
  });

  it.each(['network', '503'] as const)('retains confirmed review through a %s operations poll failure and recovers on the next poll', async (failure) => {
    let reads = 0;
    await mountGuestStatus('DIRECT_SCAN', stripeOrder('order-1'), undefined, async () => {
      reads++;
      if (reads === 2) {
        if (failure === 'network') throw new TypeError('Network unavailable');
        return json({ error: 'temporarily_unavailable' }, 503);
      }
      return json({ orders: [{ id: 'order-1', display_state: reads === 1 ? 'PAID_UNDER_REVIEW' : 'STANDARD', payment_status: 'PAID', fulfillment_status: reads === 1 ? 'WAITING' : 'PREPARING', requires_staff_acceptance: false, staff_accepted: true }], cash_enabled: false });
    });
    await clickButton(host!, '注文履歴');
    expect(reads).toBe(1);
    expect(host?.querySelector('[data-order-status]')?.textContent).toBe('お支払いを確認しました。店舗でご注文を確認中です');
    expect(host?.querySelector('[data-checkout-panel]')).toBeNull();

    vi.useFakeTimers();
    await act(async () => { setVisibilityState('hidden'); document.dispatchEvent(new Event('visibilitychange')); setVisibilityState('visible'); document.dispatchEvent(new Event('visibilitychange')); });
    await act(async () => { await vi.advanceTimersByTimeAsync(20_000); });
    expect(reads).toBe(2);
    expect(host?.querySelector('[data-order-status]')?.textContent).toBe('お支払いを確認しました。店舗でご注文を確認中です');
    expect(host?.textContent).toContain('最新の状況を確認できません。表示は前回確認時の内容です。');
    expect(host?.querySelector('[data-checkout-panel]')).toBeNull();
    expect(vi.mocked(fetch).mock.calls.some(([url]) => String(url).includes('/checkout'))).toBe(false);

    await act(async () => { getButton(host!, '繁中').click(); });
    expect(host?.querySelector('[data-order-status]')?.textContent).toBe('已確認付款，店家正在確認您的訂單');
    expect(host?.textContent).toContain('無法確認最新狀態，目前顯示上次確認的內容。');

    await act(async () => { await vi.advanceTimersByTimeAsync(20_000); });
    expect(reads).toBe(3);
    expect(host?.querySelector('[data-order-status]')?.textContent).toBe('製作中 · 已付款');
    expect(host?.textContent).not.toContain('無法確認最新狀態，目前顯示上次確認的內容。');
    expect(host?.querySelector('[data-checkout-panel]')).not.toBeNull();
  });

  it.each(['missing', 'network', '503'] as const)('renders %s initial operations neutrally without checkout controls', async (result) => {
    await mountGuestStatus('DIRECT_SCAN', stripeOrder('order-1'), undefined, async () => {
      if (result === 'network') throw new TypeError('Network unavailable');
      return result === '503' ? json({ error: 'temporarily_unavailable' }, 503) : json({ orders: [], cash_enabled: false });
    });
    await clickButton(host!, '注文履歴');
    expect(host?.querySelector('[data-order-status]')?.textContent).toBe('店舗に確認してください');
    expect(host?.querySelector('[data-checkout-panel]')).toBeNull();
    expect(host?.textContent).not.toContain('表示は前回確認時の内容です');
    await clickButton(host!, '繁中');
    expect(host?.querySelector('[data-order-status]')?.textContent).toBe('請洽服務人員確認');
  });

  it('withholds checkout controls while a confirmed STANDARD snapshot is stale', async () => {
    let reads = 0;
    await mountGuestStatus('DIRECT_SCAN', stripeOrder('order-1'), undefined, async () => {
      reads++;
      return reads === 2 ? json({ error: 'temporarily_unavailable' }, 503) : json({ orders: [standardOperation(stripeOrder('order-1'))], cash_enabled: false });
    });
    await clickButton(host!, '注文履歴');
    expect(host?.querySelector('[data-checkout-panel]')).not.toBeNull();
    await clickButton(host!, '共有内容を更新');
    expect(host?.querySelector('[data-order-status]')?.textContent).toBe('調理開始待ち · 支払確認中');
    expect(host?.textContent).toContain('最新の状況を確認できません。表示は前回確認時の内容です。');
    expect(host?.querySelector('[data-checkout-panel]')).toBeNull();
    await clickButton(host!, '共有内容を更新');
    expect(host?.querySelector('[data-checkout-panel]')).not.toBeNull();
    expect(host?.textContent).not.toContain('表示は前回確認時の内容です');
  });

  it('refreshes the menu without changing the cart and leaves requoted orders for explicit reconfirmation', async () => {
    window.history.replaceState({}, '', '/?table=table-1');
    const pricedMenu = (amount: string) => ({
      currency: 'JPY', price_mode: 'TAX_INCLUSIVE',
      categories: [{ key: 'food', name: 'お食事', position: 1, products: [{
        key: 'meal', name: '定食', effective_price_minor: amount, position: 1, availability: 'AVAILABLE', selectable: true, option_groups: [],
      }] }],
    });
    const openCart = {
      session_id: 'session-1', generation: 1, version: 4,
      lines: [{ line_id: 'line-1', product_key: 'meal', product_name: '定食', product_available: true, quantity: 1, options: [] }],
    };
    const quoted = {
      session_id: 'session-1', generation: 1, version: 4, fingerprint: 'quote-4',
      quote: {
        lines: [{ line_id: 'line-1', product_key: 'meal', name: '定食', base_gross_jpy: '150', options: [], quantity: 1 }],
        tax: { currency: 'JPY', price_mode: 'INCLUSIVE', total_gross_jpy: '150', total_net_jpy: '137', total_tax_jpy: '13', buckets: [] },
      },
    };
    let menuReads = 0;
    let previewCalls = 0;
    let commitCalls = 0;
    const paymentFlow: string[] = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith('/v1/guest/tables/table-1')) return json(table);
      if (url.endsWith('/v1/guest/tables/table-1/join')) return json(grant);
      if (url.endsWith('/v1/guest/tables/table-1/menu?amounts=string')) {
        menuReads += 1;
        return json(pricedMenu(menuReads === 1 ? '100' : menuReads === 2 ? '150' : '200'));
      }
      if (url.endsWith('/v1/guest/sessions/session-1/cart')) return json(openCart);
      if (url.endsWith('/v1/guest/sessions/session-1/orders')) return json({ orders: [] });
      if (url.endsWith('/v1/guest/sessions/session-1/cart/preview')) {
        previewCalls += 1;
        return json(quoted);
      }
      if (url.endsWith('/v1/guest/sessions/session-1/checkout-context') && init?.method === 'POST') {
        paymentFlow.push('checkout-context');
        expect(new Headers(init.headers).get('authorization')).toBe('Bearer memory-only-grant');
        return new Response(null, { status: 204 });
      }
      if (url.endsWith('/v1/guest/sessions/session-1/cart/commit') && init?.method === 'POST') {
        commitCalls += 1;
        paymentFlow.push('commit');
        return json({ error: 'requote_required', cart: openCart }, 409);
      }
      throw new Error(`Unexpected API request: ${url}`);
    });
    vi.stubGlobal('fetch', fetchMock);
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
    host = document.createElement('div');
    document.body.append(host);
    root = createRoot(host);

    await act(async () => {
      root?.render(<App />);
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(host.textContent).toContain('¥100');
    expect(host.textContent).toContain('定食');

    await openCartSheet(host!);
    await act(async () => {
      getButton(host!, '共有内容を更新').click();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(host.textContent).toContain('¥150');
    expect(host.textContent).toContain('定食');
    expect(menuReads).toBe(2);

    await act(async () => {
      getButton(host!, '金額を確認する').click();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(document.body.textContent).toContain('この内容で注文を確定');
    expect(document.body.textContent).toContain('クレジットカード');

    await act(async () => {
      getButton(host!, 'この内容で注文を確定').click();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(menuReads).toBe(3);
    expect(previewCalls).toBe(1);
    expect(commitCalls).toBe(1);
    expect(paymentFlow).toEqual(['checkout-context', 'commit']);
    expect(document.body.textContent).toContain('¥200');
    expect(document.body.textContent).toContain('定食');
    expect(document.body.textContent).not.toContain('この内容で注文を確定');
    expect(document.body.textContent).toContain('金額を確認する');
  });

  it('checks one Stripe order on demand and navigates only to its private resume response', async () => {
    const order = stripeOrder('order-resume');
    const assign = vi.spyOn(checkoutNavigation, 'assign').mockImplementation(() => undefined);
    const fetchMock = await mountCheckoutOrders([order], async (url, init) => {
      if (url.endsWith('/orders/order-resume/checkout') && init.method !== 'POST') return json(checkoutStatus('order-resume', 'OPEN', true));
      if (url.endsWith('/orders/order-resume/checkout/resume')) return json({ url: 'https://checkout.example.test/private-session', expires_at: '2026-10-18T12:30:00Z' });
      throw new Error(`Unexpected checkout API request: ${url}`);
    });

    expect(fetchMock.mock.calls.some(([input]) => String(input).includes('/checkout'))).toBe(false);
    await clickButton(host!, '支払い状況を確認');
    expect(host?.textContent).toContain('お支払い手続きができます');
    expect(host?.textContent).toContain('支払いを続ける');
    expect(host?.textContent).not.toContain('checkout.example.test');
    await clickButton(host!, '支払いを続ける');
    expect(assign).toHaveBeenCalledWith('https://checkout.example.test/private-session');
    expect(host?.textContent).not.toContain('checkout.example.test');
    expect([...Array(sessionStorage.length)].map((_, index) => sessionStorage.getItem(sessionStorage.key(index) ?? '')).join(' ')).not.toContain('checkout.example.test');
  });

  it('does not send the Stripe order when the original-browser context bootstrap fails', async () => {
    window.history.replaceState({}, '', '/?table=table-1');
    const menu = { currency: 'JPY', price_mode: 'TAX_INCLUSIVE', categories: [{ key: 'food', name: '食事', position: 1, products: [{ key: 'meal', name: '定食', effective_price_minor: '1000', position: 1, availability: 'AVAILABLE', selectable: true, option_groups: [] }] }] };
    const openCart = { session_id: 'session-1', generation: 1, version: 2, lines: [{ line_id: 'line-1', product_key: 'meal', product_name: '定食', product_available: true, quantity: 1, options: [] }] };
    const quoted = { session_id: 'session-1', generation: 1, version: 2, fingerprint: 'fp-1', quote: { lines: [{ line_id: 'line-1', product_key: 'meal', name: '定食', base_gross_jpy: '1000', quantity: 1, options: [] }], tax: { currency: 'JPY', price_mode: 'INCLUSIVE', total_gross_jpy: '1000', total_net_jpy: '909', total_tax_jpy: '91', buckets: [] } } };
    let commits = 0;
    const calls: string[] = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input);
      if (url === '/v1/guest/tables/table-1') return json(table);
      if (url === '/v1/guest/tables/table-1/join') return json(grant);
      if (url === '/v1/guest/tables/table-1/menu?amounts=string') return json(menu);
      if (url.endsWith('/sessions/session-1/cart') && !url.endsWith('/preview')) return json(openCart);
      if (url.endsWith('/sessions/session-1/orders')) return json({ orders: [] });
      if (url.endsWith('/sessions/session-1/operations')) return json({ orders: [], cash_enabled: false });
      if (url.endsWith('/cart/preview')) return json(quoted);
      if (url.endsWith('/checkout-context')) { calls.push('context'); return json({ error: 'temporary_failure' }, 503); }
      if (url.endsWith('/cart/commit')) { commits += 1; calls.push('commit'); return json({ error: 'unexpected_commit' }, 500); }
      throw new Error(`Unexpected API request: ${url} ${init.method ?? 'GET'}`);
    });
    vi.stubGlobal('fetch', fetchMock);
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
    host = document.createElement('div'); document.body.append(host); root = createRoot(host);
    await act(async () => { root?.render(<App />); await new Promise((resolve) => setTimeout(resolve, 0)); });
    await openCartSheet(host!);
    await clickButton(host!, '金額を確認する');
    await clickButton(host!, 'この内容で注文を確定');
    expect(calls).toEqual(['context']);
    expect(commits).toBe(0);
    expect(document.body.textContent).toContain('注文は送信されていません');
    expect(document.body.textContent).not.toContain('前回の操作を照合して再試行');
  });

  it('bootstraps before a new Stripe commit and selects only that attempt for status polling', async () => {
    window.history.replaceState({}, '', '/?table=table-1');
    const menu = { currency: 'JPY', price_mode: 'TAX_INCLUSIVE', categories: [{ key: 'food', name: '食事', position: 1, products: [{ key: 'meal', name: '定食', effective_price_minor: '1000', position: 1, availability: 'AVAILABLE', selectable: true, option_groups: [] }] }] };
    const openCart = { session_id: 'session-1', generation: 1, version: 2, lines: [{ line_id: 'line-1', product_key: 'meal', product_name: '定食', product_available: true, quantity: 1, options: [] }] };
    const emptyCart = { ...openCart, generation: 2, version: 0, lines: [] };
    const quoted = { session_id: 'session-1', generation: 1, version: 2, fingerprint: 'fp-1', quote: { lines: [{ line_id: 'line-1', product_key: 'meal', name: '定食', base_gross_jpy: '1000', quantity: 1, options: [] }], tax: { currency: 'JPY', price_mode: 'INCLUSIVE', total_gross_jpy: '1000', total_net_jpy: '909', total_tax_jpy: '91', buckets: [] } } };
    const order = stripeOrder('order-created');
    const flow: string[] = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input);
      if (url === '/v1/guest/tables/table-1') return json(table);
      if (url === '/v1/guest/tables/table-1/join') return json(grant);
      if (url === '/v1/guest/tables/table-1/menu?amounts=string') return json(menu);
      if (url.endsWith('/sessions/session-1/cart/preview')) return json(quoted);
      if (url.endsWith('/sessions/session-1/cart') && init.method !== 'POST') return json(flow.includes('commit') ? emptyCart : openCart);
      if (url.endsWith('/sessions/session-1/orders') && !url.includes('/checkout')) return json({ orders: flow.includes('commit') ? [order] : [] });
      if (url.endsWith('/sessions/session-1/operations')) return json({ orders: flow.includes('commit') ? [standardOperation(order)] : [], cash_enabled: false });
      if (url.endsWith('/checkout-context')) { flow.push('bootstrap'); return new Response(null, { status: 204 }); }
      if (url.endsWith('/cart/commit')) { flow.push('commit'); return json({ outcome: 'COMMITTED', order, cart: emptyCart }); }
      if (url.endsWith('/orders/order-created/checkout')) { flow.push('status'); return json(checkoutStatus('order-created', 'CREATE_PENDING', false)); }
      throw new Error(`Unexpected API request: ${url}`);
    });
    vi.stubGlobal('fetch', fetchMock);
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
    host = document.createElement('div'); document.body.append(host); root = createRoot(host);
    await act(async () => { root?.render(<App />); await new Promise((resolve) => setTimeout(resolve, 0)); });
    await openCartSheet(host!);
    await clickButton(host!, '金額を確認する');
    await clickButton(host!, 'この内容で注文を確定');
    expect(flow.slice(0, 3)).toEqual(['bootstrap', 'commit', 'status']);
    expect(document.body.textContent).toContain('注文を確認しています');
    expect(document.body.textContent).not.toContain('支払いを続ける');
  });

  it.each([
    { state: 'OPEN', title: 'can ask staff to continue when the original proof is unavailable', expected: 'お支払い手続きができます', support: true },
    { state: 'UNKNOWN', title: 'keeps an unknown outcome informational and offers no retry', expected: 'お支払い状況を確認しています', support: true },
    { state: 'PAID', title: 'does not offer resume for a paid order', expected: 'お支払い済み', support: false },
    { state: 'EXPIRED', title: 'does not offer resume for an expired order', expected: 'お支払い期限が切れました', support: false },
  ] as const)('$title', async ({ state, expected, support }) => {
    await mountCheckoutOrders([stripeOrder('order-state')], async (url) => {
      if (url.endsWith('/orders/order-state/checkout')) return json(checkoutStatus('order-state', state, false));
      throw new Error(`Unexpected checkout API request: ${url}`);
    });
    await clickButton(host!, '支払い状況を確認');
    expect(host?.textContent).toContain(expected);
    expect(host?.textContent).not.toContain('支払いを続ける');
    expect(host?.textContent.includes('スタッフにお声がけください')).toBe(support);
  });

  it('ignores a delayed payment status after the guest selects another Stripe order', async () => {
    const oldStatus = deferred<Response>();
    await mountCheckoutOrders([stripeOrder('order-old'), stripeOrder('order-new')], async (url) => {
      if (url.endsWith('/orders/order-old/checkout')) return oldStatus.promise;
      if (url.endsWith('/orders/order-new/checkout')) return json(checkoutStatus('order-new', 'PAID', false));
      throw new Error(`Unexpected checkout API request: ${url}`);
    });
    const cards = host!.querySelectorAll<HTMLElement>('[data-order-card]');
    await act(async () => { getButton(cards.item(0)!, '支払い状況を確認').click(); await new Promise((resolve) => setTimeout(resolve, 0)); });
    await act(async () => { getButton(cards.item(1)!, '支払い状況を確認').click(); await new Promise((resolve) => setTimeout(resolve, 0)); });
    expect(host?.textContent).toContain('お支払い済み');
    await act(async () => { oldStatus.resolve(json(checkoutStatus('order-old', 'OPEN', true))); await new Promise((resolve) => setTimeout(resolve, 0)); });
    expect(host?.textContent).toContain('お支払い済み');
    expect(host?.textContent).not.toContain('支払いを続ける');
  });

  it('does not navigate or change the newer payment view when an old resume request finishes late', async () => {
    const resumeResponse = deferred<Response>();
    const assign = vi.spyOn(checkoutNavigation, 'assign').mockImplementation(() => undefined);
    await mountCheckoutOrders([stripeOrder('order-resume-old'), stripeOrder('order-resume-new')], async (url, init) => {
      if (url.endsWith('/orders/order-resume-old/checkout') && init.method !== 'POST') return json(checkoutStatus('order-resume-old', 'OPEN', true));
      if (url.endsWith('/orders/order-resume-new/checkout') && init.method !== 'POST') return json(checkoutStatus('order-resume-new', 'PAID', false));
      if (url.endsWith('/orders/order-resume-old/checkout/resume')) return resumeResponse.promise;
      throw new Error(`Unexpected checkout API request: ${url}`);
    });

    const cards = host!.querySelectorAll<HTMLElement>('[data-order-card]');
    await act(async () => { getButton(cards.item(0)!, '支払い状況を確認').click(); await new Promise((resolve) => setTimeout(resolve, 0)); });
    await clickButton(host!, '支払いを続ける');
    await act(async () => { getButton(host!.querySelectorAll<HTMLElement>('[data-order-card]').item(1)!, '支払い状況を確認').click(); await new Promise((resolve) => setTimeout(resolve, 0)); });
    resumeResponse.resolve(json({ url: 'https://checkout.example.test/stale-session' }));
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });

    expect(assign).not.toHaveBeenCalled();
    expect(host?.textContent).not.toContain('支払いを続ける');
    expect(host?.textContent).not.toContain('stale-session');
  });

  it('skips the 20-second poll while the selected-order status request is still pending', async () => {
    const pendingStatus = deferred<Response>();
    let reads = 0;
    await mountCheckoutOrders([stripeOrder('order-slow')], async (url) => {
      if (url.endsWith('/orders/order-slow/checkout')) { reads++; return pendingStatus.promise; }
      throw new Error(`Unexpected checkout API request: ${url}`);
    });
    vi.useFakeTimers();
    await act(async () => { getButton(host!, '支払い状況を確認').click(); await Promise.resolve(); });
    await act(async () => { await vi.advanceTimersByTimeAsync(20_000); });
    expect(reads).toBe(1);
    await act(async () => { pendingStatus.resolve(json(checkoutStatus('order-slow', 'UNKNOWN', false))); await Promise.resolve(); });
  });

  it('uses a 20-second visible cadence and refreshes immediately when the page becomes visible again', async () => {
    let reads = 0;
    await mountCheckoutOrders([stripeOrder('order-cadence')], async (url) => {
      if (url.endsWith('/orders/order-cadence/checkout')) { reads++; return json(checkoutStatus('order-cadence', 'OPEN', true)); }
      throw new Error(`Unexpected checkout API request: ${url}`);
    });
    vi.useFakeTimers();
    await act(async () => { getButton(host!, '支払い状況を確認').click(); await Promise.resolve(); });
    expect(reads).toBe(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(19_999); });
    expect(reads).toBe(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    expect(reads).toBe(2);

    setVisibilityState('hidden');
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')); });
    await act(async () => { await vi.advanceTimersByTimeAsync(40_000); });
    expect(reads).toBe(2);
    setVisibilityState('visible');
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')); await Promise.resolve(); });
    expect(reads).toBe(3);
  });

  it.each(['PAID', 'FAILED', 'EXPIRED', 'REVIEW_REQUIRED'] as const)('stops guest polling after terminal checkout state %s', async (terminalState) => {
    let reads = 0;
    await mountCheckoutOrders([stripeOrder('order-terminal')], async (url) => {
      if (url.endsWith('/orders/order-terminal/checkout')) {
        reads++;
        return json(checkoutStatus('order-terminal', reads === 1 ? 'OPEN' : terminalState, false));
      }
      throw new Error(`Unexpected checkout API request: ${url}`);
    });
    vi.useFakeTimers();
    await act(async () => { getButton(host!, '支払い状況を確認').click(); await Promise.resolve(); });
    expect(reads).toBe(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(20_000); });
    expect(reads).toBe(2);
    await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
    expect(reads).toBe(2);
  });

  it('refreshes once when returning visible after a terminal result, without restarting periodic polling', async () => {
    let reads = 0;
    await mountCheckoutOrders([stripeOrder('order-terminal-resume')], async (url) => {
      if (url.endsWith('/orders/order-terminal-resume/checkout')) {
        reads++;
        return json(checkoutStatus('order-terminal-resume', 'PAID', false));
      }
      throw new Error(`Unexpected checkout API request: ${url}`);
    });
    vi.useFakeTimers();
    await act(async () => { getButton(host!, '支払い状況を確認').click(); await Promise.resolve(); });
    expect(reads).toBe(1);

    setVisibilityState('hidden');
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')); });
    await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
    expect(reads).toBe(1);

    setVisibilityState('visible');
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')); });
    expect(reads).toBe(2);
    await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
    expect(reads).toBe(2);
  });

  it('restarts polling when a fresh visibility read moves a cached FAILED state back to a nonterminal state', async () => {
    let reads = 0;
    await mountCheckoutOrders([stripeOrder('order-reconciled')], async (url) => {
      if (url.endsWith('/orders/order-reconciled/checkout')) {
        reads++;
        return json(checkoutStatus('order-reconciled', reads === 1 ? 'FAILED' : 'OPEN', false));
      }
      throw new Error(`Unexpected checkout API request: ${url}`);
    });
    vi.useFakeTimers();
    await act(async () => { getButton(host!, '支払い状況を確認').click(); await Promise.resolve(); });
    expect(reads).toBe(1);
    setVisibilityState('hidden');
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')); });
    setVisibilityState('visible');
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')); });
    expect(reads).toBe(2);
    await act(async () => { await vi.advanceTimersByTimeAsync(20_000); });
    expect(reads).toBe(3);
  });

  it('refreshes the already-selected order and resumes polling when staff reconciliation reopens a FAILED attempt', async () => {
    let reads = 0;
    await mountCheckoutOrders([stripeOrder('order-same-selection')], async (url) => {
      if (url.endsWith('/orders/order-same-selection/checkout')) {
        reads++;
        return json(checkoutStatus('order-same-selection', reads === 1 ? 'FAILED' : 'OPEN', false));
      }
      throw new Error(`Unexpected checkout API request: ${url}`);
    });
    vi.useFakeTimers();
    const refreshButton = getButton(host!, '支払い状況を確認');
    await act(async () => { refreshButton.click(); await Promise.resolve(); });
    expect(reads).toBe(1);
    expect(host?.textContent).toContain('お支払いを完了できませんでした');

    await act(async () => { refreshButton.click(); await Promise.resolve(); });
    expect(reads).toBe(2);
    expect(host?.textContent).toContain('お支払い手続きができます');
    await act(async () => { await vi.advanceTimersByTimeAsync(20_000); });
    expect(reads).toBe(3);
  });

  it('invalidates a pending resume when the same order is selected for a fresh status read', async () => {
    const resumeResponse = deferred<Response>();
    const assign = vi.spyOn(checkoutNavigation, 'assign').mockImplementation(() => undefined);
    let reads = 0;
    await mountCheckoutOrders([stripeOrder('order-resume-refresh')], async (url) => {
      if (url.endsWith('/orders/order-resume-refresh/checkout')) {
        reads++;
        return json(checkoutStatus('order-resume-refresh', 'OPEN', true));
      }
      if (url.endsWith('/orders/order-resume-refresh/checkout/resume')) return resumeResponse.promise;
      throw new Error(`Unexpected checkout API request: ${url}`);
    });
    await clickButton(host!, '支払い状況を確認');
    await clickButton(host!, '支払いを続ける');

    await clickButton(host!, '支払い状況を確認');
    expect(reads).toBe(2);
    expect(host?.textContent).toContain('支払いを続ける');

    resumeResponse.resolve(json({ url: 'https://checkout.example.test/stale-session' }));
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
    expect(assign).not.toHaveBeenCalled();
    expect(host?.textContent).not.toContain('stale-session');
  });

  it('schedules a safe retry after a transient immediate status read failure', async () => {
    let reads = 0;
    await mountCheckoutOrders([stripeOrder('order-retry')], async (url) => {
      if (url.endsWith('/orders/order-retry/checkout')) {
        reads++;
        if (reads === 1) throw new Error('temporary network failure');
        return json(checkoutStatus('order-retry', 'UNKNOWN', false));
      }
      throw new Error(`Unexpected checkout API request: ${url}`);
    });
    vi.useFakeTimers();
    await act(async () => { getButton(host!, '支払い状況を確認').click(); await Promise.resolve(); });
    expect(reads).toBe(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(20_000); });
    expect(reads).toBe(2);
  });

  it('switches single-select options, clears optional choices, protects required groups, and replays a 503 with the same request', async () => {
    window.history.replaceState({}, '', '/?table=table-1');
    const optionGroups = [
      { key: 'sauce', name: 'ソース', min_select: 0, max_select: 1, position: 1, options: [
        { key: 'a', name: 'A', price_delta_minor: '0', position: 1, availability: 'AVAILABLE' },
        { key: 'b', name: 'B', price_delta_minor: '0', position: 2, availability: 'AVAILABLE' },
      ] },
      { key: 'size', name: 'サイズ', min_select: 1, max_select: 1, position: 2, options: [
        { key: 'small', name: '小', price_delta_minor: '0', position: 1, availability: 'AVAILABLE' },
        { key: 'large', name: '大', price_delta_minor: '0', position: 2, availability: 'AVAILABLE' },
      ] },
    ];
    const toCartOption = (groupKey: string, optionKey: string) => {
      const group = optionGroups.find((candidate) => candidate.key === groupKey)!;
      const option = group.options.find((candidate) => candidate.key === optionKey)!;
      return { group_key: group.key, group_name: group.name, option_key: option.key, option_name: option.name, available: true };
    };
    const menu = { currency: 'JPY', price_mode: 'TAX_INCLUSIVE', categories: [{ key: 'food', name: 'お食事', position: 1, products: [{
      key: 'meal', name: '定食', effective_price_minor: '1000', position: 1, availability: 'AVAILABLE', selectable: true, option_groups: optionGroups,
    }] }] };
    let currentCart: Cart = {
      session_id: 'session-1', generation: 1, version: 4,
      lines: [{ line_id: 'line-1', product_key: 'meal', product_name: '定食', product_available: true, quantity: 1, options: [toCartOption('sauce', 'a'), toCartOption('size', 'small')] }],
    };
    const mutationCalls: Array<{ body: string; key: string | null }> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith('/v1/guest/tables/table-1')) return json(table);
      if (url.endsWith('/v1/guest/tables/table-1/join')) return json(grant);
      if (url.endsWith('/v1/guest/tables/table-1/menu?amounts=string')) return json(menu);
      if (url.endsWith('/v1/guest/sessions/session-1/cart') && init?.method !== 'PUT') return json(currentCart);
      if (url.endsWith('/v1/guest/sessions/session-1/orders')) return json({ orders: [] });
      if (url.endsWith('/v1/guest/sessions/session-1/cart/lines/line-1') && init?.method === 'PUT') {
        const body = String(init.body);
        mutationCalls.push({ body, key: new Headers(init.headers).get('Idempotency-Key') });
        if (mutationCalls.length === 1) return json({ error: 'temporary_failure' }, 503);
        const request = JSON.parse(body) as { options: Array<{ group_key: string; option_key: string }> };
        currentCart = {
          ...currentCart,
          version: currentCart.version + 1,
          lines: [{ ...currentCart.lines[0]!, options: request.options.map(({ group_key, option_key }) => toCartOption(group_key, option_key)) }],
        };
        return json(currentCart);
      }
      throw new Error(`Unexpected API request: ${url}`);
    });
    vi.stubGlobal('fetch', fetchMock);
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
    host = document.createElement('div');
    document.body.append(host);
    root = createRoot(host);

    await act(async () => {
      root?.render(<App />);
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    const productRow = host.querySelector<HTMLElement>('[data-product-key="meal"]')!;
    const addButton = within(productRow).getByRole('button', { name: 'カートに追加' });
    expect((addButton as HTMLButtonElement).disabled).toBe(false);
    await clickButton(host, 'カートに追加');
    const optionDialog = within(document.body).getByRole('dialog', { name: '定食' });
    const productSauce = getOptionGroup(optionDialog, 'ソース');
    const productSize = getOptionGroup(optionDialog, 'サイズ');
    const cartLinePosts = () => fetchMock.mock.calls.filter(([url, init]) => String(url).endsWith('/cart/lines') && init?.method === 'POST').length;
    const cartLinePostsBeforeRequiredAttempt = cartLinePosts();
    await clickButton(optionDialog, 'カートに追加');
    expect(cartLinePosts()).toBe(cartLinePostsBeforeRequiredAttempt);
    expect(productSize.getAttribute('aria-invalid')).toBe('true');
    expect(within(optionDialog).getByRole('alert').textContent).toContain('必須オプションを選択してください');
    expect(document.activeElement).toBe(within(productSize).getByRole('radio', { name: /小/ }));
    await clickOption(productSauce, 'A');
    expect(findOption(productSauce, 'B').disabled).toBe(false);
    await clickOption(productSauce, 'B');
    expect(findOption(productSauce, 'A').checked).toBe(false);
    expect(findOption(productSauce, 'B').checked).toBe(true);
    await clickButton(productSauce, '選択しない');
    expect(findOption(productSauce, 'A').checked).toBe(false);
    expect(findOption(productSauce, 'B').checked).toBe(false);
    await clickOption(productSize, '小');
    expect(productSize.getAttribute('aria-invalid')).toBeNull();
    await clickButton(optionDialog, '閉じる');
    await openCartSheet(host);

    const cartSauce = getOptionGroup(document.body, 'ソース');
    await clickOption(cartSauce, 'B');
    expect(document.body.textContent).toContain('前回の操作を照合して再試行');
    await act(async () => {
      within(document.body).getByRole('button', { name: /前回の操作を照合して再試行/ }).click();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(mutationCalls).toHaveLength(2);
    expect(mutationCalls[0]?.key).toBeTruthy();
    expect(mutationCalls[1]?.key).toBe(mutationCalls[0]?.key);
    expect(mutationCalls[1]?.body).toBe(mutationCalls[0]?.body);
    expect(JSON.parse(mutationCalls[1]!.body).options).toEqual([{ group_key: 'size', option_key: 'small' }, { group_key: 'sauce', option_key: 'b' }]);
    expect(currentCart.lines[0]?.options.map((option) => option.option_key)).toEqual(['small', 'b']);

    const updatedCartSauce = getOptionGroup(document.body, 'ソース');
    await clickOption(updatedCartSauce, 'A');
    const selectedAGroup = getOptionGroup(document.body, 'ソース');
    expect(findOption(selectedAGroup, 'A').checked).toBe(true);
    expect(JSON.parse(mutationCalls[2]!.body).options).toEqual([{ group_key: 'size', option_key: 'small' }, { group_key: 'sauce', option_key: 'a' }]);
    await clickButton(selectedAGroup, '選択しない');
    expect(currentCart.lines[0]?.options.map((option) => option.option_key)).toEqual(['small']);
    expect(mutationCalls).toHaveLength(4);
    const clearPayload = JSON.parse(mutationCalls[3]!.body) as { options: Array<{ group_key: string; option_key: string }> };
    expect(clearPayload.options).toEqual([{ group_key: 'size', option_key: 'small' }]);
  });
});

async function mountGuestStatus(
  mode: 'STAFF_OPEN' | 'SESSION_PIN' | 'DIRECT_SCAN',
  order: unknown,
  operation: unknown,
  operationsRoute?: () => Promise<Response>,
) {
  window.history.replaceState({}, '', '/?table=table-1');
  const modeTable = { ...table, mode };
  const modeGrant = { ...grant, session: { ...grant.session, mode } };
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.endsWith('/v1/guest/tables/table-1')) return json(modeTable);
    if (url.endsWith('/v1/guest/tables/table-1/join')) return json(modeGrant);
    if (url.endsWith('/v1/guest/sessions/session-1/join')) return json(modeGrant);
    if (url.endsWith('/v1/guest/tables/table-1/menu?amounts=string')) return json(guestMenu);
    if (url.endsWith('/v1/guest/sessions/session-1/cart')) return json(cart);
    if (url.endsWith('/v1/guest/sessions/session-1/orders')) return json({ orders: [order] });
    if (url.endsWith('/v1/guest/sessions/session-1/operations')) return operationsRoute ? operationsRoute() : json({ orders: [operation], cash_enabled: true });
    throw new Error(`Unexpected API request: ${url}`);
  });
  vi.stubGlobal('fetch', fetchMock);
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
  host = document.createElement('div');
  document.body.append(host);
  root = createRoot(host);
  await act(async () => { root?.render(<App />); await new Promise((resolve) => setTimeout(resolve, 0)); });
  if (mode !== 'DIRECT_SCAN') {
    if (mode === 'SESSION_PIN') await setTextField(host, 'テーブル PIN', '1234');
    await clickButton(host, '参加する');
  }
}

async function mountCheckoutOrders(
  orders: ReturnType<typeof stripeOrder>[],
  checkoutRoute: (url: string, init: RequestInit) => Promise<Response>,
) {
  window.history.replaceState({}, '', '/?table=table-1');
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    if (url === '/v1/guest/tables/table-1') return json(table);
    if (url === '/v1/guest/tables/table-1/join') return json(grant);
    if (url === '/v1/guest/tables/table-1/menu?amounts=string') return json(guestMenu);
    if (url === '/v1/guest/sessions/session-1/cart') return json({ session_id: 'session-1', generation: 1, version: 0, lines: [] });
    if (url === '/v1/guest/sessions/session-1/orders') return json({ orders });
    if (url === '/v1/guest/sessions/session-1/operations') return json({ orders: orders.map(standardOperation), cash_enabled: false });
    if (url.includes('/orders/') && url.includes('/checkout')) return checkoutRoute(url, init);
    throw new Error(`Unexpected API request: ${url}`);
  });
  vi.stubGlobal('fetch', fetchMock);
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
  host = document.createElement('div'); document.body.append(host); root = createRoot(host);
  await act(async () => { root?.render(<App />); await new Promise((resolve) => setTimeout(resolve, 0)); });
  await clickButton(host!, '注文履歴');
  return fetchMock;
}

function stripeOrder(orderID: string) {
  return {
    order_id: orderID, session_id: 'session-1', generation: 1, payment_method: 'STRIPE',
    requires_staff_acceptance: false, total_gross_jpy: '1100', created_at: '2026-10-18T00:00:00Z',
  };
}

function standardOperation(order: ReturnType<typeof stripeOrder>) {
  return { id: order.order_id, display_state: 'STANDARD', payment_status: 'PENDING', fulfillment_status: 'WAITING', requires_staff_acceptance: false, staff_accepted: true };
}

function checkoutStatus(orderID: string, state: string, resumeAvailable: boolean) {
  return {
    order_id: orderID, state, last_reconciled_at: null, payment_review_required: state === 'REVIEW_REQUIRED',
    resume_available: resumeAvailable,
  };
}

async function setTextField(container: HTMLElement, label: string, value: string) {
  const input = within(container).getByRole('textbox', { name: new RegExp(escapeRegex(label)) }) as HTMLInputElement;
  if (!input) throw new Error(`Missing field: ${label}`);
  const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')?.set;
  await act(async () => { setter?.call(input, value); input.dispatchEvent(new Event('input', { bubbles: true })); input.dispatchEvent(new Event('change', { bubbles: true })); });
}

async function clickOption(fieldset: HTMLElement, name: string) {
  const input = findOption(fieldset, name);
  await act(async () => { input.click(); await new Promise((resolve) => setTimeout(resolve, 0)); });
}

async function clickButton(container: HTMLElement, label: string) {
  const button = getButton(container, label);
  await act(async () => { button.click(); await new Promise((resolve) => setTimeout(resolve, 0)); });
}

async function openCartSheet(container: HTMLElement) {
  const button = within(container).getByRole('button', { name: /カートを開く|開啟購物車/ });
  await act(async () => { button.click(); for (let i = 0; i < 10; i++) await Promise.resolve(); });
}

function getButton(container: HTMLElement, label: string): HTMLButtonElement {
  const matcher = new RegExp(escapeRegex(label));
  return (within(container).queryByRole('button', { name: matcher }) ?? within(document.body).getByRole('button', { name: matcher })) as HTMLButtonElement;
}

function findOption(fieldset: HTMLElement, name: string): HTMLInputElement {
  const matcher = new RegExp(escapeRegex(name));
  return (within(fieldset).queryByRole('radio', { name: matcher }) ?? within(fieldset).getByRole('checkbox', { name: matcher })) as HTMLInputElement;
}

function getOptionGroup(container: HTMLElement, name: string): HTMLElement {
  return within(container).getByRole('group', { name: new RegExp(escapeRegex(name)) }) as HTMLElement;
}

function escapeRegex(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
}

function setVisibilityState(state: DocumentVisibilityState): void {
  Object.defineProperty(document, 'visibilityState', { configurable: true, value: state });
}
