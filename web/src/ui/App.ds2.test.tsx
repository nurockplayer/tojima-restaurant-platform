// @vitest-environment jsdom
import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { App } from './App';

const table = { mode: 'DIRECT_SCAN', current_session_id: 'session-1', open: true };
const grant = { table_join_token: 'grant-1', expires_at: '2030-01-01T00:00:00Z', session: { id: 'session-1', mode: 'DIRECT_SCAN' } };
const menu = {
  currency: 'JPY', price_mode: 'TAX_INCLUSIVE',
  categories: [{ key: 'food', name: 'お食事', position: 1, products: [{
    key: 'meal', name: '定食', description: '日替わりの定食です', effective_price_minor: '960', position: 1,
    availability: 'AVAILABLE', selectable: true,
    option_groups: [{ key: 'rice', name: 'ご飯の量', min_select: 1, max_select: 1, position: 1, options: [
      { key: 'regular', name: '普通', price_delta_minor: '0', position: 1, availability: 'AVAILABLE' },
      { key: 'large', name: '大盛り', price_delta_minor: '20', position: 2, availability: 'AVAILABLE' },
    ] }],
  }] }],
};
const emptyCart = { session_id: 'session-1', generation: 1, version: 0, lines: [] };
const cartWithMeal = { ...emptyCart, lines: [{ line_id: 'line-1', product_key: 'meal', product_name: '定食', product_available: true, quantity: 1, options: [] }] };
const previewForMeal = {
  session_id: 'session-1', generation: 1, version: 0, fingerprint: 'preview-1',
  quote: { lines: [{ line_id: 'line-1', product_key: 'meal', name: '定食', base_gross_jpy: '960', quantity: 1, options: [] }],
    tax: { currency: 'JPY', price_mode: 'INCLUSIVE', total_gross_jpy: '960', total_net_jpy: '873', total_tax_jpy: '87', buckets: [] } },
};

type FetchHandler = (url: string, init: RequestInit) => Response | Promise<Response>;

function installGuestFetch(onAdd: FetchHandler = () => json(emptyCart), options: { onCartRead?: FetchHandler } = {}) {
  const calls: Array<{ url: string; init: RequestInit }> = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    calls.push({ url, init });
    if (url.endsWith('/v1/guest/tables/ds2-app')) return json(table);
    if (url.endsWith('/v1/guest/tables/ds2-app/join')) return json(grant);
    if (url.endsWith('/v1/guest/tables/ds2-app/menu?amounts=string')) return json(menu);
    if (url.endsWith('/v1/guest/sessions/session-1/cart/lines') && init.method === 'POST') return onAdd(url, init);
    if (url.endsWith('/v1/guest/sessions/session-1/cart')) return options.onCartRead ? options.onCartRead(url, init) : json(emptyCart);
    if (url.endsWith('/v1/guest/sessions/session-1/orders')) return json({ orders: [] });
    if (url.endsWith('/v1/guest/sessions/session-1/operations')) return json({ orders: [], cash_enabled: true });
    throw new Error(`Unexpected API request: ${url}`);
  });
  vi.stubGlobal('fetch', fetchMock);
  return { calls, fetchMock };
}

function installCartFetch(options: {
  onRemove?: FetchHandler;
  onPreview?: FetchHandler;
  onCommit?: FetchHandler;
  onCartRead?: FetchHandler;
  getMenu?: () => unknown;
  initialCart?: unknown | (() => unknown);
} = {}) {
  const calls: Array<{ url: string; init: RequestInit }> = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    calls.push({ url, init });
    if (url.endsWith('/v1/guest/tables/ds2-app')) return json(table);
    if (url.endsWith('/v1/guest/tables/ds2-app/join')) return json(grant);
    if (url.endsWith('/v1/guest/tables/ds2-app/menu?amounts=string')) return json(options.getMenu?.() ?? menu);
    if (url.endsWith('/v1/guest/sessions/session-1/cart/preview')) return options.onPreview ? options.onPreview(url, init) : json(previewForMeal);
    if (url.endsWith('/v1/guest/sessions/session-1/cart/commit')) return options.onCommit ? options.onCommit(url, init) : json({ error: 'Unexpected commit' }, 500);
    if (url.includes('/v1/guest/sessions/session-1/cart/lines/') && init.method === 'DELETE') return options.onRemove ? options.onRemove(url, init) : json(emptyCart);
    if (url.endsWith('/v1/guest/sessions/session-1/cart')) {
      if (options.onCartRead) return options.onCartRead(url, init);
      const current = typeof options.initialCart === 'function' ? options.initialCart() : options.initialCart;
      return json(current ?? cartWithMeal);
    }
    if (url.endsWith('/v1/guest/sessions/session-1/orders')) return json({ orders: [] });
    if (url.endsWith('/v1/guest/sessions/session-1/operations')) return json({ orders: [], cash_enabled: true });
    throw new Error(`Unexpected API request: ${url}`);
  });
  vi.stubGlobal('fetch', fetchMock);
  return { calls, fetchMock };
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

function expectSameOperation(first: RequestInit, retry: RequestInit) {
  const key = new Headers(first.headers).get('Idempotency-Key');
  expect(key).toBeTruthy();
  expect(new Headers(retry.headers).get('Idempotency-Key')).toBe(key);
  expect(first.body).toBe(retry.body);
}

async function openOptions(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole('button', { name: 'カートに追加' }));
  return screen.getByRole('dialog');
}

async function openCartSheet(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole('button', { name: 'カートを開く' }));
  return screen.getByRole('dialog');
}

afterEach(async () => {
  cleanup();
  if (vi.isFakeTimers()) await vi.advanceTimersByTimeAsync(64);
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  vi.useRealTimers();
  sessionStorage.clear();
  window.history.replaceState({}, '', '/');
});

describe('guest App option sheet acceptance', () => {
  it('shows required-group validation without an API call and focuses the first selectable option', async () => {
    window.history.replaceState({}, '', '/?table=ds2-app');
    const { calls } = installGuestFetch();
    const user = userEvent.setup();
    render(<App />);

    const dialog = await openOptions(user);
    const beforeAdd = calls.filter(({ url, init }) => init.method === 'POST' && url.endsWith('/cart/lines')).length;
    await user.click(within(dialog).getByRole('button', { name: 'カートに追加' }));

    const group = within(dialog).getByRole('group', { name: /ご飯の量/ });
    expect(group.getAttribute('aria-invalid')).toBe('true');
    expect(group.getAttribute('aria-describedby')).toBeTruthy();
    expect(within(dialog).getByRole('alert').textContent).toContain('必須オプションを選択してください。');
    expect(document.activeElement).toBe(within(dialog).getByRole('radio', { name: /普通/ }));
    expect(calls.filter(({ url, init }) => init.method === 'POST' && url.endsWith('/cart/lines'))).toHaveLength(beforeAdd);
  });

  it.each([
    ['4xx', 422, { error: 'invalid_option' }],
    ['409', 409, { error: 'stale', cart: emptyCart }],
  ])('keeps the sheet and its error open after an add %s response', async (_label, status, body) => {
    window.history.replaceState({}, '', '/?table=ds2-app');
    const { calls } = installGuestFetch(() => json(body, status));
    const user = userEvent.setup();
    render(<App />);

    const dialog = await openOptions(user);
    await user.click(within(dialog).getByRole('radio', { name: /普通/ }));
    await user.click(within(dialog).getByRole('button', { name: 'カートに追加' }));

    expect(await screen.findByRole('alert')).toBeTruthy();
    expect(screen.getByRole('dialog')).toBeTruthy();
    expect(calls.filter(({ url, init }) => init.method === 'POST' && url.endsWith('/cart/lines'))).toHaveLength(1);
  });

  it('retries a 503 add with the same replay key and body, then closes and restores focus', async () => {
    window.history.replaceState({}, '', '/?table=ds2-app');
    const acceptedCart = { ...emptyCart, version: 1, lines: [{ line_id: 'line-1', product_key: 'meal', product_name: '定食', product_available: true, quantity: 1, options: [{ group_key: 'rice', option_key: 'regular', group_name: 'ご飯の量', option_name: '普通', available: true }] }] };
    let adds = 0;
    const { calls } = installGuestFetch(() => ++adds === 1 ? json({ error: 'temporary' }, 503) : json(acceptedCart));
    const user = userEvent.setup();
    render(<App />);

    const trigger = await screen.findByRole('button', { name: 'カートに追加' });
    const dialog = await openOptions(user);
    await user.click(within(dialog).getByRole('radio', { name: /普通/ }));
    await user.click(within(dialog).getByRole('button', { name: 'カートに追加' }));
    await user.click(await within(screen.getByRole('dialog')).findByRole('button', { name: '前回の操作を照合して再試行' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(trigger));
    const requests = calls.filter(({ url, init }) => init.method === 'POST' && url.endsWith('/cart/lines'));
    expect(requests).toHaveLength(2);
    expectSameOperation(requests[0]!.init, requests[1]!.init);
  });

  it('keeps focus on a safe heading until a replay opener is enabled, then restores it', async () => {
    window.history.replaceState({}, '', '/?table=ds2-app');
    const acceptedCart = { ...emptyCart, version: 1, lines: [{ line_id: 'line-1', product_key: 'meal', product_name: '定食', product_available: true, quantity: 1, options: [{ group_key: 'rice', option_key: 'regular', group_name: 'ご飯の量', option_name: '普通', available: true }] }] };
    let adds = 0;
    let holdPostReplayRefresh = false;
    let postReplayRefreshStarted = false;
    const pendingRefresh = deferred<Response>();
    const { calls } = installGuestFetch(
      () => ++adds === 1 ? json({ error: 'temporary' }, 503) : json(acceptedCart),
      { onCartRead: () => {
        if (holdPostReplayRefresh) {
          postReplayRefreshStarted = true;
          return pendingRefresh.promise;
        }
        return json(emptyCart);
      } },
    );
    const user = userEvent.setup();
    render(<App />);

    const trigger = await screen.findByRole('button', { name: 'カートに追加' });
    const dialog = await openOptions(user);
    await user.click(within(dialog).getByRole('radio', { name: /普通/ }));
    await user.click(within(dialog).getByRole('button', { name: 'カートに追加' }));
    const retry = await within(screen.getByRole('dialog')).findByRole('button', { name: '前回の操作を照合して再試行' });
    holdPostReplayRefresh = true;
    await user.click(retry);

    await waitFor(() => expect(postReplayRefreshStarted).toBe(true));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect((trigger as HTMLButtonElement).disabled).toBe(true);
    const menuHeading = screen.getByRole('heading', { name: 'お品書き' });
    await waitFor(() => expect(document.activeElement).toBe(menuHeading));
    const interimFocus = document.activeElement;
    expect(interimFocus).not.toBe(document.body);
    expect(interimFocus?.isConnected).toBe(true);
    expect((interimFocus as HTMLElement | null)?.tabIndex).toBe(-1);
    expect((interimFocus as HTMLElement | null)?.matches('h1, h2, h3')).toBe(true);

    await act(async () => { pendingRefresh.resolve(json(acceptedCart)); await Promise.resolve(); });
    await waitFor(() => expect((trigger as HTMLButtonElement).disabled).toBe(false));
    await waitFor(() => expect(document.activeElement).toBe(trigger));

    const requests = calls.filter(({ url, init }) => init.method === 'POST' && url.endsWith('/cart/lines'));
    expect(requests).toHaveLength(2);
    expectSameOperation(requests[0]!.init, requests[1]!.init);
  });

  it('closes after an accepted add and returns focus to the menu trigger', async () => {
    window.history.replaceState({}, '', '/?table=ds2-app');
    const acceptedCart = { ...emptyCart, version: 1, lines: [{ line_id: 'line-1', product_key: 'meal', product_name: '定食', product_available: true, quantity: 1, options: [{ group_key: 'rice', option_key: 'regular', group_name: 'ご飯の量', option_name: '普通', available: true }] }] };
    installGuestFetch(() => json(acceptedCart));
    const user = userEvent.setup();
    render(<App />);

    const trigger = await screen.findByRole('button', { name: 'カートに追加' });
    const dialog = await openOptions(user);
    await user.click(within(dialog).getByRole('radio', { name: /普通/ }));
    await user.click(within(dialog).getByRole('button', { name: 'カートに追加' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(trigger));
  });

  it('allows replay after a network failure and closes only after the same operation is accepted', async () => {
    window.history.replaceState({}, '', '/?table=ds2-app');
    const acceptedCart = { ...emptyCart, version: 1, lines: [{ line_id: 'line-1', product_key: 'meal', product_name: '定食', product_available: true, quantity: 1, options: [{ group_key: 'rice', option_key: 'regular', group_name: 'ご飯の量', option_name: '普通', available: true }] }] };
    let adds = 0;
    const { calls } = installGuestFetch(() => {
      if (++adds === 1) throw new TypeError('network down');
      return json(acceptedCart);
    });
    const user = userEvent.setup();
    render(<App />);

    const trigger = await screen.findByRole('button', { name: 'カートに追加' });
    const dialog = await openOptions(user);
    await user.click(within(dialog).getByRole('radio', { name: /普通/ }));
    await user.click(within(dialog).getByRole('button', { name: 'カートに追加' }));
    const retry = await within(screen.getByRole('dialog')).findByRole('button', { name: '前回の操作を照合して再試行' });
    expect(screen.getByRole('dialog')).toBeTruthy();
    await user.click(retry);

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(trigger));
    const requests = calls.filter(({ url, init }) => init.method === 'POST' && url.endsWith('/cart/lines'));
    expect(requests).toHaveLength(2);
    expectSameOperation(requests[0]!.init, requests[1]!.init);
  });

  it('closes after a delayed add accepted behind a newer poll while retaining the newer cart', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'requestAnimationFrame', 'cancelAnimationFrame'] });
    window.history.replaceState({}, '', '/?table=ds2-app');
    const addResponse = deferred<Response>();
    const newestCart = { ...emptyCart, version: 2, lines: [{ line_id: 'remote-line', product_key: 'removed-product', product_name: 'Other guest item', product_available: false, quantity: 1, options: [] }] };
    const staleAddCart = { ...emptyCart, version: 1, lines: [{ line_id: 'local-line', product_key: 'meal', product_name: '定食', product_available: true, quantity: 1, options: [] }] };
    let cartReads = 0;
    let pollEnabled = false;
    const { calls } = installGuestFetch(() => addResponse.promise);
    const fetchMock = vi.mocked(fetch);
    fetchMock.mockImplementation(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input);
      calls.push({ url, init });
      if (url.endsWith('/v1/guest/tables/ds2-app')) return json(table);
      if (url.endsWith('/v1/guest/tables/ds2-app/join')) return json(grant);
      if (url.endsWith('/v1/guest/tables/ds2-app/menu?amounts=string')) return json(menu);
      if (url.endsWith('/v1/guest/sessions/session-1/cart/lines') && init.method === 'POST') return addResponse.promise;
      if (url.endsWith('/v1/guest/sessions/session-1/cart')) return json(++cartReads > 0 && pollEnabled ? newestCart : emptyCart);
      if (url.endsWith('/v1/guest/sessions/session-1/orders')) return json({ orders: [] });
      if (url.endsWith('/v1/guest/sessions/session-1/operations')) return json({ orders: [], cash_enabled: true });
      throw new Error(`Unexpected API request: ${url}`);
    });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<App />);
    await act(async () => { for (let index = 0; index < 30; index++) await Promise.resolve(); });
    await user.click(screen.getByRole('button', { name: 'カートに追加' }));
    const dialog = screen.getByRole('dialog');
    await user.click(within(dialog).getByRole('radio', { name: /普通/ }));
    await user.click(within(dialog).getByRole('button', { name: 'カートに追加' }));
    expect(screen.getByRole('dialog')).toBeTruthy();
    expect(document.body.textContent).not.toContain('Other guest item');
    pollEnabled = true;
    await act(async () => { await vi.advanceTimersByTimeAsync(2500); });
    expect(cartReads).toBeGreaterThan(1);

    await act(async () => { addResponse.resolve(json(staleAddCart)); await Promise.resolve(); await Promise.resolve(); });
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    await user.click(await screen.findByRole('button', { name: 'カートを開く' }));
    expect(await within(screen.getByRole('dialog')).findByText('Other guest item')).toBeTruthy();
    const addCalls = calls.filter(({ url, init }) => init.method === 'POST' && url.endsWith('/cart/lines'));
    expect(addCalls).toHaveLength(1);
  });

  it('closes after a replay accepted behind a newer poll while retaining the newer cart', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'requestAnimationFrame', 'cancelAnimationFrame'] });
    window.history.replaceState({}, '', '/?table=ds2-app');
    const replayResponse = deferred<Response>();
    const newestCart = { ...emptyCart, version: 2, lines: [{ line_id: 'remote-line', product_key: 'removed-product', product_name: 'Other guest item', product_available: false, quantity: 1, options: [] }] };
    const staleAddCart = { ...emptyCart, version: 1, lines: [{ line_id: 'local-line', product_key: 'meal', product_name: '定食', product_available: true, quantity: 1, options: [] }] };
    let cartReads = 0;
    let pollEnabled = false;
    let addCallsCount = 0;
    const { calls } = installGuestFetch(() => ++addCallsCount === 1 ? json({ error: 'temporary' }, 503) : replayResponse.promise);
    const fetchMock = vi.mocked(fetch);
    fetchMock.mockImplementation(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input);
      calls.push({ url, init });
      if (url.endsWith('/v1/guest/tables/ds2-app')) return json(table);
      if (url.endsWith('/v1/guest/tables/ds2-app/join')) return json(grant);
      if (url.endsWith('/v1/guest/tables/ds2-app/menu?amounts=string')) return json(menu);
      if (url.endsWith('/v1/guest/sessions/session-1/cart/lines') && init.method === 'POST') return addCallsCount++ === 0 ? json({ error: 'temporary' }, 503) : replayResponse.promise;
      if (url.endsWith('/v1/guest/sessions/session-1/cart')) return json(++cartReads > 0 && pollEnabled ? newestCart : emptyCart);
      if (url.endsWith('/v1/guest/sessions/session-1/orders')) return json({ orders: [] });
      if (url.endsWith('/v1/guest/sessions/session-1/operations')) return json({ orders: [], cash_enabled: true });
      throw new Error(`Unexpected API request: ${url}`);
    });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<App />);
    await act(async () => { for (let index = 0; index < 30; index++) await Promise.resolve(); });
    await user.click(screen.getByRole('button', { name: 'カートに追加' }));
    const dialog = screen.getByRole('dialog');
    await user.click(within(dialog).getByRole('radio', { name: /普通/ }));
    await user.click(within(dialog).getByRole('button', { name: 'カートに追加' }));
    const retry = await within(screen.getByRole('dialog')).findByRole('button', { name: '前回の操作を照合して再試行' });
    await user.click(retry);
    expect(screen.getByRole('dialog')).toBeTruthy();
    expect(document.body.textContent).not.toContain('Other guest item');
    pollEnabled = true;
    await act(async () => { await vi.advanceTimersByTimeAsync(2500); });
    expect(cartReads).toBeGreaterThan(1);

    await act(async () => { replayResponse.resolve(json(staleAddCart)); await Promise.resolve(); await Promise.resolve(); });
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    await user.click(await screen.findByRole('button', { name: 'カートを開く' }));
    expect(await within(screen.getByRole('dialog')).findByText('Other guest item')).toBeTruthy();
    const addCalls = calls.filter(({ url, init }) => init.method === 'POST' && url.endsWith('/cart/lines'));
    expect(addCalls).toHaveLength(2);
    expectSameOperation(addCalls[0]!.init, addCalls[1]!.init);
  });

  it('does not close the option sheet when a cart poll reports another guest change', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'requestAnimationFrame', 'cancelAnimationFrame'] });
    window.history.replaceState({}, '', '/?table=ds2-app');
    const pollCart = { ...emptyCart, version: 1, lines: [{ line_id: 'other-line', product_key: 'meal', product_name: '定食', product_available: true, quantity: 1, options: [] }] };
    let cartReads = 0;
    let pollEnabled = false;
    const { calls } = installGuestFetch();
    // Replace the shared fetch stub's cart response for the first periodic read.
    const fetchMock = vi.mocked(fetch);
    fetchMock.mockImplementation(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input);
      calls.push({ url, init });
      if (url.endsWith('/v1/guest/tables/ds2-app')) return json(table);
      if (url.endsWith('/v1/guest/tables/ds2-app/join')) return json(grant);
      if (url.endsWith('/v1/guest/tables/ds2-app/menu?amounts=string')) return json(menu);
      if (url.endsWith('/v1/guest/sessions/session-1/cart')) return json(++cartReads > 0 && pollEnabled ? pollCart : emptyCart);
      if (url.endsWith('/v1/guest/sessions/session-1/orders')) return json({ orders: [] });
      if (url.endsWith('/v1/guest/sessions/session-1/operations')) return json({ orders: [], cash_enabled: true });
      throw new Error(`Unexpected API request: ${url}`);
    });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<App />);
    await act(async () => { for (let index = 0; index < 30; index++) await Promise.resolve(); });
    await user.click(screen.getByRole('button', { name: 'カートに追加' }));
    pollEnabled = true;

    await act(async () => { await vi.advanceTimersByTimeAsync(2500); });
    expect(screen.getByRole('dialog')).toBeTruthy();
    expect(calls.filter(({ url }) => url.endsWith('/cart')).length).toBeGreaterThan(1);
  });
});

describe('guest App cart sheet focus and destinations', () => {
  it('moves focus into preview, returns it to the preview button, and keeps one DemoNotice', async () => {
    window.history.replaceState({}, '', '/?table=ds2-app');
    installCartFetch();
    const user = userEvent.setup();
    const { unmount } = render(<App demo />);
    expect(document.documentElement.getAttribute('data-surface')).toBe('guest');

    const dialog = await openCartSheet(user);
    expect(within(dialog).getAllByRole('note')).toHaveLength(1);
    expect(within(dialog).getByRole('note').textContent).toBe('DEMO・参考価格：実際の注文・支払いは行われません');
    const previewButton = within(dialog).getByRole('button', { name: '金額を確認する' });
    await user.click(previewButton);
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('heading', { name: '金額を確認する' })));
    expect(screen.getByRole('dialog')).toBeTruthy();
    expect(within(screen.getByRole('dialog')).getAllByRole('note')).toHaveLength(1);
    await user.tab();
    expect(document.activeElement).toBe(within(screen.getByRole('dialog')).getByRole('button', { name: '閉じる' }));

    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'カートへ戻る' }));
    const cartPreviewButton = screen.getByRole('button', { name: '金額を確認する' });
    await waitFor(() => expect(document.activeElement).toBe(cartPreviewButton));
    expect(screen.getByRole('dialog')).toBeTruthy();
    await user.tab();
    expect(document.activeElement).toBe(within(screen.getByRole('dialog')).getByRole('button', { name: '閉じる' }));
    unmount();
    expect(document.documentElement.hasAttribute('data-surface')).toBe(false);
  });

  it('closes an emptied cart to the menu heading and clears background modal attributes', async () => {
    window.history.replaceState({}, '', '/?table=ds2-app');
    installCartFetch({ onRemove: () => json({ ...emptyCart, version: 1 }) });
    const user = userEvent.setup();
    render(<App />);

    const dialog = await openCartSheet(user);
    expect(within(dialog).queryByRole('note')).toBeNull();
    await user.click(within(dialog).getByRole('button', { name: '削除' }));
    expect(await within(screen.getByRole('dialog')).findByText('カートはまだ空です。')).toBeTruthy();
    await user.keyboard('{Escape}');

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    const menuHeading = screen.getByRole('heading', { name: 'お品書き' });
    await waitFor(() => expect(document.activeElement).toBe(menuHeading));
    await waitFor(() => expect(document.querySelector('[inert], [aria-hidden="true"]')).toBeNull());
  });

  it('keeps the cart sheet open and updates its line after another guest poll', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'requestAnimationFrame', 'cancelAnimationFrame'] });
    window.history.replaceState({}, '', '/?table=ds2-app');
    const remoteCart = { ...cartWithMeal, version: 1, lines: [{ ...cartWithMeal.lines[0]!, quantity: 3 }] };
    let cartReads = 0;
    let pollEnabled = false;
    installCartFetch({ initialCart: () => ++cartReads > 0 && pollEnabled ? remoteCart : cartWithMeal });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<App />);
    await act(async () => { for (let index = 0; index < 30; index++) await Promise.resolve(); });
    const dialog = await openCartSheet(user);
    expect(within(dialog).getByText('定食')).toBeTruthy();
    pollEnabled = true;

    await act(async () => { await vi.advanceTimersByTimeAsync(2500); });
    expect(screen.getByRole('dialog')).toBeTruthy();
    expect(screen.getByRole('dialog').querySelector('[data-quantity]')?.textContent).toBe('3');
  });

  it('restores focus to the enabled cart bar when the cart sheet closes during a replay refresh', async () => {
    window.history.replaceState({}, '', '/?table=ds2-app');
    let removeCalls = 0;
    let holdPostReplayRefresh = false;
    let postReplayRefreshStarted = false;
    const replayResponse = deferred<Response>();
    const pendingRefresh = deferred<Response>();
    installCartFetch({
      onRemove: () => ++removeCalls === 1 ? json({ error: 'temporary' }, 503) : replayResponse.promise,
      onCartRead: () => {
        if (holdPostReplayRefresh) {
          postReplayRefreshStarted = true;
          return pendingRefresh.promise;
        }
        return json(cartWithMeal);
      },
    });
    const user = userEvent.setup();
    render(<App />);

    const cartBar = await screen.findByRole('button', { name: 'カートを開く' });
    const dialog = await openCartSheet(user);
    await user.click(within(dialog).getByRole('button', { name: '削除' }));
    const retry = await within(screen.getByRole('dialog')).findByRole('button', { name: '前回の操作を照合して再試行' });
    holdPostReplayRefresh = true;
    await user.click(retry);
    await act(async () => { replayResponse.resolve(json(cartWithMeal)); await Promise.resolve(); });
    await waitFor(() => expect(postReplayRefreshStarted).toBe(true));

    expect(screen.getByRole('dialog')).toBeTruthy();
    expect(cartBar.hasAttribute('disabled')).toBe(false);
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(cartBar));
    expect(document.activeElement).not.toBe(document.body);

    await act(async () => { pendingRefresh.resolve(json(cartWithMeal)); await Promise.resolve(); });
  });

  it('keeps the cart and menu polling cadence when the cart sheet opens and closes', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'requestAnimationFrame', 'cancelAnimationFrame', 'Date'] });
    window.history.replaceState({}, '', '/?table=ds2-app');
    const { calls } = installCartFetch();
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<App />);
    await act(async () => { for (let index = 0; index < 30; index++) await Promise.resolve(); });
    const cartReads = () => calls.filter(({ url }) => url.endsWith('/v1/guest/sessions/session-1/cart')).length;
    const menuReads = () => calls.filter(({ url }) => url.endsWith('/menu?amounts=string')).length;
    const initialCartReads = cartReads();
    const initialMenuReads = menuReads();

    await openCartSheet(user);
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(cartReads()).toBe(initialCartReads);
    expect(menuReads()).toBe(initialMenuReads);

    await act(async () => { await vi.advanceTimersByTimeAsync(2499); });
    expect(cartReads()).toBe(initialCartReads);
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    expect(cartReads()).toBe(initialCartReads + 1);
    await act(async () => { await vi.advanceTimersByTimeAsync(12_499); });
    expect(menuReads()).toBe(initialMenuReads);
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    expect(menuReads()).toBe(initialMenuReads + 1);
  });

  it('returns focus to the cart heading and explains when a cart poll invalidates preview', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'requestAnimationFrame', 'cancelAnimationFrame'] });
    window.history.replaceState({}, '', '/?table=ds2-app');
    const remoteCart = { ...cartWithMeal, version: 1, lines: [{ ...cartWithMeal.lines[0]!, quantity: 2 }] };
    let cartReads = 0;
    let pollEnabled = false;
    installCartFetch({ initialCart: () => ++cartReads > 0 && pollEnabled ? remoteCart : cartWithMeal });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<App />);
    await act(async () => { for (let index = 0; index < 30; index++) await Promise.resolve(); });
    await openCartSheet(user);
    await user.click(screen.getByRole('button', { name: '金額を確認する' }));
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('heading', { name: '金額を確認する' })));
    pollEnabled = true;

    await act(async () => { await vi.advanceTimersByTimeAsync(2500); });
    const cartHeading = screen.getByRole('heading', { name: '共有カート' });
    await waitFor(() => expect(document.activeElement).toBe(cartHeading));
    expect(screen.getByRole('dialog')).toBeTruthy();
    expect(within(screen.getByRole('dialog')).getByRole('alert')).toBeTruthy();
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(document.querySelectorAll('[role="alert"]')).toHaveLength(1);
    expect(screen.getByRole('alert')).toBeTruthy();
  });

  it('focuses the cart heading after Back when the preview button became disabled', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'requestAnimationFrame', 'cancelAnimationFrame', 'Date'] });
    window.history.replaceState({}, '', '/?table=ds2-app');
    let held = false;
    const heldMenu = () => ({
      ...menu,
      categories: menu.categories.map((category) => ({ ...category, products: category.products.map((product) => ({
        ...product, selectable: !held, company_emergency_hold_active: held,
      })) })),
    });
    installCartFetch({ getMenu: heldMenu });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<App />);
    await act(async () => { for (let index = 0; index < 30; index++) await Promise.resolve(); });
    await openCartSheet(user);
    await user.click(screen.getByRole('button', { name: '金額を確認する' }));
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('heading', { name: '金額を確認する' })));
    held = true;
    await act(async () => { await vi.advanceTimersByTimeAsync(15_000); });

    await user.click(screen.getByRole('button', { name: 'カートへ戻る' }));
    const cartHeading = screen.getByRole('heading', { name: '共有カート' });
    await waitFor(() => expect(document.activeElement).toBe(cartHeading));
    expect(screen.getByRole('button', { name: '金額を確認する' }).hasAttribute('disabled')).toBe(true);
  });

  it('closes a confirmed cart to the orders heading and hides the cart bar', async () => {
    window.history.replaceState({}, '', '/?table=ds2-app');
    const result = {
      outcome: 'COMMITTED',
      order: { order_id: 'order-1', session_id: 'session-1', generation: 1, payment_method: 'CASH', quote: previewForMeal.quote, created_at: '2026-10-20T00:00:00Z' },
      cart: { ...emptyCart, version: 1 },
    };
    installCartFetch({ onCommit: () => json(result) });
    const user = userEvent.setup();
    render(<App demo />);

    await openCartSheet(user);
    await user.click(screen.getByRole('button', { name: '金額を確認する' }));
    await screen.findByRole('heading', { name: '金額を確認する' });
    await user.click(screen.getByRole('button', { name: 'この内容で注文を確定' }));

    const ordersHeading = await screen.findByRole('heading', { name: '注文履歴' });
    await waitFor(() => expect(document.activeElement).toBe(ordersHeading));
    expect(screen.queryByRole('button', { name: 'カートを開く' })).toBeNull();
    expect(screen.queryByRole('dialog')).toBeNull();
    await waitFor(() => expect(document.querySelector('[inert], [aria-hidden="true"]')).toBeNull());
  });

  it('closes after replay confirms the order, focuses orders, and preserves the commit key and body', async () => {
    window.history.replaceState({}, '', '/?table=ds2-app');
    const result = {
      outcome: 'COMMITTED',
      order: { order_id: 'order-replayed', session_id: 'session-1', generation: 1, payment_method: 'CASH', quote: previewForMeal.quote, created_at: '2026-10-20T00:00:00Z' },
      cart: { ...emptyCart, version: 1 },
    };
    let commitCount = 0;
    const { calls } = installCartFetch({ onCommit: () => ++commitCount === 1 ? json({ error: 'temporary' }, 503) : json(result) });
    const user = userEvent.setup();
    render(<App demo />);

    const dialog = await openCartSheet(user);
    await user.click(within(dialog).getByRole('button', { name: '金額を確認する' }));
    await screen.findByRole('heading', { name: '金額を確認する' });
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'この内容で注文を確定' }));
    await user.click(await within(screen.getByRole('dialog')).findByRole('button', { name: '前回の操作を照合して再試行' }));

    const ordersHeading = await screen.findByRole('heading', { name: '注文履歴' });
    await waitFor(() => expect(document.activeElement).toBe(ordersHeading));
    const commits = calls.filter(({ url, init }) => init.method === 'POST' && url.endsWith('/cart/commit'));
    expect(commits).toHaveLength(2);
    expectSameOperation(commits[0]!.init, commits[1]!.init);
    expect(screen.queryByRole('button', { name: 'カートを開く' })).toBeNull();
    expect(screen.queryByRole('dialog')).toBeNull();
    await waitFor(() => expect(document.querySelector('[inert], [aria-hidden="true"]')).toBeNull());
  });

  it('returns focus to the entry heading after a cart action expires the session', async () => {
    window.history.replaceState({}, '', '/?table=ds2-app');
    installCartFetch({ onRemove: () => json({ error: 'expired' }, 403) });
    const user = userEvent.setup();
    render(<App />);

    const dialog = await openCartSheet(user);
    await user.click(within(dialog).getByRole('button', { name: '削除' }));
    const entryHeading = await screen.findByRole('heading', { name: 'ご注文を始める' });
    await waitFor(() => expect(document.activeElement).toBe(entryHeading));
    expect(screen.queryByRole('dialog')).toBeNull();
    await waitFor(() => expect(document.querySelector('[inert], [aria-hidden="true"]')).toBeNull());
  });

  it('renders a cart error either inside the open sheet or in page feedback, never twice', async () => {
    window.history.replaceState({}, '', '/?table=ds2-app');
    installCartFetch({ onRemove: () => json({ error: 'rejected' }, 422) });
    const user = userEvent.setup();
    render(<App />);

    const dialog = await openCartSheet(user);
    await user.click(within(dialog).getByRole('button', { name: '削除' }));
    expect(await within(screen.getByRole('dialog')).findByRole('alert')).toBeTruthy();
    expect(document.querySelectorAll('[role="alert"]')).toHaveLength(1);
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(document.querySelectorAll('[role="alert"]')).toHaveLength(1);
    expect(screen.getByRole('alert')).toBeTruthy();
  });
});
