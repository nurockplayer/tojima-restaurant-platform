// @vitest-environment jsdom
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Cart } from '../api/types';
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

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
}
