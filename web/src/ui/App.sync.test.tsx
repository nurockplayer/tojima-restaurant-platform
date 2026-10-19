// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { App } from './App';

const table = { mode: 'DIRECT_SCAN', current_session_id: 'session-sync', open: true };
const grant = { table_join_token: 'grant-sync', expires_at: '2030-01-01T00:00:00Z', session: { id: 'session-sync', mode: 'DIRECT_SCAN' } };
const menu = {
  currency: 'JPY', price_mode: 'TAX_INCLUSIVE',
  categories: [{ key: 'food', name: 'お食事', position: 1, products: [{
    key: 'meal', name: '定食', description: '日替わりの定食です', effective_price_minor: '960', position: 1,
    availability: 'AVAILABLE', selectable: true, option_groups: [],
  }] }],
};
const emptyCart = { session_id: 'session-sync', generation: 1, version: 0, lines: [] };
const cartWithMeal = { ...emptyCart, version: 1, lines: [{ line_id: 'line-sync', product_key: 'meal', product_name: '定食', product_available: true, quantity: 1, options: [] }] };
const cartAfterRemove = { ...emptyCart, version: 2 };
const preview = {
  session_id: 'session-sync', generation: 1, version: 1, fingerprint: 'sync-preview',
  quote: { lines: [{ line_id: 'line-sync', product_key: 'meal', name: '定食', base_gross_jpy: '960', quantity: 1, options: [] }],
    tax: { currency: 'JPY', price_mode: 'INCLUSIVE', total_gross_jpy: '960', total_net_jpy: '873', total_tax_jpy: '87', buckets: [] } },
};

type Handler = (url: string, init: RequestInit) => Response | Promise<Response>;

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

function useFakeClock() {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'requestAnimationFrame', 'cancelAnimationFrame', 'Date'] });
  vi.setSystemTime(new Date('2026-01-01T00:00:00.000Z'));
}

async function advanceClock(milliseconds: number) {
  await act(async () => {
    vi.advanceTimersByTime(milliseconds);
    for (let index = 0; index < 30; index++) await Promise.resolve();
  });
}

function installFetch(options: {
  cart?: () => Response | Promise<Response>;
  add?: Handler;
  preview?: Handler;
  remove?: Handler;
} = {}) {
  const calls: Array<{ url: string; init: RequestInit; at: number }> = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    calls.push({ url, init, at: Date.now() });
    if (url.endsWith('/v1/guest/tables/ds2-sync')) return json(table);
    if (url.endsWith('/v1/guest/tables/ds2-sync/join')) return json(grant);
    if (url.endsWith('/v1/guest/tables/ds2-sync/menu?amounts=string')) return json(menu);
    if (url.endsWith('/v1/guest/sessions/session-sync/cart/lines') && init.method === 'POST') return options.add ? options.add(url, init) : json(cartWithMeal);
    if (url.endsWith('/v1/guest/sessions/session-sync/cart/preview')) return options.preview ? options.preview(url, init) : json(preview);
    if (url.includes('/v1/guest/sessions/session-sync/cart/lines/') && init.method === 'DELETE') return options.remove ? options.remove(url, init) : json(emptyCart);
    if (url.endsWith('/v1/guest/sessions/session-sync/cart')) return options.cart ? options.cart() : json(cartWithMeal);
    if (url.endsWith('/v1/guest/sessions/session-sync/orders')) return json({ orders: [] });
    if (url.endsWith('/v1/guest/sessions/session-sync/operations')) return json({ orders: [], cash_enabled: true });
    throw new Error(`Unexpected API request: ${url}`);
  });
  vi.stubGlobal('fetch', fetchMock);
  return { calls, fetchMock };
}

function startPage() {
  window.history.replaceState({}, '', '/?table=ds2-sync');
  render(<App />);
}

async function flushStartup() {
  await screen.findByRole('button', { name: /^カートを開く / });
  await waitFor(() => expect(screen.getByText(/^テーブルで共有中/)).toBeTruthy());
}

async function openCart(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole('button', { name: /^カートを開く / }));
  return screen.getByRole('dialog');
}

afterEach(async () => {
  cleanup();
  if (vi.isFakeTimers()) await vi.advanceTimersByTimeAsync(64);
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  vi.useRealTimers();
  Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' });
  sessionStorage.clear();
  window.history.replaceState({}, '', '/');
});

describe('guest App sync acceptance', () => {
  it('shows the exact degraded notice in the page and sheet, keeps the cart, and only clears after an accepted read', async () => {
    installFetch();
    const user = userEvent.setup();
    startPage();
    await flushStartup();
    const dialog = await openCart(user);

    await act(async () => { window.dispatchEvent(new Event('offline')); });
    const expected = /表示は .* 時点の内容です。再接続まで注文の確定はできません。カートは保持されます。/;
    await waitFor(() => expect(document.body.textContent?.match(/通信が不安定です/g)).toHaveLength(2));
    expect(document.body.textContent).toMatch(expected);
    expect(within(dialog).getByText('定食')).toBeTruthy();
    expect(screen.getByText(/同期待ち・\d{2}:\d{2} 時点/)).toBeTruthy();

    await act(async () => { window.dispatchEvent(new Event('online')); });
    expect(document.body.textContent).toMatch(expected);
    await user.click(within(dialog).getByRole('button', { name: '共有内容を更新' }));
    await waitFor(() => expect(screen.getByText(/^テーブルで共有中/)).toBeTruthy());
    expect(document.body.textContent).not.toContain('通信が不安定です');
    expect(within(screen.getByRole('dialog')).getByText('定食')).toBeTruthy();
  });

  it('disables preview and confirmation with the degraded reason while retaining lines', async () => {
    installFetch();
    const user = userEvent.setup();
    startPage();
    await flushStartup();
    let dialog = await openCart(user);
    await user.click(within(dialog).getByRole('button', { name: '金額を確認する' }));
    dialog = screen.getByRole('dialog');
    const confirm = within(dialog).getByRole('button', { name: 'この内容で注文を確定' }) as HTMLButtonElement;

    await act(async () => { window.dispatchEvent(new Event('offline')); });
    await waitFor(() => expect(confirm.disabled).toBe(true));
    const reason = within(dialog).getByText(/表示は .* 時点の内容です。再接続まで注文の確定はできません。カートは保持されます。/);
    expect(confirm.getAttribute('aria-describedby')).toBe(reason.id);
    expect(within(dialog).getByText('定食')).toBeTruthy();
    await user.click(within(dialog).getByRole('button', { name: 'カートへ戻る' }));
    dialog = screen.getByRole('dialog');
    const previewButton = within(dialog).getByRole('button', { name: '金額を確認する' }) as HTMLButtonElement;
    expect(previewButton.disabled).toBe(true);
    expect(previewButton.getAttribute('aria-describedby')).toBeTruthy();
    expect(within(dialog).getByText('定食')).toBeTruthy();
  });

  it('keeps uncertain replay available while degraded and accepts its same operation response', async () => {
    let removeCount = 0;
    let accepted = false;
    const { calls } = installFetch({
      cart: () => json(accepted ? cartAfterRemove : cartWithMeal),
      remove: () => {
        removeCount++;
        if (removeCount === 1) return json({ error: 'temporary' }, 503);
        accepted = true;
        return json(cartAfterRemove);
      },
    });
    const user = userEvent.setup();
    startPage();
    await flushStartup();
    const dialog = await openCart(user);
    await user.click(within(dialog).getByRole('button', { name: '削除' }));
    const retry = await within(dialog).findByRole('button', { name: '前回の操作を照合して再試行' });
    await act(async () => { window.dispatchEvent(new Event('offline')); });
    expect((retry as HTMLButtonElement).disabled).toBe(false);
    await user.click(retry);
    await waitFor(() => expect(removeCount).toBe(2));
    const requests = calls.filter(({ url, init }) => init.method === 'DELETE' && url.includes('/cart/lines/line-sync'));
    expect(requests).toHaveLength(2);
    const key = new Headers(requests[0]!.init.headers).get('Idempotency-Key');
    expect(key).toBeTruthy();
    expect(new Headers(requests[1]!.init.headers).get('Idempotency-Key')).toBe(key);
    expect(requests[1]!.init.body).toBe(requests[0]!.init.body);
    expect(await within(dialog).findByText('カートはまだ空です。')).toBeTruthy();
    expect(document.body.textContent).not.toContain('通信が不安定です');
  });

  it('uses an exact 7.5 second watchdog without changing the 2.5 second cart poll cadence', async () => {
    useFakeClock();
    let cartReads = 0;
    let hangs = false;
    installFetch({ cart: () => { cartReads++; return hangs ? new Promise<Response>(() => undefined) : json(cartWithMeal); } });
    startPage();
    await act(async () => { for (let index = 0; index < 40; index++) await Promise.resolve(); });
    expect(screen.getByText(/^テーブルで共有中/)).toBeTruthy();
    const baselineReads = cartReads;
    hangs = true;
    await advanceClock(2_500);
    expect(cartReads).toBe(baselineReads + 1);
    await advanceClock(2_500);
    expect(cartReads).toBe(baselineReads + 2);
    expect(document.body.textContent).not.toContain('通信が不安定です');
    await advanceClock(2_499);
    expect(document.body.textContent).not.toContain('通信が不安定です');
    await advanceClock(1);
    expect(document.body.textContent).toContain('通信が不安定です');
  });

  it.each(['late success', 'late failure'] as const)('ignores a %s from an older read after a newer snapshot', async (lateKind) => {
    useFakeClock();
    let cartReads = 0;
    let controlled = false;
    let controlledReads = 0;
    const stale = deferred<Response>();
    const newest = { ...cartWithMeal, version: 2, lines: [{ ...cartWithMeal.lines[0]!, product_name: '新しい共有内容' }] };
    installFetch({ cart: () => {
      cartReads++;
      if (!controlled) return json(cartWithMeal);
      if (++controlledReads === 1) return stale.promise;
      return json(newest);
    } });
    startPage();
    await act(async () => { for (let index = 0; index < 40; index++) await Promise.resolve(); });
    expect(screen.getByText(/^テーブルで共有中/)).toBeTruthy();
    const baselineReads = cartReads;
    controlled = true;
    vi.setSystemTime(new Date('2026-01-01T00:01:00.000Z'));
    await advanceClock(5_000);
    expect(cartReads).toBeGreaterThanOrEqual(baselineReads + 2);
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: /^カートを開く / })); });
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).getByText('新しい共有内容')).toBeTruthy();

    vi.setSystemTime(new Date('2026-01-01T00:02:00.000Z'));
    await act(async () => { stale.resolve(lateKind === 'late failure' ? json({ error: 'late' }, 503) : json({ ...cartWithMeal, version: 99, lines: [{ ...cartWithMeal.lines[0]!, product_name: '古い応答' }] })); });
    await act(async () => { for (let index = 0; index < 20; index++) await Promise.resolve(); });
    expect(within(dialog).getByText('新しい共有内容')).toBeTruthy();
    expect(document.body.textContent).not.toContain('通信が不安定です');
    expect(document.body.textContent).toContain('09:01');
    if (lateKind === 'late failure') {
      Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' });
      await act(async () => { document.dispatchEvent(new Event('visibilitychange')); });
      await advanceClock(8_000);
      expect(document.body.textContent).not.toContain('通信が不安定です');
    }
  });

  it('does not let a low-version cart read after a mutation replace the cart or sync time', async () => {
    useFakeClock();
    let cartReads = 0;
    let polling = false;
    const oldPoll = deferred<Response>();
    const accepted = { ...cartWithMeal, version: 2, lines: [{ ...cartWithMeal.lines[0]!, product_name: '定食' }] };
    const lowVersion = { ...cartWithMeal, version: 1, lines: [{ ...cartWithMeal.lines[0]!, line_id: 'old-line', product_name: '古い共有品' }] };
    installFetch({
      cart: () => { cartReads++; return polling ? oldPoll.promise : json(emptyCart); },
      add: () => json(accepted),
    });
    startPage();
    await act(async () => { for (let index = 0; index < 40; index++) await Promise.resolve(); });
    polling = true;
    const baselineReads = cartReads;
    await advanceClock(2_500);
    expect(cartReads).toBe(baselineReads + 1);
    vi.setSystemTime(new Date('2026-01-01T00:01:00.000Z'));
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'カートに追加' })); for (let index = 0; index < 40; index++) await Promise.resolve(); });
    expect(screen.getByRole('button', { name: /^カートを開く / })).toBeTruthy();
    vi.setSystemTime(new Date('2026-01-01T00:02:00.000Z'));
    await act(async () => { oldPoll.resolve(json(lowVersion)); });
    await act(async () => { for (let index = 0; index < 30; index++) await Promise.resolve(); });
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: /^カートを開く / })); });
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).getByText('定食')).toBeTruthy();
    expect(within(dialog).queryByText('古い共有品')).toBeNull();
    expect(document.body.textContent).toContain('09:01');
  });

  it('ignores an old read failure after an accepted mutation and one later failure stays healthy', async () => {
    useFakeClock();
    let polling = false;
    let controlledReads = 0;
    const oldPoll = deferred<Response>();
    const accepted = { ...cartWithMeal, version: 2 };
    installFetch({
      cart: () => {
        if (!polling) return json(emptyCart);
        if (++controlledReads === 1) return oldPoll.promise;
        if (controlledReads === 2) return json({ error: 'one failure' }, 503);
        return new Promise<Response>(() => undefined);
      },
      add: () => json(accepted),
    });
    startPage();
    await act(async () => { for (let index = 0; index < 40; index++) await Promise.resolve(); });
    polling = true;
    await advanceClock(2_500);
    await advanceClock(500);
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'カートに追加' })); for (let index = 0; index < 40; index++) await Promise.resolve(); });
    expect(screen.getByRole('button', { name: /^カートを開く / })).toBeTruthy();
    await advanceClock(500);
    await act(async () => { oldPoll.resolve(json({ error: 'old failure' }, 503)); });
    await act(async () => { for (let index = 0; index < 30; index++) await Promise.resolve(); });
    expect(document.body.textContent).not.toContain('通信が不安定です');
    expect(document.body.textContent).toContain('09:00');

    await advanceClock(1_500);
    expect(controlledReads).toBe(2);
    expect(document.body.textContent).not.toContain('通信が不安定です');
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' });
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')); });
    await advanceClock(8_000);
    expect(document.body.textContent).not.toContain('通信が不安定です');
  });

  it('reaches degraded at the watchdog when overlapping cart reads each fail after three seconds', async () => {
    useFakeClock();
    let cartReads = 0;
    let slow = false;
    installFetch({ cart: () => {
      if (++cartReads === 1) return json(cartWithMeal);
      if (!slow) return json(cartWithMeal);
      return new Promise<Response>((resolve) => { setTimeout(() => resolve(json({ error: 'slow failure' }, 503)), 3_000); });
    } });
    startPage();
    await act(async () => { for (let index = 0; index < 40; index++) await Promise.resolve(); });
    expect(screen.getByText(/^テーブルで共有中/)).toBeTruthy();
    const baselineReads = cartReads;
    slow = true;
    await advanceClock(7_499);
    expect(document.body.textContent).not.toContain('通信が不安定です');
    await advanceClock(1);
    expect(cartReads).toBeGreaterThan(baselineReads);
    expect(document.body.textContent).toContain('通信が不安定です');
  });

  it('stays healthy when overlapping cart reads each succeed after three seconds with the displayed version', async () => {
    useFakeClock();
    let cartReads = 0;
    let slow = false;
    const { calls } = installFetch({ cart: () => {
      cartReads++;
      if (!slow) return json(cartWithMeal);
      return new Promise<Response>((resolve) => { setTimeout(() => resolve(json(cartWithMeal)), 3_000); });
    } });
    startPage();
    await act(async () => { for (let index = 0; index < 40; index++) await Promise.resolve(); });
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: /^カートを開く / })); });
    const dialog = screen.getByRole('dialog');
    const baselineReads = cartReads;
    const pollStart = Date.now();
    slow = true;

    for (let elapsed = 500; elapsed <= 12_000; elapsed += 500) {
      await advanceClock(500);
      expect(document.body.textContent).not.toContain('通信が不安定です');
      expect(cartReads).toBe(baselineReads + Math.floor(elapsed / 2_500));
    }
    expect(within(dialog).getAllByText('定食')).toHaveLength(1);
    expect((within(dialog).getByRole('button', { name: '金額を確認する' }) as HTMLButtonElement).disabled).toBe(false);
    expect(calls.filter(({ url, at }) => url.endsWith('/cart') && at > pollStart).map(({ at }) => at - pollStart)).toEqual([2_500, 5_000, 7_500, 10_000]);
  });

  it('does not record success from a superseded read whose version differs from the displayed cart', async () => {
    useFakeClock();
    let cartReads = 0;
    let slow = false;
    const differentCart = { ...cartWithMeal, version: cartWithMeal.version + 1, lines: [{ ...cartWithMeal.lines[0]!, product_name: '異なる共有内容', quantity: 2 }] };
    installFetch({ cart: () => {
      cartReads++;
      if (!slow) return json(cartWithMeal);
      return new Promise<Response>((resolve) => { setTimeout(() => resolve(json(differentCart)), 3_000); });
    } });
    startPage();
    await act(async () => { for (let index = 0; index < 40; index++) await Promise.resolve(); });
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: /^カートを開く / })); });
    const dialog = screen.getByRole('dialog');
    const baselineReads = cartReads;
    slow = true;

    await advanceClock(7_499);
    expect(document.body.textContent).not.toContain('通信が不安定です');
    await advanceClock(1);
    expect(cartReads).toBe(baselineReads + 3);
    expect(document.body.textContent).toContain('通信が不安定です');
    expect(within(dialog).getAllByText('定食')).toHaveLength(1);
    expect(within(dialog).queryByText('異なる共有内容')).toBeNull();
    expect((within(dialog).getByRole('button', { name: '金額を確認する' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('restarts the exact watchdog from an accepted success between schedules', async () => {
    useFakeClock();
    let cartReads = 0;
    let hangs = false;
    installFetch({ cart: () => { cartReads++; return hangs ? new Promise<Response>(() => undefined) : json(cartWithMeal); } });
    startPage();
    await act(async () => { for (let index = 0; index < 40; index++) await Promise.resolve(); });
    const baselineReads = cartReads;
    await advanceClock(2_500);
    expect(cartReads).toBe(baselineReads + 1);
    hangs = true;

    await advanceClock(7_499);
    expect(document.body.textContent).not.toContain('通信が不安定です');
    await advanceClock(1);
    expect(document.body.textContent).toContain('通信が不安定です');
  });

  it('suspends the watchdog while hidden or in orders and restarts it on visible return', async () => {
    useFakeClock();
    let hangs = false;
    installFetch({ cart: () => hangs ? new Promise<Response>(() => undefined) : json(cartWithMeal) });
    startPage();
    await act(async () => { for (let index = 0; index < 40; index++) await Promise.resolve(); });
    hangs = true;
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: /注文履歴/ })); });
    expect(screen.queryByRole('button', { name: /^カートを開く / })).toBeNull();
    await advanceClock(8_000);
    expect(document.body.textContent).not.toContain('通信が不安定です');

    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'メニューに戻る' })); });
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' });
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')); });
    await advanceClock(8_000);
    expect(document.body.textContent).not.toContain('通信が不安定です');
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' });
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')); });
    await advanceClock(7_499);
    expect(document.body.textContent).not.toContain('通信が不安定です');
    await advanceClock(1);
    expect(document.body.textContent).toContain('通信が不安定です');
  });

  it('starts the watchdog while the initial cart read is unresolved', async () => {
    useFakeClock();
    installFetch({ cart: () => new Promise<Response>(() => undefined) });
    startPage();
    await act(async () => { for (let index = 0; index < 80; index++) await Promise.resolve(); });
    await advanceClock(7_500);
    expect(document.body.textContent).toContain('通信が不安定です');
    expect(screen.getByText(/表示は .* 時点の内容です/)).toBeTruthy();
  });

  it.each(['network', '503'] as const)('degrades after exactly two latest %s cart-read failures', async (failure) => {
    useFakeClock();
    let failing = false;
    const { calls } = installFetch({ cart: () => {
      if (!failing) return json(cartWithMeal);
      if (failure === 'network') throw new Error('disconnected');
      return json({ error: 'temporary' }, 503);
    } });
    startPage();
    await advanceClock(0);
    failing = true;
    await advanceClock(2_500);
    expect(document.body.textContent).not.toContain('通信が不安定です');
    await advanceClock(2_500);
    expect(document.body.textContent).toContain('通信が不安定です');
    expect(calls.filter(({ url, at }) => url.endsWith('/cart') && at > Date.UTC(2026, 0, 1)).map(({ at }) => at - Date.UTC(2026, 0, 1))).toEqual([2500, 5000]);
  });

  it('records an accepted 409 body cart without waiting for a later successful read', async () => {
    useFakeClock();
    let hangs = false;
    installFetch({
      cart: () => hangs ? new Promise<Response>(() => undefined) : json(emptyCart),
      add: () => json({ error: 'stale', cart: { ...cartWithMeal, version: 2 } }, 409),
    });
    startPage();
    await advanceClock(0);
    hangs = true;
    await act(async () => { window.dispatchEvent(new Event('offline')); });
    expect(document.body.textContent).toContain('通信が不安定です');
    vi.setSystemTime(new Date('2026-01-01T00:01:00.000Z'));
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'カートに追加' })); });
    await advanceClock(0);
    expect(document.body.textContent).not.toContain('通信が不安定です');
    expect(screen.getByText('テーブルで共有中・09:01')).toBeTruthy();
    expect(screen.getByRole('alert').textContent).toContain('カートまたは価格が更新されました');
  });

  it('shows the exact Chinese sync copy and Tokyo time in the page and open sheet', async () => {
    useFakeClock();
    installFetch();
    startPage();
    await advanceClock(0);
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: '繁中' })); });
    expect(screen.getByText('同桌共用中・09:00')).toBeTruthy();
    await act(async () => { window.dispatchEvent(new Event('offline')); });
    expect(screen.getByText('等待同步・09:00 的內容')).toBeTruthy();
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: /^開啟購物車 / })); });
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).getByText('網路連線不穩')).toBeTruthy();
    const reason = '目前顯示 09:00 的內容。恢復連線前無法送出訂單，購物車內容會保留。';
    expect(within(dialog).getByText(reason)).toBeTruthy();
    const button = within(dialog).getByRole('button', { name: '確認金額' }) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
    expect(document.getElementById(button.getAttribute('aria-describedby')!)?.textContent).toContain(reason);
    expect(document.body.textContent).not.toContain('最新共用內容');
  });

});
