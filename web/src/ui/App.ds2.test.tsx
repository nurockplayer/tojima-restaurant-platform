// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
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

function blockFocusBehindModal() {
  const original = HTMLElement.prototype.focus;
  const focus = vi.spyOn(HTMLElement.prototype, 'focus');
  const blocked: HTMLElement[] = [];
  focus.mockImplementation(function (this: HTMLElement, options?: FocusOptions) {
    if (this.closest('[inert], [aria-hidden="true"]')) {
      blocked.push(this);
      return;
    }
    original.call(this, options);
  });
  return { focus, blocked };
}

function holdGuestHeadingInert(text: string) {
  let root: HTMLElement | null = null;
  let resolveReady!: (element: HTMLElement) => void;
  const ready = new Promise<HTMLElement>((resolve) => { resolveReady = resolve; });
  const observer = new MutationObserver(() => {
    const heading = [...document.querySelectorAll<HTMLElement>('main h1, main h2')]
      .find((element) => element.textContent?.trim() === text);
    if (!heading || root) return;
    root = heading.closest('main')!;
    root.setAttribute('inert', '');
    resolveReady(root);
    observer.disconnect();
  });
  observer.observe(document.body, { subtree: true, childList: true });
  return { ready, release: () => root?.removeAttribute('inert') };
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
  await user.click(await screen.findByRole('button', { name: /^カートを開く / }));
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
  it('does not reopen the option sheet when a later menu refresh restores the product', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'requestAnimationFrame', 'cancelAnimationFrame', 'Date'] });
    window.history.replaceState({}, '', '/?table=ds2-app');
    let nextMenu: typeof menu | { currency: string; price_mode: string; categories: [] } | null = null;
    installCartFetch({ getMenu: () => {
      if (nextMenu) {
        const response = nextMenu;
        nextMenu = null;
        return response;
      }
      return menu;
    } });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<App />);
    await act(async () => { for (let index = 0; index < 30; index++) await Promise.resolve(); });
    await openOptions(user);

    nextMenu = { ...menu, categories: [] };
    await act(async () => { await vi.advanceTimersByTimeAsync(15_000); });
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    nextMenu = menu;
    await act(async () => { await vi.advanceTimersByTimeAsync(15_000); });

    expect(screen.queryByRole('dialog')).toBeNull();
    expect(screen.getByRole('button', { name: 'カートに追加' })).toBeTruthy();
  });

  it('focuses the menu heading when a dismissed option trigger is later removed by menu refresh', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'requestAnimationFrame', 'cancelAnimationFrame', 'Date'] });
    window.history.replaceState({}, '', '/?table=ds2-app');
    let removeProductOnNextMenuRead = false;
    installCartFetch({ getMenu: () => {
      if (removeProductOnNextMenuRead) {
        removeProductOnNextMenuRead = false;
        return { ...menu, categories: [] };
      }
      return menu;
    } });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<App />);
    await act(async () => { for (let index = 0; index < 30; index++) await Promise.resolve(); });
    const trigger = screen.getByRole('button', { name: 'カートに追加' });
    await user.click(trigger);
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: '閉じる' }));
    await act(async () => { await vi.advanceTimersByTimeAsync(64); });
    trigger.focus();
    expect(document.activeElement).toBe(trigger);
    removeProductOnNextMenuRead = true;
    await act(async () => { await vi.advanceTimersByTimeAsync(15_000); });
    await act(async () => { await vi.advanceTimersByTimeAsync(64); });

    const menuHeading = screen.getByRole('heading', { name: 'お品書き' });
    await waitFor(() => expect(document.activeElement).toBe(menuHeading));
    expect(screen.queryByRole('button', { name: 'カートに追加' })).toBeNull();
  });

  it.each(['option', 'cart'] as const)('moves focus to orders when the %s sheet is dismissed by a tab change', async (sheet) => {
    window.history.replaceState({}, '', '/?table=ds2-app');
    installCartFetch();
    const user = userEvent.setup();
    render(<App />);
    if (sheet === 'option') await openOptions(user);
    else await openCartSheet(user);

    fireEvent.click(screen.getByRole('button', { name: /注文履歴/, hidden: true }));
    const ordersHeading = await screen.findByRole('heading', { name: '注文履歴' });
    await waitFor(() => expect(document.activeElement).toBe(ordersHeading));
    expect(screen.queryByRole('dialog')).toBeNull();
    await waitFor(() => expect(document.querySelector('[inert], [aria-hidden="true"]')).toBeNull());
  });

  it.each(['option', 'cart'] as const)('keeps focus inside the %s sheet through locale change and restores its opener', async (sheet) => {
    window.history.replaceState({}, '', '/?table=ds2-app');
    installCartFetch();
    const user = userEvent.setup();
    render(<App />);
    const dialog = sheet === 'option' ? await openOptions(user) : await openCartSheet(user);

    fireEvent.click(screen.getByRole('button', { name: '繁中', hidden: true }));
    await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));
    await user.keyboard('{Escape}');
    const opener = screen.getByRole('button', { name: sheet === 'option' ? '加入購物車' : /^開啟購物車 / });
    await waitFor(() => expect(document.activeElement).toBe(opener));
    expect(screen.queryByRole('dialog')).toBeNull();
    await waitFor(() => expect(document.querySelector('[inert], [aria-hidden="true"]')).toBeNull());
  });

  it.each([409, 422])('keeps the option sheet focused after an add rejection (%i)', async (status) => {
    window.history.replaceState({}, '', '/?table=ds2-app');
    installGuestFetch(() => json({ error: 'rejected' }, status));
    const user = userEvent.setup();
    render(<App />);

    const dialog = await openOptions(user);
    await user.click(within(dialog).getByRole('radio', { name: /普通/ }));
    await user.click(within(dialog).getByRole('button', { name: 'カートに追加' }));
    const alert = await within(dialog).findByRole('alert');
    await waitFor(() => expect(screen.getByRole('dialog')).toBeTruthy());
    expect(alert).toBeTruthy();
    expect(dialog.contains(document.activeElement)).toBe(true);
  });

  it('keeps the option sheet focused when an uncertain replay fails again', async () => {
    window.history.replaceState({}, '', '/?table=ds2-app');
    let addCalls = 0;
    installGuestFetch(() => { addCalls++; return json({ error: 'temporary' }, 503); });
    const user = userEvent.setup();
    render(<App />);

    const dialog = await openOptions(user);
    await user.click(within(dialog).getByRole('radio', { name: /普通/ }));
    await user.click(within(dialog).getByRole('button', { name: 'カートに追加' }));
    const retry = await within(dialog).findByRole('button', { name: '前回の操作を照合して再試行' });
    await user.click(retry);
    await within(dialog).findByRole('alert');

    expect(addCalls).toBe(2);
    expect(screen.getByRole('dialog')).toBeTruthy();
    expect(dialog.contains(document.activeElement)).toBe(true);
  });

  it.each([403, 410])('focuses the entry heading when a menu refresh removes the open product before session expiry (%i)', async (status) => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'requestAnimationFrame', 'cancelAnimationFrame', 'Date'] });
    window.history.replaceState({}, '', '/?table=ds2-app');
    const removedMenu = { ...menu, categories: [] };
    let removeProductOnNextMenuRead = false;
    let productRemoved = false;
    let expireCartReads = false;
    let cartReads = 0;
    installCartFetch({
      getMenu: () => {
        if (removeProductOnNextMenuRead) {
          removeProductOnNextMenuRead = false;
          productRemoved = true;
          return removedMenu;
        }
        return menu;
      },
      onCartRead: () => {
        cartReads++;
        return expireCartReads ? json({ error: 'expired' }, status) : json(emptyCart);
      },
    });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<App />);
    await act(async () => { for (let index = 0; index < 30; index++) await Promise.resolve(); });

    await openOptions(user);
    const entryFocusEvents: Element[] = [];
    const recordEntryFocus = (event: FocusEvent) => {
      if (event.target instanceof HTMLElement && event.target.id === 'entry-title') entryFocusEvents.push(event.target);
    };
    document.addEventListener('focusin', recordEntryFocus);
    removeProductOnNextMenuRead = true;
    await act(async () => { await vi.advanceTimersByTimeAsync(15_000); });

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(productRemoved).toBe(true);
    expect(cartReads).toBeGreaterThan(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(64); });
    const menuHeading = screen.getByRole('heading', { name: 'お品書き' });
    await waitFor(() => expect(document.activeElement).toBe(menuHeading));
    expireCartReads = true;
    await act(async () => { await vi.advanceTimersByTimeAsync(2_500); });
    await act(async () => { await vi.advanceTimersByTimeAsync(64); });
    const entryHeading = await screen.findByRole('heading', { name: 'ご注文を始める' });
    await waitFor(() => expect(document.activeElement).toBe(entryHeading));
    expect(entryFocusEvents).toEqual([entryHeading]);
    await waitFor(() => expect(document.querySelector('[inert], [aria-hidden="true"]')).toBeNull());
    document.removeEventListener('focusin', recordEntryFocus);
  });

  it('focuses entry once if the session expires during the removed product sheet exit', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'requestAnimationFrame', 'cancelAnimationFrame', 'Date'] });
    window.history.replaceState({}, '', '/?table=ds2-app');
    const exit = deferred<void>();
    const original = Object.getOwnPropertyDescriptor(Element.prototype, 'getAnimations');
    Object.defineProperty(Element.prototype, 'getAnimations', { configurable: true, value: function (this: Element) {
      return this.hasAttribute('data-exiting') ? [{ finished: exit.promise } as unknown as Animation] : [];
    } });
    try {
      let removeProduct = false;
      let expire = false;
      installCartFetch({
        getMenu: () => removeProduct ? { ...menu, categories: [] } : menu,
        onCartRead: () => expire ? json({ error: 'expired' }, 410) : json(emptyCart),
      });
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
      render(<App />);
      await act(async () => { for (let index = 0; index < 30; index++) await Promise.resolve(); });
      await openOptions(user);
      removeProduct = true;
      await act(async () => { await vi.advanceTimersByTimeAsync(15_000); });
      expect(screen.getByRole('dialog')).toBeTruthy();
      const focus = vi.spyOn(HTMLElement.prototype, 'focus');
      expire = true;
      await act(async () => { await vi.advanceTimersByTimeAsync(2_500); });
      await act(async () => { exit.resolve(); await exit.promise; await vi.advanceTimersByTimeAsync(64); });

      const entryHeading = screen.getByRole('heading', { name: 'ご注文を始める' });
      expect(document.activeElement).toBe(entryHeading);
      expect(focus.mock.contexts.filter((element) => element === entryHeading)).toHaveLength(1);
      expect(screen.queryByRole('dialog')).toBeNull();
      expect(document.querySelector('[inert], [aria-hidden="true"]')).toBeNull();
    } finally {
      if (original) Object.defineProperty(Element.prototype, 'getAnimations', original);
      else delete (Element.prototype as Partial<Element>).getAnimations;
    }
  });

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
    await user.click(await screen.findByRole('button', { name: /^カートを開く / }));
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
    await user.click(await screen.findByRole('button', { name: /^カートを開く / }));
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
  it('reserves the measured cart bar height for scrolling, page content and feedback', async () => {
    window.history.replaceState({}, '', '/?table=ds2-app');
    installCartFetch({ onRemove: () => json({ error: 'rejected' }, 422) });
    let height = 92;
    let resize!: () => void;
    vi.stubGlobal('ResizeObserver', class {
      constructor(callback: () => void) { resize = callback; }
      observe() {}
      disconnect() {}
    });
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      return { height: this.getAttribute('data-testid') === 'cart-bar' ? height : 0 } as DOMRect;
    });
    const user = userEvent.setup();
    const view = render(<App />);
    await screen.findByRole('button', { name: /^カートを開く / });
    expect(document.documentElement.style.scrollPaddingBottom).toBe('calc(92px + var(--ref-space-4))');
    expect(screen.getByRole('main').style.paddingBottom).toBe('calc(92px + var(--ref-space-4))');
    await act(async () => { height = 136; resize(); });
    expect(document.documentElement.style.scrollPaddingBottom).toBe('calc(136px + var(--ref-space-4))');
    const dialog = await openCartSheet(user);
    await user.click(within(dialog).getByRole('button', { name: '削除' }));
    await within(dialog).findByRole('alert');
    within(dialog).getByRole('button', { name: '閉じる' }).focus();
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(screen.getByRole('alert').parentElement?.style.bottom).toBe('calc(136px + var(--ref-space-2))');
    view.unmount();
    expect(document.documentElement.style.scrollPaddingBottom).toBe('');
  });

  it.each(['option', 'cart', 'preview'] as const)('focuses the entry heading once when a cart poll expires the session with the %s sheet open', async (sheet) => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'requestAnimationFrame', 'cancelAnimationFrame', 'Date'] });
    window.history.replaceState({}, '', '/?table=ds2-app');
    let expire = false;
    installCartFetch({ onCartRead: () => expire ? json({ error: 'expired' }, 403) : json(cartWithMeal) });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<App />);
    await act(async () => { for (let index = 0; index < 30; index++) await Promise.resolve(); });

    if (sheet === 'option') await openOptions(user);
    else {
      const dialog = await openCartSheet(user);
      if (sheet === 'preview') await user.click(within(dialog).getByRole('button', { name: '金額を確認する' }));
    }
    const entryFocusEvents: Element[] = [];
    const inertFocus = blockFocusBehindModal();
    const inertHold = sheet === 'cart' ? holdGuestHeadingInert('ご注文を始める') : null;
    const recordEntryFocus = (event: FocusEvent) => {
      if (event.target instanceof HTMLElement && event.target.id === 'entry-title') entryFocusEvents.push(event.target);
    };
    document.addEventListener('focusin', recordEntryFocus);
    expire = true;
    await act(async () => { await vi.advanceTimersByTimeAsync(2_500); });
    const entryHeading = await screen.findByRole('heading', { name: 'ご注文を始める' });
    if (inertHold) {
      await inertHold.ready;
      await act(async () => { await vi.advanceTimersByTimeAsync(40); });
      expect(document.activeElement).not.toBe(entryHeading);
      expect(inertFocus.focus.mock.contexts.filter((element) => element === entryHeading)).toHaveLength(0);
      inertHold.release();
    }
    await act(async () => { await vi.advanceTimersByTimeAsync(64); });

    await waitFor(() => expect(document.activeElement).toBe(entryHeading));
    expect(entryFocusEvents).toEqual([entryHeading]);
    expect(inertFocus.blocked).toEqual([]);
    expect(inertFocus.focus.mock.contexts.filter((element) => element === entryHeading)).toHaveLength(1);
    expect(inertFocus.blocked).toEqual([]);
    expect(inertFocus.focus.mock.contexts.filter((element) => element === entryHeading)).toHaveLength(1);
    expect(screen.queryByRole('dialog')).toBeNull();
    await waitFor(() => expect(document.querySelector('[inert], [aria-hidden="true"]')).toBeNull());
    document.removeEventListener('focusin', recordEntryFocus);
  });

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
    const inertFocus = blockFocusBehindModal();
    render(<App />);

    const dialog = await openCartSheet(user);
    expect(within(dialog).queryByRole('note')).toBeNull();
    await user.click(within(dialog).getByRole('button', { name: '削除' }));
    expect(await within(screen.getByRole('dialog')).findByText('カートはまだ空です。')).toBeTruthy();
    const inertHold = holdGuestHeadingInert('お品書き');
    await user.keyboard('{Escape}');

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    const menuHeading = screen.getByRole('heading', { name: 'お品書き' });
    await inertHold.ready;
    await act(async () => { await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))); });
    expect(document.activeElement).not.toBe(menuHeading);
    expect(inertFocus.focus.mock.contexts.filter((element) => element === menuHeading)).toHaveLength(0);
    inertHold.release();
    await waitFor(() => expect(document.activeElement).toBe(menuHeading));
    expect(inertFocus.blocked).toEqual([]);
    expect(inertFocus.focus.mock.contexts.filter((element) => element === menuHeading)).toHaveLength(1);
    await waitFor(() => expect(document.querySelector('[inert], [aria-hidden="true"]')).toBeNull());
  });

  it('keeps focus in the cart sheet when another guest empties it, then closes to the menu heading', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'requestAnimationFrame', 'cancelAnimationFrame', 'Date'] });
    window.history.replaceState({}, '', '/?table=ds2-app');
    let pollEnabled = false;
    installCartFetch({ initialCart: () => pollEnabled ? { ...emptyCart, version: 1 } : cartWithMeal });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<App />);
    await act(async () => { for (let index = 0; index < 30; index++) await Promise.resolve(); });

    const dialog = await openCartSheet(user);
    await user.click(within(dialog).getByRole('button', { name: '金額を確認する' }));
    const cartHeading = await screen.findByRole('heading', { name: '金額を確認する' });
    await waitFor(() => expect(document.activeElement).toBe(cartHeading));
    pollEnabled = true;
    await act(async () => { await vi.advanceTimersByTimeAsync(2_500); });

    const sharedCartHeading = screen.getByRole('heading', { name: '共有カート' });
    await waitFor(() => expect(document.activeElement).toBe(sharedCartHeading));
    expect(screen.getByRole('dialog')).toBeTruthy();
    expect(within(screen.getByRole('dialog')).getByText('カートはまだ空です。')).toBeTruthy();
    await user.keyboard('{Escape}');

    await act(async () => { await vi.advanceTimersByTimeAsync(64); });
    const menuHeading = screen.getByRole('heading', { name: 'お品書き' });
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(menuHeading));
    await waitFor(() => expect(document.querySelector('[inert], [aria-hidden="true"]')).toBeNull());
  });

  it('moves focus to the cart heading when a menu refresh removes the focused cart row control', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'requestAnimationFrame', 'cancelAnimationFrame', 'Date'] });
    window.history.replaceState({}, '', '/?table=ds2-app');
    let removeProductOnNextMenuRead = false;
    installCartFetch({
      getMenu: () => {
        if (removeProductOnNextMenuRead) {
          removeProductOnNextMenuRead = false;
          return { ...menu, categories: [] };
        }
        return menu;
      },
      initialCart: cartWithMeal,
    });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<App />);
    await act(async () => { for (let index = 0; index < 30; index++) await Promise.resolve(); });

    const dialog = await openCartSheet(user);
    const focusedOption = within(dialog).getByRole('radio', { name: /普通/ });
    focusedOption.focus();
    expect(document.activeElement).toBe(focusedOption);
    removeProductOnNextMenuRead = true;
    await act(async () => { await vi.advanceTimersByTimeAsync(15_000); });

    const cartHeading = screen.getByRole('heading', { name: '共有カート' });
    await waitFor(() => expect(document.activeElement).toBe(cartHeading));
    expect(screen.getByRole('dialog')).toBeTruthy();
    await user.keyboard('{Escape}');
    await act(async () => { await vi.advanceTimersByTimeAsync(64); });
    const cartBar = screen.getByRole('button', { name: /^カートを開く / });
    await waitFor(() => expect(document.activeElement).toBe(cartBar));
    await waitFor(() => expect(document.querySelector('[inert], [aria-hidden="true"]')).toBeNull());
  });

  it.each([409, 422])('keeps the cart sheet focused after a remove rejection (%i)', async (status) => {
    window.history.replaceState({}, '', '/?table=ds2-app');
    installCartFetch({ onRemove: () => json({ error: 'rejected' }, status) });
    const user = userEvent.setup();
    render(<App />);

    const dialog = await openCartSheet(user);
    await user.click(within(dialog).getByRole('button', { name: '削除' }));
    const alert = await within(dialog).findByRole('alert');
    expect(screen.getByRole('dialog')).toBeTruthy();
    expect(alert).toBeTruthy();
    expect(dialog.contains(document.activeElement)).toBe(true);
  });

  it('keeps the cart sheet focused when an uncertain remove replay fails again', async () => {
    window.history.replaceState({}, '', '/?table=ds2-app');
    let removeCalls = 0;
    installCartFetch({ onRemove: () => { removeCalls++; return json({ error: 'temporary' }, 503); } });
    const user = userEvent.setup();
    render(<App />);

    const dialog = await openCartSheet(user);
    await user.click(within(dialog).getByRole('button', { name: '削除' }));
    const retry = await within(dialog).findByRole('button', { name: '前回の操作を照合して再試行' });
    await user.click(retry);
    await within(dialog).findByRole('alert');

    expect(removeCalls).toBe(2);
    expect(screen.getByRole('dialog')).toBeTruthy();
    expect(dialog.contains(document.activeElement)).toBe(true);
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

    const cartBar = await screen.findByRole('button', { name: /^カートを開く / });
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
    const inertFocus = blockFocusBehindModal();
    const inertHold = holdGuestHeadingInert('注文履歴');
    render(<App demo />);

    await openCartSheet(user);
    await user.click(screen.getByRole('button', { name: '金額を確認する' }));
    await screen.findByRole('heading', { name: '金額を確認する' });
    await user.click(screen.getByRole('button', { name: 'この内容で注文を確定' }));

    const ordersHeading = await screen.findByRole('heading', { name: '注文履歴' });
    await inertHold.ready;
    await act(async () => { await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))); });
    expect(document.activeElement).not.toBe(ordersHeading);
    expect(inertFocus.focus.mock.contexts.filter((element) => element === ordersHeading)).toHaveLength(0);
    inertHold.release();
    await waitFor(() => expect(document.activeElement).toBe(ordersHeading));
    expect(screen.queryByRole('button', { name: /^カートを開く / })).toBeNull();
    expect(screen.queryByRole('dialog')).toBeNull();
    await waitFor(() => expect(document.querySelector('[inert], [aria-hidden="true"]')).toBeNull());
    expect(inertFocus.blocked).toEqual([]);
    expect(inertFocus.focus.mock.contexts.filter((element) => element === ordersHeading)).toHaveLength(1);
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
    const focus = vi.spyOn(HTMLElement.prototype, 'focus');
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
    expect(screen.queryByRole('button', { name: /^カートを開く / })).toBeNull();
    expect(screen.queryByRole('dialog')).toBeNull();
    await waitFor(() => expect(document.querySelector('[inert], [aria-hidden="true"]')).toBeNull());
    expect(focus.mock.contexts.filter((element) => element === ordersHeading)).toHaveLength(1);
  });

  it('focuses orders after keyboard replay from page feedback when the confirmation sheet was dismissed', async () => {
    window.history.replaceState({}, '', '/?table=ds2-app');
    const result = {
      outcome: 'COMMITTED',
      order: { order_id: 'order-page-replay', session_id: 'session-1', generation: 1, payment_method: 'CASH', quote: previewForMeal.quote, created_at: '2026-10-20T00:00:00Z' },
      cart: { ...emptyCart, version: 1 },
    };
    const replayResponse = deferred<Response>();
    let commitCount = 0;
    const { calls } = installCartFetch({ onCommit: () => ++commitCount === 1 ? json({ error: 'temporary' }, 503) : replayResponse.promise });
    const user = userEvent.setup();
    render(<App demo />);

    await openCartSheet(user);
    await user.click(screen.getByRole('button', { name: '金額を確認する' }));
    await screen.findByRole('heading', { name: '金額を確認する' });
    await user.click(screen.getByRole('button', { name: 'この内容で注文を確定' }));
    const dialog = screen.getByRole('dialog');
    await within(dialog).findByRole('button', { name: '前回の操作を照合して再試行' });
    within(dialog).getByRole('button', { name: '閉じる' }).focus();
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: /^カートを開く / })));

    const ordersFocusEvents: Element[] = [];
    const trackOrdersFocus = (event: FocusEvent) => {
      const target = event.target;
      if (target instanceof HTMLElement && target === screen.queryByRole('heading', { name: '注文履歴' })) ordersFocusEvents.push(target);
    };
    document.addEventListener('focusin', trackOrdersFocus);
    const pageRetry = screen.getByRole('button', { name: '前回の操作を照合して再試行' });
    pageRetry.focus();
    await user.keyboard('{Enter}');
    await waitFor(() => expect(commitCount).toBe(2));
    await act(async () => { replayResponse.resolve(json(result)); await Promise.resolve(); });

    const ordersHeading = await screen.findByRole('heading', { name: '注文履歴' });
    await waitFor(() => expect(document.activeElement).toBe(ordersHeading));
    expect(ordersFocusEvents).toEqual([ordersHeading]);
    document.removeEventListener('focusin', trackOrdersFocus);
    await waitFor(() => expect(document.querySelector('[inert], [aria-hidden="true"]')).toBeNull());
    const commits = calls.filter(({ url, init }) => init.method === 'POST' && url.endsWith('/cart/commit'));
    expect(commits).toHaveLength(2);
    expectSameOperation(commits[0]!.init, commits[1]!.init);
  });

  it('focuses the entry heading when a page-level replay expires the session after the sheet closes', async () => {
    window.history.replaceState({}, '', '/?table=ds2-app');
    let addCount = 0;
    const { calls } = installGuestFetch(() => ++addCount === 1 ? json({ error: 'temporary' }, 503) : json({ error: 'expired' }, 403));
    const user = userEvent.setup();
    render(<App />);

    const dialog = await openOptions(user);
    await user.click(within(dialog).getByRole('radio', { name: /普通/ }));
    await user.click(within(dialog).getByRole('button', { name: 'カートに追加' }));
    await within(screen.getByRole('dialog')).findByRole('button', { name: '前回の操作を照合して再試行' });
    within(screen.getByRole('dialog')).getByRole('button', { name: '閉じる' }).focus();
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('heading', { name: 'お品書き' })));

    const pageRetry = screen.getByRole('button', { name: '前回の操作を照合して再試行' });
    pageRetry.focus();
    await user.keyboard('{Enter}');
    const entryHeading = await screen.findByRole('heading', { name: 'ご注文を始める' });
    await waitFor(() => expect(document.activeElement).toBe(entryHeading));
    await waitFor(() => expect(document.querySelector('[inert], [aria-hidden="true"]')).toBeNull());
    const adds = calls.filter(({ url, init }) => init.method === 'POST' && url.endsWith('/cart/lines'));
    expect(adds).toHaveLength(2);
    expectSameOperation(adds[0]!.init, adds[1]!.init);
  });

  it('focuses the menu when a page-level remove replay empties the cart after the sheet closes', async () => {
    window.history.replaceState({}, '', '/?table=ds2-app');
    const replayResponse = deferred<Response>();
    const emptiedCart = { ...emptyCart, version: 1 };
    let removeCount = 0;
    const { calls } = installCartFetch({
      onRemove: () => ++removeCount === 1 ? json({ error: 'temporary' }, 503) : replayResponse.promise,
      onCartRead: () => removeCount > 1 ? json(emptiedCart) : json(cartWithMeal),
    });
    const user = userEvent.setup();
    render(<App />);

    const dialog = await openCartSheet(user);
    await user.click(within(dialog).getByRole('button', { name: '削除' }));
    await within(screen.getByRole('dialog')).findByRole('button', { name: '前回の操作を照合して再試行' });
    within(screen.getByRole('dialog')).getByRole('button', { name: '閉じる' }).focus();
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: /^カートを開く / })));

    const pageRetry = screen.getByRole('button', { name: '前回の操作を照合して再試行' });
    pageRetry.focus();
    await user.keyboard('{Enter}');
    await waitFor(() => expect(removeCount).toBe(2));
    await act(async () => { replayResponse.resolve(json(emptiedCart)); await Promise.resolve(); });
    const menuHeading = await screen.findByRole('heading', { name: 'お品書き' });
    await waitFor(() => expect(document.activeElement).toBe(menuHeading));
    await waitFor(() => expect(screen.queryByRole('button', { name: '前回の操作を照合して再試行' })).toBeNull());
    const removes = calls.filter(({ url, init }) => init.method === 'DELETE' && url.includes('/cart/lines/'));
    expect(removes).toHaveLength(2);
    expectSameOperation(removes[0]!.init, removes[1]!.init);
  });

  it('keeps focus on a connected page control if it moves during an empty-cart replay', async () => {
    window.history.replaceState({}, '', '/?table=ds2-app');
    const replayResponse = deferred<Response>();
    const emptiedCart = { ...emptyCart, version: 1 };
    let removeCount = 0;
    installCartFetch({
      onRemove: () => ++removeCount === 1 ? json({ error: 'temporary' }, 503) : replayResponse.promise,
      onCartRead: () => removeCount > 1 ? json(emptiedCart) : json(cartWithMeal),
    });
    const user = userEvent.setup();
    render(<App />);

    const dialog = await openCartSheet(user);
    await user.click(within(dialog).getByRole('button', { name: '削除' }));
    await within(screen.getByRole('dialog')).findByRole('button', { name: '前回の操作を照合して再試行' });
    within(screen.getByRole('dialog')).getByRole('button', { name: '閉じる' }).focus();
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: /^カートを開く / })));

    const pageRetry = screen.getByRole('button', { name: '前回の操作を照合して再試行' });
    pageRetry.focus();
    await user.keyboard('{Enter}');
    await waitFor(() => expect(removeCount).toBe(2));
    const languageButton = screen.getByRole('button', { name: '繁中' });
    languageButton.focus();
    await act(async () => { replayResponse.resolve(json(emptiedCart)); await Promise.resolve(); });

    await waitFor(() => expect(screen.queryByRole('button', { name: '前回の操作を照合して再試行' })).toBeNull());
    expect(document.activeElement).toBe(languageButton);
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
