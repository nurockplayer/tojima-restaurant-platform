// @vitest-environment jsdom
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Cart } from '../api/types';
import { checkoutNavigation } from '../api/checkout';
import { App } from './App';

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
      if (url.endsWith('/v1/guest/sessions/session-1/operations')) return json({ orders: [{ id: 'order-1', payment_status: 'UNPAID', fulfillment_status: 'WAITING', requires_staff_acceptance: true, staff_accepted: false, cash_mode: 'PREPAID' }], cash_enabled: true });
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

    expect(host.textContent).toContain('東島');
    expect(host.textContent).toContain('共有カート');
    expect(host.textContent).toContain('お食事');
    expect(host.textContent).toContain('Previously added item');
    expect(host.textContent).toContain('テーブルの QR から参加');
    expect(host.textContent).not.toContain('DIRECT_SCAN');
    const requested = fetchMock.mock.calls.map(([url]) => String(url));
    expect(requested).toContain('/v1/guest/tables/table-1');
    expect(requested).toContain('/v1/guest/tables/table-1/join');
    expect(requested).toContain('/v1/guest/tables/table-1/menu?amounts=string');
    expect(requested).toContain('/v1/guest/sessions/session-1/cart');
    expect(requested).toContain('/v1/guest/sessions/session-1/orders');
    expect(sessionStorage.getItem('tojima.table-join.session-1')).toBe('memory-only-grant');

    await act(async () => {
      [...(host?.querySelectorAll('button') ?? [])].find((button) => button.textContent?.includes('注文履歴'))?.click();
    });
    expect(host.textContent).toContain('¥1,100 × 1');
    expect(host.textContent).toContain('大盛り +¥110 × 1');
    expect(host.textContent).toContain('¥1,210');
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
    const guestOperation = { id: 'order-1', payment_status: 'UNPAID', fulfillment_status: scenario.fulfillment, requires_staff_acceptance: scenario.requires, staff_accepted: false, ...(scenario.cashMode ? { cash_mode: scenario.cashMode } : {}) };
    await mountGuestStatus(mode, order, guestOperation);
    await act(async () => {
      [...(host?.querySelectorAll('button') ?? [])].find((button) => button.textContent?.includes('注文履歴'))?.click();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(host?.querySelector('.guest-order-status')?.textContent, host?.textContent).toBe(scenario.expected);
    if (!scenario.requires) expect(host?.querySelector('.guest-order-status')?.textContent).not.toContain('店員の受付待ち');
    if (scenario.cashMode === 'PREPAID') {
      await clickButton(host!, '繁中');
      expect(host?.querySelector('.guest-order-status')?.textContent).toBe('等待開始製作 · 尚未付款');
    }
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

    await act(async () => {
      host?.querySelector<HTMLButtonElement>('button[aria-label="共有内容を更新"]')?.click();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(host.textContent).toContain('¥150');
    expect(host.textContent).toContain('定食');
    expect(menuReads).toBe(2);

    await act(async () => {
      [...(host?.querySelectorAll('button') ?? [])].find((button) => button.textContent?.includes('金額を確認する'))?.click();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(host.textContent).toContain('この内容で注文を確定');
    expect(host.textContent).toContain('クレジットカード');

    await act(async () => {
      [...(host?.querySelectorAll('button') ?? [])].find((button) => button.textContent?.includes('この内容で注文を確定'))?.click();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(menuReads).toBe(3);
    expect(previewCalls).toBe(1);
    expect(commitCalls).toBe(1);
    expect(paymentFlow).toEqual(['checkout-context', 'commit']);
    expect(host.textContent).toContain('¥200');
    expect(host.textContent).toContain('定食');
    expect(host.textContent).not.toContain('この内容で注文を確定');
    expect(host.textContent).toContain('金額を確認する');
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
    await clickButton(host!, '金額を確認する');
    await clickButton(host!, 'この内容で注文を確定');
    expect(calls).toEqual(['context']);
    expect(commits).toBe(0);
    expect(host?.textContent).toContain('注文は送信されていません');
    expect(host?.textContent).not.toContain('前回の操作を照合して再試行');
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
      if (url.endsWith('/sessions/session-1/operations')) return json({ orders: [], cash_enabled: false });
      if (url.endsWith('/checkout-context')) { flow.push('bootstrap'); return new Response(null, { status: 204 }); }
      if (url.endsWith('/cart/commit')) { flow.push('commit'); return json({ outcome: 'COMMITTED', order, cart: emptyCart }); }
      if (url.endsWith('/orders/order-created/checkout')) { flow.push('status'); return json(checkoutStatus('order-created', 'CREATE_PENDING', false)); }
      throw new Error(`Unexpected API request: ${url}`);
    });
    vi.stubGlobal('fetch', fetchMock);
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
    host = document.createElement('div'); document.body.append(host); root = createRoot(host);
    await act(async () => { root?.render(<App />); await new Promise((resolve) => setTimeout(resolve, 0)); });
    await clickButton(host!, '金額を確認する');
    await clickButton(host!, 'この内容で注文を確定');
    expect(flow.slice(0, 3)).toEqual(['bootstrap', 'commit', 'status']);
    expect(host?.textContent).toContain('注文を確認しています');
    expect(host?.textContent).not.toContain('支払いを続ける');
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
    const cards = host!.querySelectorAll<HTMLElement>('.order-card');
    await act(async () => { cards.item(0)!.querySelector<HTMLButtonElement>('.checkout-status-panel button')!.click(); await new Promise((resolve) => setTimeout(resolve, 0)); });
    await act(async () => { host!.querySelectorAll<HTMLElement>('.order-card').item(1)!.querySelector<HTMLButtonElement>('.checkout-status-panel button')!.click(); await new Promise((resolve) => setTimeout(resolve, 0)); });
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

    const cards = host!.querySelectorAll<HTMLElement>('.order-card');
    await act(async () => { cards.item(0)!.querySelector<HTMLButtonElement>('.checkout-status-panel button')!.click(); await new Promise((resolve) => setTimeout(resolve, 0)); });
    await clickButton(host!, '支払いを続ける');
    await act(async () => { host!.querySelectorAll<HTMLElement>('.order-card').item(1)!.querySelector<HTMLButtonElement>('.checkout-status-panel button')!.click(); await new Promise((resolve) => setTimeout(resolve, 0)); });
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
    await act(async () => { host!.querySelector<HTMLButtonElement>('.checkout-status-panel button')!.click(); await Promise.resolve(); });
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
    await act(async () => { host!.querySelector<HTMLButtonElement>('.checkout-status-panel button')!.click(); await Promise.resolve(); });
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
    await act(async () => { host!.querySelector<HTMLButtonElement>('.checkout-status-panel button')!.click(); await Promise.resolve(); });
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
    await act(async () => { host!.querySelector<HTMLButtonElement>('.checkout-status-panel button')!.click(); await Promise.resolve(); });
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
    await act(async () => { host!.querySelector<HTMLButtonElement>('.checkout-status-panel button')!.click(); await Promise.resolve(); });
    expect(reads).toBe(1);
    setVisibilityState('hidden');
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')); });
    setVisibilityState('visible');
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')); });
    expect(reads).toBe(2);
    await act(async () => { await vi.advanceTimersByTimeAsync(20_000); });
    expect(reads).toBe(3);
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
    await act(async () => { host!.querySelector<HTMLButtonElement>('.checkout-status-panel button')!.click(); await Promise.resolve(); });
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

    const productCard = host.querySelector<HTMLElement>('.product-card')!;
    const addButton = productCard.querySelector<HTMLButtonElement>('button.small')!;
    const productSauce = [...productCard.querySelectorAll('fieldset.option-group')].find((fieldset) => fieldset.querySelector('legend')?.textContent?.includes('ソース'))!;
    const productSize = [...productCard.querySelectorAll('fieldset.option-group')].find((fieldset) => fieldset.querySelector('legend')?.textContent?.includes('サイズ'))!;
    expect(addButton.disabled).toBe(true);
    await clickOption(productSauce, 'A');
    expect(findOption(productSauce, 'B').disabled).toBe(false);
    await clickOption(productSauce, 'B');
    expect(findOption(productSauce, 'A').checked).toBe(false);
    expect(findOption(productSauce, 'B').checked).toBe(true);
    await clickButton(productSauce, '選択しない');
    expect(findOption(productSauce, 'A').checked).toBe(false);
    expect(findOption(productSauce, 'B').checked).toBe(false);
    expect(addButton.disabled).toBe(true);
    await clickOption(productSize, '小');
    expect(addButton.disabled).toBe(false);

    const cartSauce = [...host.querySelectorAll('fieldset.compact-options')].find((fieldset) => fieldset.querySelector('legend')?.textContent?.includes('ソース'))!;
    await clickOption(cartSauce, 'B');
    expect(host.textContent).toContain('前回の操作を照合して再試行');
    await act(async () => {
      host?.querySelector<HTMLButtonElement>('button.retry')?.click();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(mutationCalls).toHaveLength(2);
    expect(mutationCalls[0]?.key).toBeTruthy();
    expect(mutationCalls[1]?.key).toBe(mutationCalls[0]?.key);
    expect(mutationCalls[1]?.body).toBe(mutationCalls[0]?.body);
    expect(JSON.parse(mutationCalls[1]!.body).options).toEqual([{ group_key: 'size', option_key: 'small' }, { group_key: 'sauce', option_key: 'b' }]);
    expect(currentCart.lines[0]?.options.map((option) => option.option_key)).toEqual(['small', 'b']);

    const updatedCartSauce = [...host.querySelectorAll('fieldset.compact-options')].find((fieldset) => fieldset.querySelector('legend')?.textContent?.includes('ソース'))!;
    await clickOption(updatedCartSauce, 'A');
    const selectedAGroup = [...host.querySelectorAll('fieldset.compact-options')].find((fieldset) => fieldset.querySelector('legend')?.textContent?.includes('ソース'))!;
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
    if (url.endsWith('/v1/guest/sessions/session-1/operations')) return json({ orders: [operation], cash_enabled: true });
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
  orders: unknown[],
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
    if (url === '/v1/guest/sessions/session-1/operations') return json({ orders: [], cash_enabled: false });
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

function checkoutStatus(orderID: string, state: string, resumeAvailable: boolean) {
  return {
    order_id: orderID, state, last_reconciled_at: null, payment_review_required: state === 'REVIEW_REQUIRED',
    resume_available: resumeAvailable,
  };
}

async function setTextField(container: Element, label: string, value: string) {
  const field = [...container.querySelectorAll('label.field')].find((item) => item.textContent?.includes(label));
  const input = field?.querySelector('input');
  if (!input) throw new Error(`Missing field: ${label}`);
  const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')?.set;
  await act(async () => { setter?.call(input, value); input.dispatchEvent(new Event('input', { bubbles: true })); input.dispatchEvent(new Event('change', { bubbles: true })); });
}

async function clickOption(fieldset: Element, name: string) {
  const input = findOption(fieldset, name);
  await act(async () => { input.click(); await new Promise((resolve) => setTimeout(resolve, 0)); });
}

async function clickButton(container: Element, label: string) {
  const button = [...container.querySelectorAll('button')].find((candidate) => candidate.textContent?.includes(label));
  if (!button) throw new Error(`Missing button: ${label}`);
  await act(async () => { button.click(); await new Promise((resolve) => setTimeout(resolve, 0)); });
}

function findOption(fieldset: Element, name: string): HTMLInputElement {
  const label = [...fieldset.querySelectorAll('label')].find((candidate) => candidate.textContent?.includes(name));
  const input = label?.querySelector('input');
  if (!input) throw new Error(`Missing option: ${name}`);
  return input;
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
