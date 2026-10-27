// @vitest-environment jsdom
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { within } from '@testing-library/react';
import { expect, it, vi } from 'vitest';
import { App } from './App';
import { OrderStatusView } from './OrderRead';

it('P84 B keeps two mounted carts at 2.5s independently of 30s ORDER_READ and stops all periodic reads in background', async () => {
  vi.useFakeTimers();
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
  const visible = (value: 'visible' | 'hidden') => Object.defineProperty(document, 'visibilityState', { configurable: true, value });
  visible('visible');
  window.history.replaceState({}, '', '/?table=p84-cadence');
  sessionStorage.clear();
  const json = (value: unknown) => new Response(JSON.stringify(value), { headers: { 'content-type': 'application/json' } });
  let quantity = 1;
  let version = 1;
  let cartReads = 0;
  let statusReads = 0;
  let menuReads = 0;
  const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    if (path === '/v1/guest/tables/p84-cadence') return json({ mode: 'DIRECT_SCAN', open: true, current_session_id: 'p84-session' });
    if (path === '/v1/guest/tables/p84-cadence/join') return json({ table_join_token: 'fixture-join', expires_at: '2099-01-01T00:00:00Z', session: { id: 'p84-session', mode: 'DIRECT_SCAN' } });
    if (path.endsWith('/menu?amounts=string')) {
      menuReads++;
      return json({ currency: 'JPY', price_mode: 'TAX_INCLUSIVE', categories: [{ key: 'c', name: 'お茶', position: 0, products: [{ key: 'tea', name: 'ほうじ茶', description: '', effective_price_minor: '330', position: 0, availability: 'AVAILABLE', selectable: true, option_groups: [] }] }] });
    }
    if (path === '/v1/guest/sessions/p84-session/cart/lines/line') {
      const body = JSON.parse(String(init?.body)) as { quantity: number };
      quantity = body.quantity; version++;
      return json(cart());
    }
    if (path === '/v1/guest/sessions/p84-session/cart') { cartReads++; return json(cart()); }
    if (path.endsWith('/orders')) return json({ orders: [] });
    if (path.endsWith('/operations')) return json({ orders: [], cash_enabled: true, card_enabled: true });
    if (path === `/v1/guest/orders/${'a'.repeat(32)}/status`) {
      statusReads++;
      return json({ display_state: 'STANDARD', version: 1, payment_status: 'UNPAID', fulfillment_status: 'WAITING', total_gross_jpy: '660', effective_total_gross_jpy: '660', effective_lines: [], adjusted: false, refunds: [], refunded_total_jpy: '0', currency: 'JPY', is_terminal: false });
    }
    throw new Error(`Unexpected P84 B request: ${path}`);
  });
  function cart() {
    return { session_id: 'p84-session', generation: 1, version, lines: [{ line_id: 'line', product_key: 'tea', product_name: 'ほうじ茶', product_available: true, quantity, options: [] }] };
  }
  vi.stubGlobal('fetch', fetcher);
  const hosts = Array.from({ length: 3 }, () => { const host = document.createElement('div'); document.body.append(host); return host; });
  const roots = hosts.map((host) => createRoot(host));
  const advance = async (ms: number) => { await act(async () => { await vi.advanceTimersByTimeAsync(ms); }); };
  const visibility = async (value: 'visible' | 'hidden') => { await act(async () => { visible(value); document.dispatchEvent(new Event('visibilitychange')); }); await advance(1); };
  try {
    await act(async () => {
      roots[0]!.render(<App />); roots[1]!.render(<App />);
      roots[2]!.render(<OrderStatusView grant={{ order_id: 'a'.repeat(32), order_read_token: 'A'.repeat(43), expires_at: '2099-01-01T00:00:00Z' }} locale="ja" />);
      for (let i = 0; i < 40; i++) await Promise.resolve();
    });
    await advance(1);
    const initialCartReads = cartReads;
    expect(initialCartReads).toBeGreaterThanOrEqual(2); expect(statusReads).toBe(1);
    // A real mounted guest mutation updates only its own UI immediately. The
    // second mounted guest observes the shared mocked HTTP state on its poll.
    await openCartSheet(hosts[0]!);
    const plus = within(document.body).getByRole('button', { name: 'ほうじ茶 +' });
    expect(plus).toBeTruthy();
    await act(async () => { plus.click(); for (let i = 0; i < 30; i++) await Promise.resolve(); });
    expect(document.body.querySelector('[data-quantity]')?.textContent).toBe('2');
    await closeCart();
    await openCartSheet(hosts[1]!);
    expect(document.body.querySelector('[data-quantity]')?.textContent).toBe('1');
    await closeCart();
    const afterMutation = cartReads;
    await advance(2498); expect(cartReads).toBe(afterMutation); expect(statusReads).toBe(1);
    await advance(1); expect(cartReads).toBe(afterMutation + 2);
    await openCartSheet(hosts[1]!);
    expect(document.body.querySelector('[data-quantity]')?.textContent).toBe('2');
    await closeCart();
    await advance(27_499); expect(statusReads).toBe(1);
    await advance(2); expect(statusReads).toBe(2);
    expect(cartReads).toBeGreaterThan(afterMutation + 20);
    await visibility('hidden');
    const background = { cartReads, statusReads, menuReads, requests: fetcher.mock.calls.length };
    await advance(90_000);
    expect({ cartReads, statusReads, menuReads, requests: fetcher.mock.calls.length }).toEqual(background);
    await visibility('visible'); expect(statusReads).toBe(background.statusReads + 1);
    expect(cartReads).toBe(background.cartReads);
    // The first tick also refreshes an aged menu; adopting that menu can add
    // an immediate cart read. The following tick has only the periodic reads.
    await advance(2500); expect(cartReads).toBeGreaterThanOrEqual(background.cartReads + 2);
    const resumedCartReads = cartReads;
    await advance(2500); expect(cartReads).toBe(resumedCartReads + 2);
    await act(async () => roots.forEach((root) => root.unmount()));
    const unmounted = fetcher.mock.calls.length;
    await advance(90_000); expect(fetcher).toHaveBeenCalledTimes(unmounted);
  } finally {
    await act(async () => roots.forEach((root) => root.unmount()));
    hosts.forEach((host) => host.remove());
    sessionStorage.clear(); window.history.replaceState({}, '', '/');
    visible('visible'); vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.useRealTimers();
  }
});

async function openCartSheet(host: HTMLElement) {
  const trigger = within(host).getByRole('button', { name: /カートを開く|開啟購物車/ });
  await act(async () => { trigger.click(); for (let i = 0; i < 5; i++) await Promise.resolve(); });
}

async function closeCart() {
  const close = within(document.body).getByRole('button', { name: '閉じる' });
  await act(async () => { close.click(); for (let i = 0; i < 5; i++) await Promise.resolve(); });
}
