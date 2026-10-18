// @vitest-environment jsdom
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { expect, it, vi } from 'vitest';
import { App } from './App';

it('P84 renders untrusted menu category, product and description as literal text', async () => {
  const category = '<script>window.p84Injected=true</script>';
  const name = '<img src=x onerror="window.p84Injected=true">';
  const description = '<svg onload="window.p84Injected=true"></svg>';
  const json = (value: unknown) => new Response(JSON.stringify(value), { headers: { 'Content-Type': 'application/json' } });
  window.history.replaceState({}, '', '/?table=p84-xss');
  sessionStorage.clear();
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const path = String(input);
    if (path === '/v1/guest/tables/p84-xss') return json({ mode: 'DIRECT_SCAN', open: true, current_session_id: 'p84-session' });
    if (path === '/v1/guest/tables/p84-xss/join') return json({ table_join_token: 'p84-test-grant', expires_at: '2030-01-01T00:00:00Z', session: { id: 'p84-session', mode: 'DIRECT_SCAN' } });
    if (path === '/v1/guest/tables/p84-xss/menu?amounts=string') return json({
      currency: 'JPY', price_mode: 'TAX_INCLUSIVE', categories: [{ key: 'food', name: category, position: 0, products: [{
        key: 'meal', name, description, effective_price_minor: '100', position: 0, availability: 'AVAILABLE', selectable: true, option_groups: [],
      }] }],
    });
    if (path === '/v1/guest/sessions/p84-session/cart') return json({ session_id: 'p84-session', generation: 1, version: 1, lines: [] });
    if (path === '/v1/guest/sessions/p84-session/orders') return json({ orders: [] });
    if (path === '/v1/guest/sessions/p84-session/operations') return json({ orders: [], cash_enabled: true });
    throw new Error(`Unexpected P84 request: ${path}`);
  }));
  const host = document.createElement('div');
  document.body.append(host);
  const root = createRoot(host);
  try {
    await act(async () => {
      root.render(<App />);
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(host.querySelector('h3')?.textContent).toBe(category);
    expect(host.querySelector('.product-card h4')?.textContent).toBe(name);
    expect(host.querySelector('.product-card p')?.textContent).toBe(description);
    // Assert DOM structure as well as non-execution: jsdom does not execute
    // every injected script/event, so absence of a side effect alone is weak.
    expect(host.querySelector('script, img, svg, [onerror], [onload]')).toBeNull();
    expect(host.innerHTML).toContain('&lt;img');
  } finally {
    await act(async () => root.unmount());
    host.remove();
    sessionStorage.clear();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    window.history.replaceState({}, '', '/');
  }
});
