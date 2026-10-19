// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { grantFromFragment, orderReadClient, orderReadLink, type OrderReadGrant } from '../api/order-read';
import { MyOrders, OrderPage, OrderStatusView, orderPollInterval, RequestOrderLink } from './OrderRead';
import { loadMyOrders, rememberOrderGrant } from './my-orders';
import styles from './OrderRead.module.css';

const grant: OrderReadGrant = { order_id: 'a'.repeat(32), order_read_token: 'A'.repeat(43), expires_at: '2099-10-19T00:00:00Z' };
const state = { display_state: 'STANDARD', version: 1, payment_status: 'UNPAID', fulfillment_status: 'WAITING', total_gross_jpy: '9007199254740993', effective_total_gross_jpy: '9007199254740993', effective_lines: [], adjusted: false, refunds: [], refunded_total_jpy: '0', currency: 'JPY', is_terminal: false };
function json(value: unknown, status = 200) { return new Response(JSON.stringify(value), { status, headers: { 'content-type': 'application/json' } }); }
async function flush(ms = 1) { await act(async () => { await vi.advanceTimersByTimeAsync(ms); }); }
async function visibility(value: 'visible' | 'hidden') {
  Object.defineProperty(document, 'visibilityState', { configurable: true, value });
  await act(async () => { fireEvent(document, new Event('visibilitychange')); });
  await flush();
}
beforeEach(() => {
  vi.useFakeTimers();
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
  Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' });
  localStorage.clear();
  localStorage.setItem('tojima.my-orders.v1', 'null'); loadMyOrders();
  localStorage.setItem('tojima.my-orders.v1', '[]'); loadMyOrders();
});
afterEach(() => { cleanup(); window.history.replaceState({}, '', '/'); vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.useRealTimers(); });

describe('read-only order status', () => {
  it.each([
    ['REQUESTED', 'ja', '返金処理中'], ['PROCESSING', 'ja', '返金処理中'], ['FAILED', 'ja', '返金を確認中です。店舗にお問い合わせください'], ['SUCCEEDED', 'ja', '返金済み ¥330'],
    ['REQUESTED', 'zh', '退款處理中'], ['PROCESSING', 'zh', '退款處理中'], ['FAILED', 'zh', '退款確認中，請洽店家'], ['SUCCEEDED', 'zh', '已退款 ¥330'],
  ] as const)('shows %s refund safely in %s', async (refundState, locale, message) => {
    const fetcher = vi.fn(async () => json({ ...state, payment_status: 'PAID', refunds: [{ status: refundState, amount_jpy: '330', method: 'STRIPE' }] }));
    vi.stubGlobal('fetch', fetcher);
    render(<OrderStatusView grant={grant} locale={locale} />); await flush();
    expect(screen.getByRole('status').textContent).toContain(message);
    if (refundState !== 'SUCCEEDED') expect(screen.queryByText(/返金済み|已退款/)).toBeNull();
  });

  it.each(['ja', 'zh'] as const)('shows adjusted effective content and original total in %s', async (locale) => {
    vi.stubGlobal('fetch', vi.fn(async () => json({ ...state, total_gross_jpy: '660', effective_total_gross_jpy: '330', effective_lines: [{ product_name: 'ほうじ茶', quantity: 1, line_total_jpy: '330', option_names: ['茶葉多め'] }], adjusted: true })));
    render(<OrderStatusView grant={grant} locale={locale} />); await flush();
    expect(screen.getByText('ほうじ茶 × 1')).toBeTruthy();
    expect(screen.getByText('茶葉多め')).toBeTruthy();
    expect(screen.getAllByText('¥330').length).toBeGreaterThan(0); expect(screen.getByText('¥660')).toBeTruthy();
    expect(screen.getAllByText(locale === 'ja' ? '店舗により注文内容が変更されました' : '店家已調整您的訂單').length).toBeGreaterThan(0);
  });

  it.each(['ja', 'zh'] as const)('distinguishes same-product variants and shows the reduced effective quantity (%s)', async (locale) => {
    vi.stubGlobal('fetch', vi.fn(async () => json({ ...state, total_gross_jpy: '990', effective_total_gross_jpy: '660', adjusted: true, effective_lines: [
      { product_name: 'ほうじ茶', quantity: 1, line_total_jpy: '330', option_names: ['無糖'] },
      { product_name: 'ほうじ茶', quantity: 1, line_total_jpy: '330', option_names: ['甘さ控えめ'] },
    ] })));
    render(<OrderStatusView grant={grant} locale={locale} />); await flush();
    expect(screen.getAllByText('ほうじ茶 × 1')).toHaveLength(2);
    expect(screen.getByText('無糖')).toBeTruthy(); expect(screen.getByText('甘さ控えめ')).toBeTruthy();
    expect(screen.getByText('¥660')).toBeTruthy(); expect(screen.getByText('¥990')).toBeTruthy();
  });

  it.each(['ja', 'zh'] as const)('labels a whole-order cancellation in %s', async (locale) => {
    vi.stubGlobal('fetch', vi.fn(async () => json({ ...state, display_state: 'CANCELLED', payment_status: 'REFUNDED', fulfillment_status: 'CANCELLED', refunds: [{ status: 'SUCCEEDED', amount_jpy: '330', method: 'STRIPE' }], refunded_total_jpy: '330' })));
    render(<OrderStatusView grant={grant} locale={locale} />); await flush();
    expect(screen.getAllByText(locale === 'ja' ? 'この注文は取り消されました' : '此訂單已取消').length).toBeGreaterThan(0);
    expect(screen.getByText(locale === 'ja' ? '返金済み ¥330' : '已退款 ¥330')).toBeTruthy();
  });

  it.each([
    ['ja', 'お支払いを確認しました。店舗でご注文を確認中です'],
    ['zh', '已確認付款，店家正在確認您的訂單'],
  ] as const)('shows the exact paid review message in %s, keeps polling, and shows kitchen state after release', async (locale, message) => {
    let released = false;
    const fetcher = vi.fn(async () => json({ ...state, payment_status: 'PAID', display_state: released ? 'STANDARD' : 'PAID_UNDER_REVIEW', fulfillment_status: released ? 'PREPARING' : 'WAITING', is_terminal: false }));
    vi.stubGlobal('fetch', fetcher);
    render(<OrderStatusView grant={grant} locale={locale} />); await flush();
    expect(screen.getByRole('status').textContent).toBe(message);
    expect(screen.queryByText(/調理中|製作中|返金|退款/)).toBeNull();
    await flush(30_001); expect(fetcher).toHaveBeenCalledTimes(2);
    expect(screen.getByRole('status').textContent).toBe(message);
    released = true;
    await flush(30_001); expect(fetcher).toHaveBeenCalledTimes(3);
    expect(screen.queryByText(message)).toBeNull();
    expect(screen.getByText(locale === 'ja' ? '調理中' : '製作中')).toBeTruthy();
  });

  it.each([
    { display_state: 'FUTURE_SUCCESS', payment_status: 'PAID', fulfillment_status: 'SERVED' },
    { display_state: 'FUTURE_SUCCESS', payment_status: 'PAID', fulfillment_status: 'CANCELLED' },
    { display_state: 'CANCELLED', payment_status: 'PAID', fulfillment_status: 'SERVED' },
    { display_state: 'UNKNOWN', payment_status: 'REFUNDED', fulfillment_status: 'SERVED' },
    { display_state: 'STANDARD', payment_status: 'FUTURE_PAYMENT', fulfillment_status: 'SERVED' },
    { display_state: 'STANDARD', payment_status: 'PAID', fulfillment_status: 'FUTURE_KITCHEN' },
    { display_state: undefined, payment_status: 'PAID', fulfillment_status: 'SERVED' },
  ])('uses a neutral fallback for unsupported state $display_state / $payment_status / $fulfillment_status', async (unsupported) => {
    vi.stubGlobal('fetch', vi.fn(async () => json({ ...state, ...unsupported })));
    render(<OrderStatusView grant={grant} locale="ja" />); await flush();
    expect(screen.getByRole('status').textContent).toBe('店舗に確認してください');
    expect(screen.queryByText(/支払済み|提供済み|返金/)).toBeNull();
  });

  it('starts hidden without a request, refreshes immediately on resume, and polls at 30s only while visible', async () => {
    await visibility('hidden');
    const fetcher = vi.fn(async () => json(state)); vi.stubGlobal('fetch', fetcher);
    const view = render(<OrderStatusView grant={grant} locale="ja" />);
    await flush(90_000); expect(fetcher).not.toHaveBeenCalled();
    await visibility('visible'); expect(fetcher).toHaveBeenCalledTimes(1);
    expect(screen.getByText('¥9,007,199,254,740,993')).toBeTruthy();
    await flush(29_000); expect(fetcher).toHaveBeenCalledTimes(1);
    await flush(1001); expect(fetcher).toHaveBeenCalledTimes(2);
    await visibility('hidden'); await flush(90_000); expect(fetcher).toHaveBeenCalledTimes(2);
    await visibility('visible'); expect(fetcher).toHaveBeenCalledTimes(3);
    view.unmount(); await flush(90_000); expect(fetcher).toHaveBeenCalledTimes(3);
  });

  it('has no overlap for a 35-second request and aborts on hide and unmount', async () => {
    let active = 0; let maxActive = 0;
    const signals: AbortSignal[] = [];
    const fetcher = vi.fn((_url: RequestInfo | URL, init?: RequestInit) => new Promise<Response>((resolve, reject) => {
      const signal = init!.signal!; signals.push(signal); active++; maxActive = Math.max(maxActive, active);
      const timer = setTimeout(() => { active--; resolve(json(state)); }, 35_000);
      signal.addEventListener('abort', () => { clearTimeout(timer); active--; reject(new DOMException('Aborted', 'AbortError')); }, { once: true });
    })); vi.stubGlobal('fetch', fetcher);
    const view = render(<OrderStatusView grant={grant} locale="ja" />);
    await flush(30_001); expect(fetcher).toHaveBeenCalledTimes(1); expect(maxActive).toBe(1);
    await visibility('hidden'); expect(signals[0]!.aborted).toBe(true); expect(active).toBe(0);
    await flush(60_000); expect(fetcher).toHaveBeenCalledTimes(1);
    await visibility('visible'); expect(fetcher).toHaveBeenCalledTimes(2);
    await flush(35_001); expect(maxActive).toBe(1); expect(active).toBe(0);
    await flush(30_001); expect(fetcher).toHaveBeenCalledTimes(3);
    view.unmount(); expect(signals[2]!.aborted).toBe(true);
  });

  it('does not overlap even when an aborted transport settles late, and ignores its result', async () => {
    let resolveOld!: (response: Response) => void;
    const signals: AbortSignal[] = [];
    const fetcher = vi.fn((_url: RequestInfo | URL, init?: RequestInit) => {
      signals.push(init!.signal!);
      return signals.length === 1 ? new Promise<Response>((resolve) => { resolveOld = resolve; }) : Promise.resolve(json({ ...state, version: 2, fulfillment_status: 'READY' }));
    }); vi.stubGlobal('fetch', fetcher);
    render(<OrderStatusView grant={grant} locale="ja" />);
    await visibility('hidden'); expect(signals[0]!.aborted).toBe(true);
    await visibility('visible'); await flush(35_000); expect(fetcher).toHaveBeenCalledTimes(1);
    await act(async () => { resolveOld(json({ ...state, total_gross_jpy: '1' })); });
    await flush(); expect(fetcher).toHaveBeenCalledTimes(2);
    expect(screen.queryByText('¥1')).toBeNull(); expect(screen.getByText('配膳待ち')).toBeTruthy();
  });

  it('retains confirmed values with a stale notice after failure, then recovers without guessing', async () => {
    let count = 0;
    const fetcher = vi.fn(async () => { count++; if (count === 2) throw new TypeError('offline'); return json(state); });
    vi.stubGlobal('fetch', fetcher);
    render(<OrderStatusView grant={grant} locale="zh" />); await flush();
    await flush(30_001); expect(fetcher).toHaveBeenCalledTimes(2);
    expect(screen.getByRole('status').textContent).toContain('無法確認最新狀態');
    expect(screen.getByText('尚未付款')).toBeTruthy();
    expect(screen.getByText('¥9,007,199,254,740,993')).toBeTruthy();
    await visibility('hidden'); await visibility('visible');
    expect(fetcher).toHaveBeenCalledTimes(3); expect(screen.queryByRole('status')).toBeNull();
  });

  it.each(['ja', 'zh'] as const)('retains effective amount and processing refund during a stale read, then shows server-confirmed success (%s)', async (locale) => {
    let count = 0;
    const adjusted = {
      ...state, payment_status: 'PAID', fulfillment_status: 'SERVED', total_gross_jpy: '660', effective_total_gross_jpy: '330',
      effective_lines: [{ product_name: 'ほうじ茶', quantity: 1, line_total_jpy: '330', option_names: ['茶葉多め'] }], adjusted: true,
      refunds: [{ status: 'PROCESSING', amount_jpy: '330', method: 'STRIPE' }], refunded_total_jpy: '0',
    };
    const fetcher = vi.fn(async () => {
      count++;
      if (count === 2) throw new TypeError('offline');
      return json(count === 1 ? adjusted : { ...adjusted, refunds: [{ status: 'SUCCEEDED', amount_jpy: '330', method: 'STRIPE' }], refunded_total_jpy: '330', is_terminal: true });
    });
    vi.stubGlobal('fetch', fetcher);
    render(<OrderStatusView grant={grant} locale={locale} />); await flush();
    const pending = locale === 'ja' ? '返金処理中' : '退款處理中';
    const success = locale === 'ja' ? '返金済み ¥330' : '已退款 ¥330';
    expect(screen.getByText('ほうじ茶 × 1')).toBeTruthy();
    expect(screen.getByText('茶葉多め')).toBeTruthy();
    expect(screen.getAllByText('¥330').length).toBeGreaterThan(0); expect(screen.getByText('¥660')).toBeTruthy();
    expect(screen.getByText(pending)).toBeTruthy();
    await flush(30_001); expect(fetcher).toHaveBeenCalledTimes(2);
    expect(screen.getByText(pending)).toBeTruthy(); expect(screen.getByText('¥660')).toBeTruthy();
    expect(screen.getByText('茶葉多め')).toBeTruthy();
    expect(screen.getAllByRole('status').some((node) => node.textContent?.includes(locale === 'ja' ? '最新の状況を確認できません' : '無法確認最新狀態'))).toBe(true);
    await flush(30_001); expect(fetcher).toHaveBeenCalledTimes(3);
    expect(screen.getByText(success)).toBeTruthy(); expect(screen.queryByText(pending)).toBeNull();
    expect(screen.getByText('茶葉多め')).toBeTruthy();
  });

  it('keeps polling a paid, served order until the server confirms refund resolution', async () => {
    let resolved = false;
    const fetcher = vi.fn(async () => json({ ...state, is_terminal: resolved, payment_status: 'PAID', fulfillment_status: 'SERVED' }));
    vi.stubGlobal('fetch', fetcher);
    render(<OrderStatusView grant={grant} locale="ja" />); await flush();
    expect(fetcher).toHaveBeenCalledTimes(1);
    // The server keeps is_terminal=false while a refund remains unresolved.
    for (let expected = 2; expected <= 4; expected++) {
      await flush(30_001); expect(fetcher).toHaveBeenCalledTimes(expected);
    }
    resolved = true;
    await flush(30_001); expect(fetcher).toHaveBeenCalledTimes(5);
    await flush(90_000); expect(fetcher).toHaveBeenCalledTimes(5);
  });

  it('stops periodic polling only on the server terminal flag, and still refreshes on resume', async () => {
    const fetcher = vi.fn(async () => json({ ...state, is_terminal: true, payment_status: 'PAID', fulfillment_status: 'SERVED' }));
    vi.stubGlobal('fetch', fetcher);
    render(<OrderStatusView grant={grant} locale="ja" />); await flush(90_000);
    expect(fetcher).toHaveBeenCalledTimes(1);
    await visibility('hidden'); await visibility('visible'); expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it('cancels the old capability and clears its displayed values on identity change', async () => {
    const signals: AbortSignal[] = [];
    vi.stubGlobal('fetch', vi.fn((_url: RequestInfo | URL, init?: RequestInit) => { signals.push(init!.signal!); return new Promise<Response>(() => undefined); }));
    const view = render(<OrderStatusView grant={grant} locale="ja" />);
    view.rerender(<OrderStatusView grant={{ ...grant, order_read_token: 'B'.repeat(43) }} locale="ja" />);
    expect(signals[0]!.aborted).toBe(true); expect(signals).toHaveLength(2);
    expect(screen.queryByText('¥9,007,199,254,740,993')).toBeNull();
  });

  it('uses the fragment for sharing and sends the token only in the bearer header', async () => {
    // Testing Library's async wrapper also needs its final zero-delay timer.
    vi.useRealTimers();
    const user = userEvent.setup();
    const write = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue();
    const fetcher = vi.fn(async () => json(state)); vi.stubGlobal('fetch', fetcher);
    render(<OrderStatusView grant={grant} locale="ja" />);
    expect(await screen.findByText('¥9,007,199,254,740,993')).toBeTruthy();
    await user.click(screen.getByRole('button', { name: '共有リンクをコピー' }));
    expect(await screen.findByRole('button', { name: 'コピーしました' })).toBeTruthy();
    const link = write.mock.calls[0]![0]; const url = new URL(link);
    expect(url.search).toBe(''); expect(url.hash).toContain(grant.order_read_token);
    expect(grantFromFragment(url.hash)).toEqual(grant);
    const [path, init] = fetcher.mock.calls[0] as unknown as [string, RequestInit];
    expect(path).not.toContain(grant.order_read_token); expect(path).not.toContain('?');
    expect(init.headers).toMatchObject({ authorization: `Bearer ${grant.order_read_token}` });
    expect(init.referrerPolicy).toBe('no-referrer');
  });

  it('changes the capability when another saved fragment link is opened on the same page', async () => {
    let count = 0;
    const fetcher = vi.fn(async () => json({ ...state, total_gross_jpy: String(++count), effective_total_gross_jpy: String(count) }));
    vi.stubGlobal('fetch', fetcher);
    window.history.replaceState({}, '', orderReadLink(grant));
    render(<OrderPage />); await flush();
    expect(screen.getByText('¥1')).toBeTruthy();
    const next = { ...grant, order_id: 'b'.repeat(32), order_read_token: 'B'.repeat(43) };
    window.history.replaceState({}, '', orderReadLink(next));
    await act(async () => { fireEvent(window, new HashChangeEvent('hashchange')); });
    await flush();
    expect(fetcher).toHaveBeenCalledTimes(2);
    expect(screen.queryByText('¥1')).toBeNull(); expect(screen.getByText('¥2')).toBeTruthy();
  });

  it('clamps configuration to 15–30 seconds', () => {
    expect(orderPollInterval()).toBe(30_000); expect(orderPollInterval(1)).toBe(15_000);
    expect(orderPollInterval(99_000)).toBe(30_000); expect(orderPollInterval(NaN)).toBe(30_000);
  });
});

describe('same-browser my orders', () => {
  it.each([
    ['expired', '2000-01-01T00:00:00Z', 403],
    ['rejected', grant.expires_at, 403],
    ['expired despite a successful read', '2000-01-01T00:00:00Z', 200],
  ])('keeps a valid saved grant when a fragment is %s', async (_reason, expires_at, status) => {
    rememberOrderGrant(grant);
    const replacement = { ...grant, order_read_token: 'B'.repeat(43), expires_at };
    vi.stubGlobal('fetch', vi.fn(async () => json(state, status)));
    window.history.replaceState({}, '', orderReadLink(replacement));
    render(<OrderPage />);
    expect(loadMyOrders()).toEqual([grant]);
    await flush();
    expect(loadMyOrders()).toEqual([grant]);
    expect(JSON.parse(localStorage.getItem('tojima.my-orders.v1')!)).toEqual([grant]);
    expect(screen.getByRole('link').getAttribute('href')).toBe(orderReadLink(grant));
  });

  it.each([false, true])('saves a valid fragment only after its first successful status read (existing grant: %s)', async (existing) => {
    if (existing) rememberOrderGrant(grant);
    const replacement = { ...grant, order_read_token: 'B'.repeat(43) };
    let resolveStatus!: (response: Response) => void;
    const fetcher = vi.fn(() => new Promise<Response>((resolve) => { resolveStatus = resolve; }));
    vi.stubGlobal('fetch', fetcher);
    window.history.replaceState({}, '', orderReadLink(replacement));
    render(<OrderPage />);
    await flush();
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(loadMyOrders()).toEqual(existing ? [grant] : []);
    await act(async () => { resolveStatus(json(state)); });
    await flush();
    expect(loadMyOrders()).toEqual([replacement]);
    expect(JSON.parse(localStorage.getItem('tojima.my-orders.v1')!)).toEqual([replacement]);
    expect(screen.getByRole('link').getAttribute('href')).toBe(orderReadLink(replacement));
  });

  it('persists grants and reloads a bounded list of fragment links', async () => {
    act(() => rememberOrderGrant(grant));
    const view = render(<MyOrders locale="ja" />);
    expect(screen.getByRole('link').getAttribute('href')).toBe(orderReadLink(grant));
    view.unmount(); render(<MyOrders locale="zh" />);
    expect(screen.getByRole('heading', { name: '我的訂單' })).toBeTruthy();
    expect(screen.getByRole('link').getAttribute('href')).toBe(orderReadLink(grant));
    expect(loadMyOrders()).toHaveLength(1);
  });
  it('keeps links usable in memory when localStorage reads and writes throw', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('denied'); });
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('quota'); });
    render(<MyOrders locale="ja" />);
    act(() => rememberOrderGrant(grant));
    expect(screen.getByRole('link').getAttribute('href')).toBe(orderReadLink(grant));
    expect(loadMyOrders()).toEqual([grant]);
    act(() => rememberOrderGrant({ ...grant, order_read_token: 'B'.repeat(43) }));
    expect(loadMyOrders()).toHaveLength(1);
  });
  it('retains a failed write even when storage reads still work', () => {
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('quota'); });
    render(<MyOrders locale="ja" />);
    act(() => rememberOrderGrant(grant));
    expect(loadMyOrders()).toEqual([grant]);
    expect(screen.getByRole('link').getAttribute('href')).toBe(orderReadLink(grant));
  });
  it('ignores corrupt or expired saved entries', () => {
    localStorage.setItem('tojima.my-orders.v1', JSON.stringify([{}, { ...grant, expires_at: '2000-01-01T00:00:00Z' }]));
    expect(loadMyOrders()).toEqual([]);
    localStorage.setItem('tojima.my-orders.v1', 'broken');
    expect(loadMyOrders()).toEqual([]);
  });
});

describe('order-link target sizing', () => {
  it.each([
    ['ja', 'この注文の閲覧リンクを保存', '注文状況'],
    ['zh', '儲存此筆訂單的查閱連結', '訂單狀態'],
  ] as const)('sizes the request and resulting order link in %s', async (locale, requestName, resultName) => {
    vi.spyOn(orderReadClient, 'issue').mockResolvedValue(grant);
    render(<RequestOrderLink sessionID="session" token="join-token" orderID={grant.order_id} locale={locale} />);

    const request = screen.getByRole('button', { name: requestName });
    expect(request.className).toContain(styles.control);
    expect(request.getAttribute('data-control')).toBe('order-link-action');
    await act(async () => { fireEvent.click(request); });

    const result = screen.getByRole('link', { name: resultName });
    expect(result.className).toContain(styles.control);
    expect(result.getAttribute('data-control')).toBe('order-link-result');
    expect(result.getAttribute('href')).toBe(orderReadLink(grant));
  });

  it('uses the same sized control treatment for sharing, saved links, and the shared URL field', async () => {
    vi.useRealTimers();
    const user = userEvent.setup();
    vi.spyOn(orderReadClient, 'status').mockResolvedValue({ ...state, currency: 'JPY' });
    rememberOrderGrant(grant);
    render(<><OrderStatusView grant={grant} locale="ja" /><MyOrders locale="ja" /></>);

    const savedLink = screen.getByRole('link', { name: grant.order_id });
    expect(savedLink.className).toContain(styles.control);
    const shareButton = await screen.findByRole('button', { name: '共有リンクをコピー' });
    expect(shareButton.className).toContain(styles.control);
    await user.click(shareButton);
    const sharedInput = await screen.findByRole('textbox', { name: '共有リンク' });
    expect(sharedInput.className).toContain(styles.sharedInput);
  });
});
