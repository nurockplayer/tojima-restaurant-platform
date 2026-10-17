// @vitest-environment jsdom
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
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
  sessionStorage.clear();
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
      if (url.endsWith('/v1/guest/sessions/session-1/cart/commit') && init?.method === 'POST') {
        commitCalls += 1;
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
    expect(host.textContent).toContain('¥200');
    expect(host.textContent).toContain('定食');
    expect(host.textContent).not.toContain('この内容で注文を確定');
    expect(host.textContent).toContain('金額を確認する');
  });
});

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
}
