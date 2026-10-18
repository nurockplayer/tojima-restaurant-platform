// @vitest-environment jsdom
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { waitFor, within } from '@testing-library/dom';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { StaffOrder } from '../api/operations';
import { StaffApp } from './StaffApp';

let root: Root | undefined;
let host: HTMLDivElement | undefined;
const base = '/v1/staff/tenants/tenant-1/stores/store-1';
const staffContext = { tenantID: 'tenant-1', storeID: 'store-1' };
const staffToken = 'mock-token-unit-test-only';
const table = { table_id: 'table-1', name: 'T1', session_id: 'session-1', status: 'OPEN', mode: 'DIRECT_SCAN', unpaid_cash_jpy: '1100', order_count: 1 };

beforeEach(() => {
  if (typeof HTMLDialogElement.prototype.showModal !== 'function') Object.defineProperty(HTMLDialogElement.prototype, 'showModal', { configurable: true, value(this: HTMLDialogElement) { this.setAttribute('open', ''); } });
  if (typeof HTMLDialogElement.prototype.close !== 'function') Object.defineProperty(HTMLDialogElement.prototype, 'close', { configurable: true, value(this: HTMLDialogElement) { this.removeAttribute('open'); this.dispatchEvent(new Event('close')); } });
});

afterEach(async () => {
  if (root) await act(async () => root?.unmount());
  root = undefined; host?.remove(); host = undefined;
  sessionStorage.clear(); vi.unstubAllGlobals(); vi.useRealTimers();
});

describe('staff operations', () => {
  it('retains a cancelled kitchen card until the kitchen acknowledges its update', async () => {
    const order = staffOrder({ fulfillment_status: 'CANCELLED', order_updated: true, version: 4, lines: [{ product_name: '餃子', quantity: 0, unit_gross_jpy: '500', line_gross_jpy: '0', options: [] }] });
    let acknowledged = false;
    const routes: Route = async (url, init) => {
      if (url === `${base}/operations/access`) return json({ can_cash: false, can_serve: false, can_kitchen: true, can_manage: false });
      if (url === `${base}/operations/kds`) return json({ orders: acknowledged ? [] : [order] });
      if (url.endsWith('/acknowledge-update')) {
        expect(JSON.parse(String(init.body))).toEqual({ expected_version: 4 });
        acknowledged = true; return json({ ...order, version: 5, order_updated: false });
      }
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes, { initialView: 'kds' });
    expect(host?.textContent).toContain('餃子 × 0');
    expect(within(host!).queryByRole('button', { name: '調理開始' })).toBeNull();
    await click(host!, '更新を確認');
    await waitFor(() => expect(host?.textContent).not.toContain('餃子 × 0'));
    expect(acknowledged).toBe(true);
  });

  it('shows localized adjustment labels in JA and zh-Hant', async () => {
    const order = staffOrder({ payment_method: 'STRIPE', payment_status: 'PAID' });
    const routes: Route = async (url) => {
      if (url === `${base}/operations/access`) return json({ can_cash: false, can_serve: false, can_kitchen: false, can_manage: true, can_approve_fulfillment: true });
      if (url === `${base}/operations/tables`) return json({ tables: [table] });
      if (url.endsWith('/operations')) return json({ session: { id: 'session-1', status: 'OPEN', mode: 'DIRECT_SCAN' }, orders: [order], receipts: [], close_blockers: [] });
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    await click(host!, '詳細'); await click(host!, '取消・減量');
    expect(host?.textContent).toContain('取消・数量変更');
    expect(host?.textContent).toContain('取消理由（必須）');
    await click(host!, 'キャンセル');
    await click(host!, '繁體中文'); await click(host!, '取消・減量');
    expect(host?.textContent).toContain('取消或減少訂購數量');
    expect(host?.textContent).toContain('取消原因（必填）');
  });

  it.each([
    { code: 'REFUND_DISPATCH_PENDING' as const, ja: '返金の送信待ち（自動再試行）', zh: '退款等待送出（自動重試）' },
    { code: 'HOLD_RECONCILIATION_NEEDED' as const, ja: '返金状況の確認が必要です', zh: '需要確認退款狀態' },
  ])('shows $code without offering an unsafe manager retry', async ({ code, ja, zh }) => {
    const order = staffOrder({ payment_status: 'REFUND_PENDING', fulfillment_status: 'CANCELLED', refunds: [{ id: 'refund-pending', amount_jpy: '500', status: 'PROCESSING', method: 'STRIPE', safe_error_code: code }] });
    const routes: Route = async (url) => {
      if (url === `${base}/operations/access`) return json({ can_cash: false, can_serve: false, can_kitchen: false, can_manage: true, can_approve_fulfillment: true });
      if (url === `${base}/operations/tables`) return json({ tables: [table] });
      if (url.endsWith('/operations')) return json({ session: { id: 'session-1', status: 'OPEN', mode: 'DIRECT_SCAN' }, orders: [order], receipts: [], close_blockers: [] });
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes); await click(host!, '詳細');
    expect(host?.textContent).toContain(ja);
    expect(within(host!).queryByRole('button', { name: '返金を再試行' })).toBeNull();
    await click(host!, '繁體中文');
    expect(host?.textContent).toContain(zh);
    expect(within(host!).queryByRole('button', { name: '重試退款' })).toBeNull();
  });

  it('shows FAILED refund and retries only after an explicit click', async () => {
    const order = staffOrder({ payment_status: 'PAID', refunds: [{ id: 'refund-failed', amount_jpy: '500', status: 'FAILED', method: 'STRIPE' }] });
    let retryCalls = 0; let refundStatus: 'FAILED' | 'PROCESSING' = 'FAILED'; const retryResponse = deferred<Response>();
    const routes: Route = async (url) => {
      if (url === `${base}/operations/access`) return json({ can_cash: false, can_serve: false, can_kitchen: false, can_manage: true, can_approve_fulfillment: true });
      if (url === `${base}/operations/tables`) return json({ tables: [table] });
      if (url.endsWith('/operations')) return json({ session: { id: 'session-1', status: 'OPEN', mode: 'DIRECT_SCAN' }, orders: [{ ...order, refunds: [{ ...order.refunds![0]!, status: refundStatus }] }], receipts: [], close_blockers: [] });
      if (url.endsWith('/refunds/refund-failed/retry')) { retryCalls++; return retryResponse.promise; }
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes); await click(host!, '詳細');
    expect(host?.textContent).toContain('失敗'); expect(retryCalls).toBe(0);
    const retry = within(host!).getByRole('button', { name: '返金を再試行' });
    await userEvent.setup().dblClick(retry);
    await waitFor(() => expect(retryCalls).toBe(1));
    refundStatus = 'PROCESSING'; retryResponse.resolve(json({ ...order, refunds: [{ ...order.refunds![0]!, status: 'PROCESSING' }] }));
    await waitFor(() => expect(host?.textContent).toContain('処理中'));
  });

  it('requires cash hand-back confirmation before a paid cash adjustment can be confirmed', async () => {
    const order = staffOrder({ payment_method: 'CASH', payment_status: 'PAID' });
    const routes: Route = async (url, init) => {
      if (url === `${base}/operations/access`) return json({ can_cash: true, can_serve: true, can_kitchen: false, can_manage: true, can_approve_fulfillment: true });
      if (url === `${base}/operations/tables`) return json({ tables: [table] });
      if (url.endsWith('/operations')) return json({ session: { id: 'session-1', status: 'OPEN', mode: 'DIRECT_SCAN' }, orders: [order], receipts: [], close_blockers: [] });
      if (url.endsWith('/adjustments/preview')) return json({ expected_version: 3, original_total_gross_jpy: '1210', effective_total_gross_jpy: '0', refund_amount_jpy: '1210', whole_order_cancel: true, lines: [] });
      if (url.endsWith('/adjustments')) return json({ ...order, payment_status: 'REFUND_PENDING' });
      throw new Error(`Unexpected request ${url} ${String(init.body)}`);
    };
    await mountStaff(routes); await click(host!, '詳細'); await click(host!, '取消・減量');
    const dialog = host!.querySelector<HTMLDialogElement>('dialog[open]')!;
    await userEvent.setup().clear(within(dialog).getAllByRole('spinbutton')[0]!);
    await userEvent.setup().type(within(dialog).getAllByRole('spinbutton')[0]!, '1');
    await userEvent.setup().type(within(dialog).getByLabelText('取消理由（必須）'), '誤注文');
    await click(dialog, '変更内容を確認');
    await waitFor(() => expect(within(dialog).getByRole('button', { name: '変更を確定' }).hasAttribute('disabled')).toBe(true));
    await userEvent.setup().click(within(dialog).getByRole('checkbox'));
    expect(within(dialog).getByRole('button', { name: '変更を確定' }).hasAttribute('disabled')).toBe(true);
    await click(dialog, '変更内容を確認');
    await waitFor(() => expect(within(dialog).getByRole('button', { name: '変更を確定' }).hasAttribute('disabled')).toBe(false));
  });

  it('retries an uncertain adjustment with the original idempotency key and body', async () => {
    const order = staffOrder({ payment_method: 'STRIPE', payment_status: 'PAID' });
    const calls: Array<{ body: string; key: string | null }> = [];
    const routes: Route = async (url, init) => {
      if (url === `${base}/operations/access`) return json({ can_cash: false, can_serve: false, can_kitchen: false, can_manage: true, can_approve_fulfillment: true });
      if (url === `${base}/operations/tables`) return json({ tables: [table] });
      if (url.endsWith('/operations')) return json({ session: { id: 'session-1', status: 'OPEN', mode: 'DIRECT_SCAN' }, orders: [order], receipts: [], close_blockers: [] });
      if (url.endsWith('/adjustments/preview')) return json({ expected_version: 3, original_total_gross_jpy: '1210', effective_total_gross_jpy: '0', refund_amount_jpy: '1210', whole_order_cancel: true, lines: [] });
      if (url.endsWith('/adjustments')) { calls.push({ body: String(init.body), key: new Headers(init.headers).get('Idempotency-Key') }); return calls.length === 1 ? json({ error: 'unknown_result' }, 502) : json({ ...order, payment_status: 'REFUND_PENDING' }); }
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes); await click(host!, '詳細'); await click(host!, '取消・減量');
    const dialog = host!.querySelector<HTMLDialogElement>('dialog[open]')!; const user = userEvent.setup();
    await user.clear(within(dialog).getAllByRole('spinbutton')[0]!); await user.type(within(dialog).getAllByRole('spinbutton')[0]!, '1');
    await user.type(within(dialog).getByLabelText('取消理由（必須）'), '誤注文'); await user.click(within(dialog).getByRole('button', { name: '変更内容を確認' }));
    await waitFor(() => expect(within(dialog).getByRole('button', { name: '変更を確定' }).hasAttribute('disabled')).toBe(false));
    await user.click(within(dialog).getByRole('button', { name: '変更を確定' }));
    await waitFor(() => expect(within(dialog).getByRole('button', { name: '同じ操作を確認して再試行' })).toBeTruthy());
    await user.click(within(dialog).getByRole('button', { name: '同じ操作を確認して再試行' }));
    await waitFor(() => expect(calls).toHaveLength(2));
    expect(calls[0]).toEqual(calls[1]);
  });

  it('invalidates an adjustment preview when the staff identity disconnects', async () => {
    const order = staffOrder({ payment_method: 'STRIPE', payment_status: 'PAID' });
    const routes: Route = async (url) => {
      if (url === `${base}/operations/access`) return json({ can_cash: false, can_serve: false, can_kitchen: false, can_manage: true, can_approve_fulfillment: true });
      if (url === `${base}/operations/tables`) return json({ tables: [table] });
      if (url.endsWith('/operations')) return json({ session: { id: 'session-1', status: 'OPEN', mode: 'DIRECT_SCAN' }, orders: [order], receipts: [], close_blockers: [] });
      if (url.endsWith('/adjustments/preview')) return json({ expected_version: 3, original_total_gross_jpy: '1210', effective_total_gross_jpy: '0', refund_amount_jpy: '1210', whole_order_cancel: true, lines: [] });
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes); await click(host!, '詳細'); await click(host!, '取消・減量');
    const dialog = host!.querySelector<HTMLDialogElement>('dialog[open]')!; const user = userEvent.setup();
    await user.clear(within(dialog).getAllByRole('spinbutton')[0]!); await user.type(within(dialog).getAllByRole('spinbutton')[0]!, '1');
    await user.type(within(dialog).getByLabelText('取消理由（必須）'), '誤注文'); await user.click(within(dialog).getByRole('button', { name: '変更内容を確認' }));
    await waitFor(() => expect(host?.textContent).toContain('返金額'));
    await click(host!, '切断');
    expect(host?.querySelector('dialog[open]')).toBeNull();
    expect(host?.textContent).not.toContain('誤注文');
  });

  it('ignores a delayed server preview after the selected quantity changes', async () => {
    const order = staffOrder({ payment_method: 'STRIPE', payment_status: 'PAID' });
    const firstPreview = deferred<Response>(); let previewCalls = 0;
    const routes: Route = async (url) => {
      if (url === `${base}/operations/access`) return json({ can_cash: false, can_serve: false, can_kitchen: false, can_manage: true, can_approve_fulfillment: true });
      if (url === `${base}/operations/tables`) return json({ tables: [table] });
      if (url.endsWith('/operations')) return json({ session: { id: 'session-1', status: 'OPEN', mode: 'DIRECT_SCAN' }, orders: [order], receipts: [], close_blockers: [] });
      if (url.endsWith('/adjustments/preview')) {
        previewCalls++;
        return previewCalls === 1 ? firstPreview.promise : json({ expected_version: 3, original_total_gross_jpy: '1210', effective_total_gross_jpy: '1210', refund_amount_jpy: '0', whole_order_cancel: false, lines: [] });
      }
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes); await click(host!, '詳細'); await click(host!, '取消・減量');
    const dialog = host!.querySelector<HTMLDialogElement>('dialog[open]')!; const user = userEvent.setup();
    const qty = within(dialog).getAllByRole('spinbutton')[0]!;
    await user.clear(qty); await user.type(qty, '1');
    await user.type(within(dialog).getByLabelText('取消理由（必須）'), '誤注文');
    await user.click(within(dialog).getByRole('button', { name: '変更内容を確認' }));
    await waitFor(() => expect(previewCalls).toBe(1));
    await user.clear(qty); await user.type(qty, '0');
    firstPreview.resolve(json({ expected_version: 3, original_total_gross_jpy: '1210', effective_total_gross_jpy: '0', refund_amount_jpy: '1210', whole_order_cancel: true, lines: [] }));
    await waitFor(() => expect(within(dialog).getByRole('button', { name: '変更を確定' }).hasAttribute('disabled')).toBe(true));
    expect(host?.textContent).not.toContain('返金額');
    await user.clear(qty); await user.type(qty, '1');
    await user.click(within(dialog).getByRole('button', { name: '変更内容を確認' }));
    await waitFor(() => expect(host?.textContent).toContain('返金額'));
    expect(previewCalls).toBe(2);
  });

  it('reloads after a stale adjustment confirm and requires a fresh version preview', async () => {
    let detailCalls = 0; const previewVersions: number[] = []; let adjustCalls = 0;
    const makeOrder = (version: number) => staffOrder({ payment_method: 'STRIPE', payment_status: 'PAID', version, lines: [{ product_name: '餃子', quantity: 1, unit_gross_jpy: '500', line_gross_jpy: '500', options: [], line_index: 0 }] });
    const routes: Route = async (url, init) => {
      if (url === `${base}/operations/access`) return json({ can_cash: false, can_serve: false, can_kitchen: false, can_manage: true, can_approve_fulfillment: true });
      if (url === `${base}/operations/tables`) return json({ tables: [table] });
      if (url === `${base}/tables/table-1/sessions/session-1/operations`) { detailCalls++; return json({ session: { id: 'session-1', status: 'OPEN', mode: 'DIRECT_SCAN' }, orders: [makeOrder(detailCalls === 1 ? 3 : 4)], receipts: [], close_blockers: [] }); }
      if (url === `${base}/operations/reviews`) return json({ orders: [] });
      if (url.endsWith('/adjustments/preview')) { previewVersions.push(JSON.parse(String(init.body)).expected_version); return json({ expected_version: previewVersions.at(-1), original_total_gross_jpy: '500', effective_total_gross_jpy: '0', refund_amount_jpy: '500', whole_order_cancel: true, lines: [] }); }
      if (url.endsWith('/adjustments')) { adjustCalls++; return adjustCalls === 1 ? json({ error: 'stale_version' }, 409) : json(makeOrder(5)); }
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes); await click(host!, '詳細'); await click(host!, '取消・減量');
    const dialog = host!.querySelector<HTMLDialogElement>('dialog[open]')!; const user = userEvent.setup();
    const qty = within(dialog).getAllByRole('spinbutton')[0]!;
    await user.clear(qty); await user.type(qty, '1'); await user.type(within(dialog).getByLabelText('取消理由（必須）'), '誤注文');
    await user.click(within(dialog).getByRole('button', { name: '変更内容を確認' }));
    await waitFor(() => expect(within(dialog).getByRole('button', { name: '変更を確定' }).hasAttribute('disabled')).toBe(false));
    await user.click(within(dialog).getByRole('button', { name: '変更を確定' }));
    await waitFor(() => expect(within(dialog).getByRole('button', { name: '変更を確定' }).hasAttribute('disabled')).toBe(true));
    expect(adjustCalls).toBe(1);
    await user.clear(qty); await user.type(qty, '1');
    await user.click(within(dialog).getByRole('button', { name: '変更内容を確認' }));
    await waitFor(() => expect(previewVersions).toEqual([3, 4]));
    await user.click(within(dialog).getByRole('button', { name: '変更を確定' }));
    await waitFor(() => expect(adjustCalls).toBe(2));
  });

  it('requires kitchen acknowledgement before starting an updated order', async () => {
    let ackCalls = 0; let kdsCalls = 0;
    const updated = staffOrder({ id: 'updated-order', fulfillment_status: 'ACCEPTED', order_updated: true });
    const routes: Route = async (url) => {
      if (url === `${base}/operations/access`) return json({ can_cash: false, can_serve: false, can_kitchen: true, can_manage: false });
      if (url === `${base}/operations/kds`) { kdsCalls++; return json({ orders: [kdsCalls === 1 ? updated : { ...updated, order_updated: false }] }); }
      if (url.endsWith('/acknowledge-update')) { ackCalls++; return json({ ...updated, order_updated: false, version: 4 }); }
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes, { initialView: 'kds' });
    await waitFor(() => expect(host?.textContent).toContain('更新を確認'));
    expect(within(host!).queryByRole('button', { name: '調理開始' })).toBeNull();
    await click(host!, '更新を確認');
    await waitFor(() => expect(ackCalls).toBe(1));
    await waitFor(() => expect(within(host!).getByRole('button', { name: '調理開始' })).toBeTruthy());
  });

  it('previews a manager adjustment on the server and confirms only after the preview', async () => {
    const order = staffOrder({ payment_status: 'PAID', payment_method: 'STRIPE', payment_review_required: true, review_decision: 'UNRESOLVED', lines: [{ product_name: '餃子', quantity: 1, unit_gross_jpy: '500', line_gross_jpy: '500', options: [], line_index: 0, original_quantity: 1 }] });
    let previewBody = '';
    let adjustBody = '';
    const routes: Route = async (url, init) => {
      if (url === `${base}/operations/access`) return json({ can_cash: false, can_serve: false, can_kitchen: false, can_manage: true, can_approve_fulfillment: true });
      if (url === `${base}/operations/reviews`) return json({ orders: [order] });
      if (url.endsWith('/adjustments/preview')) { previewBody = String(init.body); return json({ expected_version: 3, original_total_gross_jpy: '1210', effective_total_gross_jpy: '710', refund_amount_jpy: '500', whole_order_cancel: false, lines: [] }); }
      if (url.endsWith('/adjustments')) { adjustBody = String(init.body); return json({ ...order, version: 4, effective_total_gross_jpy: '710', payment_status: 'PAID', refunds: [{ id: 'refund-1', amount_jpy: '500', status: 'PROCESSING', method: 'STRIPE' }] }); }
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    const user = userEvent.setup();
    await user.click(within(host!).getByRole('button', { name: '要確認' }));
    await waitFor(() => expect(host?.textContent).toContain('餃子'));
    await user.click(within(host!).getByRole('button', { name: '取消・減量' }));
    const dialog = host?.querySelector<HTMLDialogElement>('dialog[open]');
    const qty = within(dialog!).getAllByRole('spinbutton')[0]!;
    await user.clear(qty); await user.type(qty, '1');
    await user.type(within(dialog!).getByLabelText('取消理由（必須）'), '誤注文');
    await user.click(within(dialog!).getByRole('button', { name: '変更内容を確認' }));
    await waitFor(() => expect(host?.textContent).toContain('返金額'));
    expect(JSON.parse(previewBody)).toEqual({ expected_version: 3, reason: '誤注文', lines: [{ line_index: 0, cancelled_quantity: 1 }], cash_hand_back_confirmed: false });
    await user.click(within(dialog!).getByRole('button', { name: '変更を確定' }));
    await waitFor(() => expect(adjustBody).not.toBe(''));
    expect(JSON.parse(adjustBody)).toEqual(JSON.parse(previewBody));
  });

  it('refreshes the review queue and navigation count from the shared refresh button', async () => {
    const first = staffOrder({ payment_status: 'PAID', payment_review_required: true, review_decision: 'UNRESOLVED', review_reason_codes: ['PAYMENT_REVIEW'] });
    const arriving = staffOrder({ id: 'order-2', payment_status: 'PAID', payment_review_required: true, review_decision: 'UNRESOLVED', review_reason_codes: ['OPERATIONAL_HOLD'], lines: [{ product_name: '餃子', quantity: 1, unit_gross_jpy: '500', line_gross_jpy: '500', options: [] }] });
    let reviewCalls = 0;
    const routes: Route = async (url) => {
      if (url === `${base}/operations/access`) return json({ can_cash: false, can_serve: false, can_kitchen: false, can_manage: false, can_approve_fulfillment: true });
      if (url === `${base}/operations/reviews`) { reviewCalls++; return json({ orders: reviewCalls === 1 ? [first] : [first, arriving] }); }
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    const user = userEvent.setup();
    await user.click(within(host!).getByRole('button', { name: '要確認' }));
    await waitFor(() => expect(within(host!).getByRole('button', { name: '要確認 · 1' })).toBeTruthy());
    expect(host?.textContent).not.toContain('餃子');

    await user.click(within(host!.querySelector('nav')!).getByRole('button', { name: '最新状態に更新' }));

    await waitFor(() => expect(within(host!).getByRole('button', { name: '要確認 · 2' })).toBeTruthy());
    expect(host?.textContent).toContain('餃子');
    expect(reviewCalls).toBe(2);
  });

  it('keeps a fully cancelled failed-refund order recoverable without review or adjustment actions', async () => {
    const order = staffOrder({ payment_method: 'STRIPE', payment_status: 'REFUND_PENDING', fulfillment_status: 'CANCELLED', payment_review_required: false, review_decision: 'CANCELLED', review_reason_codes: ['REFUND_UNRESOLVED'], total_gross_jpy: '500', effective_total_gross_jpy: '0', lines: [{ product_name: '餃子', quantity: 0, unit_gross_jpy: '500', line_gross_jpy: '0', options: [] }], refunds: [{ id: 'refund-failed', amount_jpy: '500', method: 'STRIPE', status: 'FAILED' }] });
    let retryCalls = 0;
    const routes: Route = async (url, init) => {
      if (url === `${base}/operations/access`) return json({ can_cash: false, can_serve: false, can_kitchen: false, can_manage: false, can_approve_fulfillment: true });
      if (url === `${base}/operations/reviews`) return json({ orders: [order] });
      if (url.endsWith('/refunds/refund-failed/retry')) { retryCalls++; expect(JSON.parse(String(init.body))).toEqual({ expected_version: order.version }); return json(order); }
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    const user = userEvent.setup();
    await user.click(within(host!).getByRole('button', { name: '要確認' }));
    await waitFor(() => expect(host?.textContent).toContain('返金未完了'));
    const queue = host!.querySelector<HTMLElement>('.review-queue')!;
    expect(queue.textContent).toContain('変更後合計');
    expect(queue.textContent).toContain('¥0');
    expect(queue.textContent).toContain('元の合計');
    expect(queue.textContent).toContain('¥500');
    expect(queue.textContent).toContain('失敗');
    expect(within(queue).queryByRole('button', { name: '提供を承認' })).toBeNull();
    expect(within(queue).queryByRole('button', { name: '取消・減量' })).toBeNull();
    await user.click(within(queue).getByRole('button', { name: '返金を再試行' }));
    await waitFor(() => expect(retryCalls).toBe(1));
  });

  it('renders paid review orders and retries an uncertain approval with the same key and body', async () => {
    const order = staffOrder({ payment_status: 'PAID', payment_review_required: true, review_decision: 'UNRESOLVED', review_reason_codes: ['COMPANY_HOLD', 'SESSION_CLOSED'] });
    let postCalls = 0;
    const routes: Route = async (url) => {
      if (url === `${base}/operations/access`) return json({ can_cash: false, can_serve: false, can_kitchen: false, can_manage: false, can_approve_fulfillment: true });
      if (url === `${base}/operations/reviews`) return json({ orders: postCalls > 1 ? [] : [order] });
      if (url.endsWith('/approve-fulfillment')) {
        postCalls++;
        if (postCalls === 1) return json({ error: 'uncertain' }, 502);
        return json({ ...order, payment_review_required: false, review_decision: 'APPROVED', fulfillment_status: 'ACCEPTED', version: 4 });
      }
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    const user = userEvent.setup();
    await user.click(within(host!).getByRole('button', { name: '要確認' }));
    await waitFor(() => expect(host?.textContent).toContain('企業の商品保留'));
    expect(host?.textContent).toContain('企業の商品保留');
    expect(host?.textContent).toContain('1,210');
    expect(host?.textContent).not.toContain('stripe');
    await user.click(within(host!).getByRole('button', { name: '提供を承認' }));
    await user.type(within(host!).getByLabelText('承認理由'), '商品保留解除を確認');
    await user.click(within(host!).getByRole('button', { name: '承認して厨房へ送る' }));
    const approvalDialog = host?.querySelector<HTMLDialogElement>('dialog[open]');
    await waitFor(() => expect(within(approvalDialog!).getByRole('button', { name: '同じ操作を確認して再試行' })).toBeTruthy());
    const firstCall = vi.mocked(fetch).mock.calls.find(([input]) => String(input).endsWith('/approve-fulfillment'));
    await user.click(within(approvalDialog!).getByRole('button', { name: '同じ操作を確認して再試行' }));
    const approvalCalls = vi.mocked(fetch).mock.calls.filter(([input]) => String(input).endsWith('/approve-fulfillment'));
    expect(approvalCalls).toHaveLength(2);
    expect(new Headers(approvalCalls[0]?.[1]?.headers).get('Idempotency-Key')).toBe(new Headers(approvalCalls[1]?.[1]?.headers).get('Idempotency-Key'));
    expect(approvalCalls[0]?.[1]?.body).toBe(approvalCalls[1]?.[1]?.body);
    expect(String(approvalCalls[0]?.[1]?.body)).toContain('商品保留解除を確認');
    expect(firstCall).toBeDefined();
    expect(host?.textContent).toContain('確認が必要な注文はありません');
  });

  it('reloads the review queue after a stale approval version', async () => {
    const order = staffOrder({ payment_status: 'PAID', payment_review_required: true, review_decision: 'UNRESOLVED', review_reason_codes: ['PAYMENT_REVIEW'] });
    let postCalls = 0;
    let reviewCalls = 0;
    const posts: Array<{ body: string; key: string | null }> = [];
    const routes: Route = async (url, init) => {
      if (url === `${base}/operations/access`) return json({ can_cash: false, can_serve: false, can_kitchen: false, can_manage: false, can_approve_fulfillment: true });
      if (url === `${base}/operations/reviews`) { reviewCalls++; return json({ orders: [reviewCalls === 1 ? order : { ...order, version: 4 }] }); }
      if (url.endsWith('/approve-fulfillment')) { postCalls++; posts.push({ body: String(init.body), key: new Headers(init.headers).get('Idempotency-Key') }); return postCalls === 1 ? json({ error: 'stale_version' }, 409) : json({ ...order, version: 5, review_decision: 'APPROVED' }); }
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    const user = userEvent.setup();
    await user.click(within(host!).getByRole('button', { name: '要確認' }));
    await waitFor(() => expect(host?.textContent).toContain('支払い確認'));
    await user.click(within(host!).getByRole('button', { name: '提供を承認' }));
    await user.type(within(host!).getByLabelText('承認理由'), '最新状態を確認');
    await user.click(within(host!).getByRole('button', { name: '承認して厨房へ送る' }));
    expect(postCalls).toBe(1);
    await waitFor(() => expect(host?.textContent).toContain('最新状態を読み込みました'));
    expect(host?.querySelector('dialog[open]')).toBeNull();
    await user.click(within(host!).getByRole('button', { name: '提供を承認' }));
    await user.type(within(host!).getByLabelText('承認理由'), '再確認後に承認');
    await user.click(within(host!).getByRole('button', { name: '承認して厨房へ送る' }));
    expect(postCalls).toBe(2);
    expect(JSON.parse(posts[1]!.body)).toEqual({ expected_version: 4, reason: '再確認後に承認' });
    expect(posts[0]!.key).not.toBe(posts[1]!.key);
    expect(reviewCalls).toBe(3);
  });

  it('shows the queue read-only to staff and never renders an approval action', async () => {
    const order = staffOrder({ payment_status: 'PAID', payment_review_required: true, review_decision: 'UNRESOLVED', review_reason_codes: ['OPERATIONAL_HOLD'] });
    const routes: Route = async (url) => {
      if (url === `${base}/operations/access`) return json({ can_cash: true, can_serve: false, can_kitchen: false, can_manage: false });
      if (url === `${base}/operations/tables`) return json({ tables: [table] });
      if (url === `${base}/operations/reviews`) return json({ orders: [order] });
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    const user = userEvent.setup();
    await user.click(within(host!).getByRole('button', { name: '要確認' }));
    await waitFor(() => expect(host?.textContent).toContain('運営保留'));
    expect(within(host!).queryByRole('button', { name: '提供を承認' })).toBeNull();
  });

  it('drops a late review response after staff disconnects', async () => {
    const response = deferred<Response>();
    const order = staffOrder({ payment_status: 'PAID', payment_review_required: true, review_decision: 'UNRESOLVED', review_reason_codes: ['PAYMENT_REVIEW'] });
    const routes: Route = async (url) => {
      if (url === `${base}/operations/access`) return json({ can_cash: true, can_serve: false, can_kitchen: false, can_manage: false });
      if (url === `${base}/operations/tables`) return json({ tables: [table] });
      if (url === `${base}/operations/reviews`) return response.promise;
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    const user = userEvent.setup();
    await user.click(within(host!).getByRole('button', { name: '要確認' }));
    await user.click(within(host!).getByRole('button', { name: '切断' }));
    await act(async () => { response.resolve(json({ orders: [order] })); await new Promise((resolve) => setTimeout(resolve, 0)); });
    expect(host?.textContent).toContain('接続');
    expect(host?.textContent).not.toContain('ラーメン');
  });

  it('keeps the document language in sync with the selected staff locale', async () => {
    const routes: Route = async (url) => {
      if (url === `${base}/operations/access`) return json({ can_cash: false, can_serve: true, can_kitchen: false, can_manage: false });
      if (url === `${base}/operations/tables`) return json({ tables: [table] });
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    expect(document.documentElement.lang).toBe('ja');
    await click(host!, '繁體中文');
    expect(document.documentElement.lang).toBe('zh-Hant');
    await click(host!, '日本語');
    expect(document.documentElement.lang).toBe('ja');
    await click(host!, '切断');
    await click(host!, '繁體中文');
    expect(document.documentElement.lang).toBe('zh-Hant');
  });

  it('shows KDS poll failures as stale and clears the banner after recovery', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let kdsCalls = 0;
    const routes: Route = async (url) => {
      if (url === `${base}/operations/access`) return json({ can_cash: false, can_serve: false, can_kitchen: true, can_manage: false });
      if (url === `${base}/operations/kds`) { kdsCalls += 1; return kdsCalls === 2 ? json({ error: 'unavailable' }, 502) : json({ orders: [] }); }
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    expect(kdsCalls).toBe(1);
    expect(document.visibilityState).toBe('visible');
    await act(async () => { await vi.advanceTimersByTimeAsync(2500); });
    const stale = host?.querySelector('p.kds-stale[role="status"]');
    expect(stale?.textContent).toContain('厨房看板の接続が切れています');
    expect(stale?.textContent).toContain('最終同期:');
    await click(host!, '繁體中文');
    expect(host?.querySelector('p.kds-stale[role="status"]')?.textContent).toContain('廚房看板連線中斷');
    await act(async () => { await vi.advanceTimersByTimeAsync(2500); });
    expect(host?.querySelector('p.kds-stale[role="status"]')).toBeNull();
    expect(kdsCalls).toBe(3);
  });

  it('does not overlap a pending KDS poll and reports its failure before recovery', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const pendingPoll = deferred<Response>();
    let kdsCalls = 0;
    const routes: Route = async (url) => {
      if (url === `${base}/operations/access`) return json({ can_cash: false, can_serve: false, can_kitchen: true, can_manage: false });
      if (url === `${base}/operations/kds`) { kdsCalls += 1; return kdsCalls === 2 ? pendingPoll.promise : json({ orders: [] }); }
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    expect(kdsCalls).toBe(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(2500); });
    expect(kdsCalls).toBe(2);
    await act(async () => { await vi.advanceTimersByTimeAsync(2500); });
    expect(kdsCalls).toBe(2);
    pendingPoll.resolve(json({ error: 'unavailable' }, 502));
    await act(async () => { await pendingPoll.promise; await new Promise((resolve) => setTimeout(resolve, 0)); });
    expect(host?.querySelector('p.kds-stale[role="status"]')?.textContent).toContain('厨房看板の接続が切れています');
    await act(async () => { await vi.advanceTimersByTimeAsync(2500); });
    expect(kdsCalls).toBe(3);
    expect(host?.querySelector('p.kds-stale[role="status"]')).toBeNull();
  });

  it('marks the board stale and resumes polling when a KDS request hangs', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let kdsCalls = 0;
    let hungSignal: AbortSignal | undefined;
    const routes: Route = async (url, init) => {
      if (url === `${base}/operations/access`) return json({ can_cash: false, can_serve: false, can_kitchen: true, can_manage: false });
      if (url === `${base}/operations/kds`) {
        kdsCalls += 1;
        if (kdsCalls === 2) { hungSignal = init.signal ?? undefined; return new Promise<Response>((_resolve, reject) => init.signal?.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')))); }
        return kdsCalls === 3 ? json({ error: 'unavailable' }, 502) : json({ orders: [] }); }
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    await act(async () => { await vi.advanceTimersByTimeAsync(2500); });
    expect(kdsCalls).toBe(2);
    await act(async () => { await vi.advanceTimersByTimeAsync(7500); });
    expect(kdsCalls).toBe(2);
    expect(hungSignal?.aborted).toBe(false);
    expect(host?.querySelector('p.kds-stale[role="status"]')).toBeNull();
    await act(async () => { await vi.advanceTimersByTimeAsync(2500); });
    expect(hungSignal?.aborted).toBe(true);
    expect(host?.querySelector('p.kds-stale[role="status"]')?.textContent).toContain('厨房看板の接続が切れています');
    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    expect(kdsCalls).toBeGreaterThanOrEqual(4);
    expect(host?.querySelector('p.kds-stale[role="status"]')).toBeNull();
  });

  it('does not start a KDS poll while the initial board load is still pending', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const initialLoad = deferred<Response>();
    let kdsCalls = 0;
    const routes: Route = async (url) => {
      if (url === `${base}/operations/access`) return json({ can_cash: false, can_serve: false, can_kitchen: true, can_manage: false });
      if (url === `${base}/operations/kds`) { kdsCalls += 1; return kdsCalls === 1 ? initialLoad.promise : json({ orders: [] }); }
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    expect(kdsCalls).toBe(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(2500); });
    expect(kdsCalls).toBe(1);
    initialLoad.resolve(json({ orders: [staffOrder({ id: 'kds-1', fulfillment_status: 'ACCEPTED' })] }));
    await act(async () => { await initialLoad.promise; await new Promise((resolve) => setTimeout(resolve, 0)); });
    expect(host?.textContent).toContain('kds-1');
    await act(async () => { await vi.advanceTimersByTimeAsync(2500); });
    expect(kdsCalls).toBe(2);
  });

  it('keeps the KDS poll guard when the operator leaves and returns to the board', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const pendingPoll = deferred<Response>();
    let kdsCalls = 0;
    const routes: Route = async (url) => {
      if (url === `${base}/operations/access`) return json({ can_cash: false, can_serve: true, can_kitchen: true, can_manage: false });
      if (url === `${base}/operations/tables`) return json({ tables: [table] });
      if (url === `${base}/operations/kds`) { kdsCalls += 1; return kdsCalls === 2 ? pendingPoll.promise : json({ orders: [] }); }
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    await click(host!, '厨房看板');
    await act(async () => { await vi.advanceTimersByTimeAsync(2500); });
    expect(kdsCalls).toBe(2);
    await click(host!, 'テーブル');
    await click(host!, '厨房看板');
    await act(async () => { await vi.advanceTimersByTimeAsync(2500); });
    expect(kdsCalls).toBe(2);
    pendingPoll.resolve(json({ orders: [] }));
    await act(async () => { await pendingPoll.promise; await new Promise((resolve) => setTimeout(resolve, 0)); });
    await act(async () => { await vi.advanceTimersByTimeAsync(2500); });
    expect(kdsCalls).toBe(3);
  });

  it('reports a pending KDS poll failure that settles after the operator returns to the board', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const pendingPoll = deferred<Response>();
    let kdsCalls = 0;
    const routes: Route = async (url) => {
      if (url === `${base}/operations/access`) return json({ can_cash: false, can_serve: true, can_kitchen: true, can_manage: false });
      if (url === `${base}/operations/tables`) return json({ tables: [table] });
      if (url === `${base}/operations/kds`) { kdsCalls += 1; return kdsCalls === 2 ? pendingPoll.promise : json({ orders: [] }); }
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    await click(host!, '厨房看板');
    await act(async () => { await vi.advanceTimersByTimeAsync(2500); });
    expect(kdsCalls).toBe(2);
    await click(host!, 'テーブル');
    await click(host!, '厨房看板');
    pendingPoll.resolve(json({ error: 'unavailable' }, 502));
    await act(async () => { await pendingPoll.promise; await new Promise((resolve) => setTimeout(resolve, 0)); });
    expect(host?.querySelector('p.kds-stale[role="status"]')?.textContent).toContain('厨房看板の接続が切れています');
  });

  it('dismisses the close dialog on cancel and restores focus to its opener', async () => {
    const routes: Route = async (url) => {
      if (url === `${base}/operations/access`) return json({ can_cash: false, can_serve: true, can_kitchen: false, can_manage: false });
      if (url === `${base}/operations/tables`) return json({ tables: [table] });
      if (url === `${base}/tables/table-1/sessions/session-1/operations`) return json({ session: { id: 'session-1', status: 'OPEN', mode: 'DIRECT_SCAN' }, orders: [], receipts: [], close_blockers: [] });
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    await click(host!, '詳細');
    const opener = [...host!.querySelectorAll('button')].find((button) => button.textContent?.includes('テーブルを閉じる'));
    expect(opener).toBeTruthy();
    await click(host!, 'テーブルを閉じる');
    const dialog = host!.querySelector<HTMLDialogElement>('dialog[open]');
    expect(dialog).toBeTruthy();
    expect(dialog?.getAttribute('aria-labelledby')).toBe('close-title');
    await act(async () => { dialog?.dispatchEvent(new Event('cancel', { cancelable: true })); await new Promise((resolve) => setTimeout(resolve, 0)); });
    expect(host?.querySelector('dialog[open]')).toBeNull();
    expect(document.activeElement).toBe(opener);
  });

  it('keeps the cash dialog open when cancel is pressed after an uncertain settlement', async () => {
    const order = staffOrder({ payment_method: 'CASH', payment_status: 'UNPAID', fulfillment_status: 'ACCEPTED' });
    const routes: Route = async (url) => {
      if (url === `${base}/operations/access`) return json({ can_cash: true, can_serve: true, can_kitchen: false, can_manage: false });
      if (url === `${base}/operations/tables`) return json({ tables: [table] });
      if (url === `${base}/tables/table-1/sessions/session-1/operations`) return json({ session: { id: 'session-1', status: 'OPEN', mode: 'DIRECT_SCAN' }, orders: [order], receipts: [], close_blockers: [] });
      if (url === `${base}/tables/table-1/sessions/session-1/cash/preview`) return json({ session_id: 'session-1', fingerprint: 'cash-snapshot-1', order_ids: ['order-1'], due_jpy: '1100', tax: { gross_jpy: '1100', net_jpy: '1000', tax_jpy: '100', groups: [] } });
      if (url === `${base}/tables/table-1/sessions/session-1/cash/settle`) return json({ error: 'unknown_result' }, 502);
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    await click(host!, '詳細');
    await click(host!, '現金を精算');
    await setField(host!, 'お預かり金額', '1100');
    await setField(host!, 'レジ精算番号', 'reg-42');
    await click(host!, '現金を確認して精算');
    const dialog = host!.querySelector<HTMLDialogElement>('dialog[open]');
    expect(dialog).toBeTruthy();
    const cancel = new Event('cancel', { cancelable: true });
    await act(async () => { dialog?.dispatchEvent(cancel); await new Promise((resolve) => setTimeout(resolve, 0)); });
    expect(cancel.defaultPrevented).toBe(true);
    expect(host?.querySelector('dialog[open]')).toBe(dialog);
    await act(async () => { dialog?.close(); await new Promise((resolve) => setTimeout(resolve, 0)); });
    expect(host?.querySelector('dialog[open]')).toBe(dialog);
    const repeatedCancel = new Event('cancel', { cancelable: true });
    await act(async () => { dialog?.dispatchEvent(repeatedCancel); await new Promise((resolve) => setTimeout(resolve, 0)); });
    expect(repeatedCancel.defaultPrevented).toBe(true);
    expect(host?.querySelector('dialog[open]')).toBe(dialog);
  });

  it('keeps the cash dialog open when cancel is pressed while settlement is pending', async () => {
    const settlement = deferred<Response>();
    const order = staffOrder({ payment_method: 'CASH', payment_status: 'UNPAID', fulfillment_status: 'ACCEPTED' });
    const routes: Route = async (url) => {
      if (url === `${base}/operations/access`) return json({ can_cash: true, can_serve: true, can_kitchen: false, can_manage: false });
      if (url === `${base}/operations/tables`) return json({ tables: [table] });
      if (url === `${base}/tables/table-1/sessions/session-1/operations`) return json({ session: { id: 'session-1', status: 'OPEN', mode: 'DIRECT_SCAN' }, orders: [order], receipts: [], close_blockers: [] });
      if (url === `${base}/tables/table-1/sessions/session-1/cash/preview`) return json({ session_id: 'session-1', fingerprint: 'cash-snapshot-1', order_ids: ['order-1'], due_jpy: '1100', tax: { gross_jpy: '1100', net_jpy: '1000', tax_jpy: '100', groups: [] } });
      if (url === `${base}/tables/table-1/sessions/session-1/cash/settle`) return settlement.promise;
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    await click(host!, '詳細');
    await click(host!, '現金を精算');
    await setField(host!, 'お預かり金額', '1100');
    await setField(host!, 'レジ精算番号', 'reg-42');
    await click(host!, '現金を確認して精算');
    const dialog = host!.querySelector<HTMLDialogElement>('dialog[open]');
    expect(dialog).toBeTruthy();
    const cancel = new Event('cancel', { cancelable: true });
    await act(async () => { dialog?.dispatchEvent(cancel); await new Promise((resolve) => setTimeout(resolve, 0)); });
    expect(cancel.defaultPrevented).toBe(true);
    expect(host?.querySelector('dialog[open]')).toBe(dialog);
    await act(async () => { dialog?.close(); await new Promise((resolve) => setTimeout(resolve, 0)); });
    expect(host?.querySelector('dialog[open]')).toBe(dialog);
    const repeatedCancel = new Event('cancel', { cancelable: true });
    await act(async () => { dialog?.dispatchEvent(repeatedCancel); await new Promise((resolve) => setTimeout(resolve, 0)); });
    expect(repeatedCancel.defaultPrevented).toBe(true);
    expect(host?.querySelector('dialog[open]')).toBe(dialog);
    settlement.resolve(json({ error: 'unknown_result' }, 502));
    await act(async () => { await settlement.promise; await new Promise((resolve) => setTimeout(resolve, 0)); });
  });

  it('restores cash focus to its opener after the preview finishes loading', async () => {
    const preview = deferred<Response>();
    const order = staffOrder({ payment_method: 'CASH', payment_status: 'UNPAID', fulfillment_status: 'ACCEPTED' });
    const routes: Route = async (url) => {
      if (url === `${base}/operations/access`) return json({ can_cash: true, can_serve: true, can_kitchen: false, can_manage: false });
      if (url === `${base}/operations/tables`) return json({ tables: [table] });
      if (url === `${base}/tables/table-1/sessions/session-1/operations`) return json({ session: { id: 'session-1', status: 'OPEN', mode: 'DIRECT_SCAN' }, orders: [order], receipts: [], close_blockers: [] });
      if (url === `${base}/tables/table-1/sessions/session-1/cash/preview`) return preview.promise;
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    await click(host!, '詳細');
    const opener = [...host!.querySelectorAll('button')].find((button) => button.textContent?.includes('現金を精算'));
    expect(opener).toBeTruthy();
    await click(host!, '現金を精算');
    expect(opener?.disabled).toBe(true);
    preview.resolve(json({ session_id: 'session-1', fingerprint: 'cash-snapshot-1', order_ids: ['order-1'], due_jpy: '1100', tax: { gross_jpy: '1100', net_jpy: '1000', tax_jpy: '100', groups: [] } }));
    await act(async () => { await preview.promise; await new Promise((resolve) => setTimeout(resolve, 0)); });
    const dialog = host!.querySelector<HTMLDialogElement>('dialog[open]');
    expect(dialog).toBeTruthy();
    expect(opener?.disabled).toBe(false);
    await act(async () => { dialog?.dispatchEvent(new Event('cancel', { cancelable: true })); await new Promise((resolve) => setTimeout(resolve, 0)); });
    expect(host?.querySelector('dialog[open]')).toBeNull();
    expect(document.activeElement).toBe(opener);
  });

  it('shows demo identity and opens the KDS view when launched from the kitchen route', async () => {
    const routes: Route = async (url) => {
      if (url === `${base}/operations/access`) return json({ can_cash: true, can_serve: true, can_kitchen: true, can_manage: false });
      if (url === `${base}/operations/tables`) return json({ tables: [] });
      if (url === `${base}/operations/kds`) return json({ orders: [] });
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes, { demo: true, initialView: 'kds' });

    expect(host?.textContent).toContain('博多餃子酒場 シロナガスクジラ');
    expect(host?.textContent).toContain('参考価格 · 現行店内価格未確認');
    expect(host?.textContent).toContain('厨房看板');
  });

  it('allows a corrected token after a rejected login instead of leaving the form busy', async () => {
    let accessCalls = 0;
    const accessTokens: string[] = [];
    const routes: Route = async (url, init) => {
      if (url === `${base}/operations/access`) {
        accessCalls += 1;
        accessTokens.push(new Headers(init.headers).get('Authorization') ?? '');
        if (accessCalls === 1) return json({ error: 'unauthorized' }, 401);
        return json({ can_cash: false, can_serve: true, can_kitchen: false, can_manage: false });
      }
      if (url === `${base}/operations/tables`) return json({ tables: [table] });
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    expect(host?.textContent).toContain('スタッフ権限を確認できません');
    const connectButton = [...host!.querySelectorAll('button')].find((button) => button.textContent?.includes('接続'));
    expect(connectButton?.disabled).toBe(false);
    await setField(host!, 'スタッフアクセストークン', 'corrected-token');
    await click(host!, '接続');
    expect(accessCalls).toBe(2);
    expect(accessTokens).toEqual(['Bearer mock-token-unit-test-only', 'Bearer corrected-token']);
    expect(host?.textContent).toContain('T1');
  });

  it('previews exact cash due and retries a 502 settlement with the frozen request once explicitly', async () => {
    const order = staffOrder({ id: 'cash-1', payment_method: 'CASH', payment_status: 'UNPAID', fulfillment_status: 'ACCEPTED' });
    let settleCalls: Array<{ body: string; key: string | null }> = [];
    const routes: Route = async (url, init) => {
      if (url === `${base}/operations/access`) return json({ can_cash: true, can_serve: true, can_kitchen: true, can_manage: false });
      if (url === `${base}/operations/tables`) return json({ tables: [table] });
      if (url === `${base}/tables/table-1/sessions/session-1/operations`) return json({ session: { id: 'session-1', status: 'OPEN', mode: 'DIRECT_SCAN' }, orders: [order], receipts: [], close_blockers: [] });
      if (url === `${base}/tables/table-1/sessions/session-1/cash/preview`) return json({ session_id: 'session-1', fingerprint: 'cash-snapshot-1', order_ids: ['cash-1'], due_jpy: '1100', tax: { gross_jpy: '1100', net_jpy: '1000', tax_jpy: '100', groups: [] } });
      if (url === `${base}/tables/table-1/sessions/session-1/cash/settle`) {
        settleCalls.push({ body: String(init.body), key: new Headers(init.headers).get('Idempotency-Key') });
        if (settleCalls.length === 1) return json({ error: 'temporary_failure' }, 502);
        return json({ id: 'receipt-1', session_id: 'session-1', order_ids: ['cash-1'], due_jpy: '1100', tendered_jpy: '1500', change_jpy: '400', cashier_id: 'staff-1', register_session_ref: 'reg-42', created_at: '2026-10-18T01:00:00Z', tax: { gross_jpy: '1100', net_jpy: '1000', tax_jpy: '100', groups: [] } });
      }
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    expect(host?.textContent).toContain('T1');
    await click(host!, '詳細');
    await click(host!, '現金を精算');
    expect(host?.textContent).toContain('¥1,100');
    expect(host?.textContent).toContain('cash-1');
    await setField(host!, 'お預かり金額', '1500');
    await setField(host!, 'レジ精算番号', 'reg-42');
    await click(host!, '現金を確認して精算');
    expect(host?.textContent).toContain('同じ操作を確認して再試行');
    await click(host!, '同じ操作を確認して再試行');
    expect(settleCalls).toHaveLength(2);
    expect(settleCalls[0]?.key).toBeTruthy();
    expect(settleCalls[1]?.key).toBe(settleCalls[0]?.key);
    expect(settleCalls[1]?.body).toBe(settleCalls[0]?.body);
    expect(JSON.parse(settleCalls[0]!.body)).toEqual({ fingerprint: 'cash-snapshot-1', tendered_jpy: '1500', register_session_ref: 'reg-42' });
    expect(host?.textContent).toContain('receipt-1');
    expect(host?.textContent).toContain('¥400');
  });

  it('blocks close when the server lists an unserved order and sends no close request', async () => {
    let closeCalls = 0;
    const order = staffOrder({ fulfillment_status: 'READY', payment_status: 'PAID', payment_method: 'STRIPE' });
    const routes: Route = async (url) => {
      if (url === `${base}/operations/access`) return json({ can_cash: true, can_serve: true, can_kitchen: false, can_manage: true, can_approve_fulfillment: true });
      if (url === `${base}/operations/tables`) return json({ tables: [table] });
      if (url === `${base}/operations/cash-settings`) return json({ enabled: true, mode: 'PAY_LATER', version: 2 });
      if (url === `${base}/tables/table-1/sessions/session-1/operations`) return json({ session: { id: 'session-1', status: 'OPEN', mode: 'STAFF_OPEN' }, orders: [order], receipts: [], close_blockers: ['UNSERVED_ORDER'] });
      if (url.endsWith('/close')) { closeCalls += 1; return json({ closed: true }); }
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    await click(host!, '詳細');
    await click(host!, 'テーブルを閉じる');
    expect(host?.textContent).toContain('まだ提供していない注文があります。');
    const confirm = [...host!.querySelectorAll('button')].find((button) => button.textContent?.includes('閉卓を確認'));
    expect(confirm?.disabled).toBe(true);
    expect(closeCalls).toBe(0);
  });

  it('explains an operational hold in Japanese and Chinese and keeps close blocked', async () => {
    const routes: Route = async (url) => {
      if (url === `${base}/operations/access`) return json({ can_cash: false, can_serve: true, can_kitchen: false, can_manage: false });
      if (url === `${base}/operations/tables`) return json({ tables: [table] });
      if (url === `${base}/tables/table-1/sessions/session-1/operations`) return json({ session: { id: 'session-1', status: 'OPEN', mode: 'STAFF_OPEN' }, orders: [], receipts: [], close_blockers: ['OPERATION_HOLD'] });
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    await click(host!, '詳細');
    await click(host!, 'テーブルを閉じる');
    expect(host?.textContent).toContain('運営保留中のため、責任者の確認が必要です。');
    expect(host?.textContent).not.toContain('OPERATION_HOLD');
    const confirm = [...host!.querySelectorAll('button')].find((button) => button.textContent?.includes('閉卓を確認'));
    expect(confirm?.disabled).toBe(true);
    await click(host!, '繁體中文');
    expect(host?.textContent).toContain('此桌次目前由營運保留，請由負責人確認後再關桌。');
  });

  it('describes unpaid or unknown payment as a payment hold, not a cash order', async () => {
    const routes: Route = async (url) => {
      if (url === `${base}/operations/access`) return json({ can_cash: false, can_serve: true, can_kitchen: false, can_manage: false });
      if (url === `${base}/operations/tables`) return json({ tables: [table] });
      if (url === `${base}/tables/table-1/sessions/session-1/operations`) return json({ session: { id: 'session-1', status: 'OPEN', mode: 'STAFF_OPEN' }, orders: [], receipts: [], close_blockers: ['UNPAID_OR_UNKNOWN_PAYMENT'] });
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    await click(host!, '詳細');
    await click(host!, 'テーブルを閉じる');
    expect(host?.textContent).toContain('未確認または未精算の支払いがあります。');
    expect(host?.textContent).not.toContain('未精算の現金注文があります。');
    expect(host?.textContent).not.toContain('UNPAID_OR_UNKNOWN_PAYMENT');
  });

  it('allows a cashier with service capability to close an unblocked session', async () => {
    let closeCalls = 0;
    const routes: Route = async (url) => {
      if (url === `${base}/operations/access`) return json({ can_cash: true, can_serve: true, can_kitchen: false, can_manage: false });
      if (url === `${base}/operations/tables`) return json({ tables: [table] });
      if (url === `${base}/tables/table-1/sessions/session-1/operations`) return json({ session: { id: 'session-1', status: 'OPEN', mode: 'STAFF_OPEN' }, orders: [staffOrder({ fulfillment_status: 'SERVED', payment_method: 'STRIPE', payment_status: 'PAID' })], receipts: [], close_blockers: [] });
      if (url.endsWith('/close')) { closeCalls += 1; return json({ closed: true }); }
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    await click(host!, '詳細');
    expect(host?.textContent).toContain('提供済み');
    await click(host!, 'テーブルを閉じる');
    const confirm = [...host!.querySelectorAll('button')].find((button) => button.textContent?.includes('閉卓を確認'));
    expect(confirm?.disabled).toBe(false);
    await click(host!, '閉卓を確認');
    expect(closeCalls).toBe(1);
  });

  it('does not expose session close to a tenant admin without service capability', async () => {
    let closeCalls = 0;
    const routes: Route = async (url) => {
      if (url === `${base}/operations/access`) return json({ can_cash: false, can_serve: false, can_kitchen: false, can_manage: true, can_approve_fulfillment: true });
      if (url === `${base}/operations/tables`) return json({ tables: [table] });
      if (url === `${base}/operations/cash-settings`) return json({ enabled: true, mode: 'PAY_LATER', version: 2 });
      if (url === `${base}/tables/table-1/sessions/session-1/operations`) return json({ session: { id: 'session-1', status: 'OPEN', mode: 'STAFF_OPEN' }, orders: [], receipts: [], close_blockers: [] });
      if (url.endsWith('/close')) { closeCalls += 1; return json({ closed: true }); }
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    await click(host!, '詳細');
    expect(host?.textContent).not.toContain('テーブルを閉じる');
    expect(closeCalls).toBe(0);
  });

  it('lets an authorized cashier read and explicitly reconcile Stripe order status', async () => {
    let statusReads = 0;
    let reconciles = 0;
    const order = staffOrder({ payment_method: 'STRIPE', payment_status: 'PENDING' });
    const routes: Route = async (url, init) => {
      if (url === `${base}/operations/access`) return json({ can_cash: true, can_serve: false, can_kitchen: false, can_manage: false });
      if (url === `${base}/operations/tables`) return json({ tables: [table] });
      if (url === `${base}/tables/table-1/sessions/session-1/operations`) return json({ session: { id: 'session-1', status: 'OPEN', mode: 'DIRECT_SCAN' }, orders: [order], receipts: [], close_blockers: [] });
      if (url.endsWith('/orders/order-1/payment') && init.method !== 'POST') { statusReads++; return json(paymentStatus('OPEN')); }
      if (url.endsWith('/orders/order-1/payment/reconcile') && init.method === 'POST') { reconciles++; return json(paymentStatus('PAID')); }
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    await click(host!, '詳細');
    expect(host?.textContent).toContain('クレジットカード');
    await click(host!, '支払い状況を確認');
    expect(statusReads).toBe(1);
    expect(host?.textContent).toContain('お支払い手続きができます');
    await click(host!, '最新の支払い状況を確認');
    expect(reconciles).toBe(1);
    expect(host?.textContent).toContain('お支払い済み');
  });

  it('does not expose payment reconciliation to a manager-only account', async () => {
    let paymentRequests = 0;
    const order = staffOrder({ payment_method: 'STRIPE', payment_status: 'PENDING' });
    const routes: Route = async (url) => {
      if (url === `${base}/operations/access`) return json({ can_cash: false, can_serve: false, can_kitchen: false, can_manage: true, can_approve_fulfillment: true });
      if (url === `${base}/operations/tables`) return json({ tables: [table] });
      if (url === `${base}/operations/cash-settings`) return json({ enabled: true, mode: 'PAY_LATER', version: 1 });
      if (url === `${base}/tables/table-1/sessions/session-1/operations`) return json({ session: { id: 'session-1', status: 'OPEN', mode: 'DIRECT_SCAN' }, orders: [order], receipts: [], close_blockers: [] });
      if (url.includes('/payment')) { paymentRequests++; return json(paymentStatus('OPEN')); }
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    await click(host!, '詳細');
    expect(host?.textContent).toContain('クレジットカード');
    expect(host?.textContent).not.toContain('支払い状況を確認');
    expect(paymentRequests).toBe(0);
  });

  it('shows an error on the order card when the first payment status read fails', async () => {
    const order = staffOrder({ payment_method: 'STRIPE', payment_status: 'PENDING' });
    const routes: Route = async (url, init) => {
      if (url === `${base}/operations/access`) return json({ can_cash: true, can_serve: false, can_kitchen: false, can_manage: false });
      if (url === `${base}/operations/tables`) return json({ tables: [table] });
      if (url === `${base}/tables/table-1/sessions/session-1/operations`) return json({ session: { id: 'session-1', status: 'OPEN', mode: 'DIRECT_SCAN' }, orders: [order], receipts: [], close_blockers: [] });
      if (url.endsWith('/orders/order-1/payment') && init.method !== 'POST') return json({ error: 'temporarily_unavailable' }, 503);
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    await click(host!, '詳細');
    await click(host!, '支払い状況を確認');
    expect(host?.textContent).toContain('支払い状況を確認できません。時間をおいてお試しください。');
  });

  it('does not show a delayed payment status after staff disconnects', async () => {
    const statusResponse = deferred<Response>();
    const order = staffOrder({ payment_method: 'STRIPE', payment_status: 'PENDING' });
    const routes: Route = async (url, init) => {
      if (url === `${base}/operations/access`) return json({ can_cash: true, can_serve: false, can_kitchen: false, can_manage: false });
      if (url === `${base}/operations/tables`) return json({ tables: [table] });
      if (url === `${base}/tables/table-1/sessions/session-1/operations`) return json({ session: { id: 'session-1', status: 'OPEN', mode: 'DIRECT_SCAN' }, orders: [order], receipts: [], close_blockers: [] });
      if (url.endsWith('/orders/order-1/payment') && init.method !== 'POST') return statusResponse.promise;
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    await click(host!, '詳細');
    await click(host!, '支払い状況を確認');
    await click(host!, '切断');
    statusResponse.resolve(json(paymentStatus('PAID')));
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
    expect(host?.textContent).toContain('接続');
    expect(host?.textContent).not.toContain('お支払い済み');
  });

  it('shows reception only with service capability and requires explicit acceptance for direct-scan orders', async () => {
    let accepted = false;
    let acceptCalls = 0;
    const routes: Route = async (url, init) => {
      if (url === `${base}/operations/access`) return json({ can_cash: false, can_serve: true, can_kitchen: false, can_manage: false });
      if (url === `${base}/operations/tables`) return json({ tables: [table] });
      if (url === `${base}/tables/table-1/sessions/session-1/operations`) return json({ session: { id: 'session-1', status: 'OPEN', mode: 'DIRECT_SCAN' }, orders: [staffOrder({ fulfillment_status: accepted ? 'ACCEPTED' : 'WAITING', requires_staff_acceptance: true, staff_accepted: accepted })], receipts: [], close_blockers: [] });
      if (url.endsWith('/orders/order-1/accept')) {
        acceptCalls += 1; accepted = true;
        expect(new Headers(init.headers).get('Idempotency-Key')).toBeTruthy();
        expect(JSON.parse(String(init.body))).toEqual({ expected_version: 3 });
        return json(staffOrder({ fulfillment_status: 'ACCEPTED', requires_staff_acceptance: true, staff_accepted: true, version: 4 }));
      }
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    expect(host?.textContent).not.toContain('厨房看板');
    await click(host!, '詳細');
    expect(host?.textContent).toContain('受付確認中');
    await click(host!, '注文を受け付ける');
    expect(acceptCalls).toBe(1);
    expect(host?.textContent).not.toContain('注文を受け付ける');
  });

  it('limits a kitchen-only account to one card per order and preparation actions', async () => {
    const orders = [staffOrder({ id: 'order-a', fulfillment_status: 'ACCEPTED' }), staffOrder({ id: 'order-b', fulfillment_status: 'PREPARING' }), staffOrder({ id: 'order-c', fulfillment_status: 'READY' })];
    const routes: Route = async (url) => {
      if (url === `${base}/operations/access`) return json({ can_cash: false, can_serve: false, can_kitchen: true, can_manage: false });
      if (url === `${base}/operations/kds`) return json({ orders });
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    expect(host?.textContent).toContain('厨房看板');
    expect(host?.textContent).not.toContain('桌次');
    expect(host?.querySelectorAll('.staff-order-card')).toHaveLength(3);
    expect(host?.textContent).toContain('ラーメン × 1');
    expect(host?.textContent).toContain('大盛り');
    expect(host?.textContent).toContain('調理開始');
    expect(host?.textContent).toContain('調理完了');
    expect(host?.textContent).not.toContain('提供済みにする');
    expect(host?.textContent).not.toContain('¥1,210');
  });

  it('does not let a delayed table A response overwrite the newer table B selection', async () => {
    const tableA = { ...table, table_id: 'table-a', name: 'T-A', session_id: 'session-a' };
    const tableB = { ...table, table_id: 'table-b', name: 'T-B', session_id: 'session-b' };
    const responseA = deferred<Response>();
    const responseB = deferred<Response>();
    const routes: Route = async (url) => {
      if (url === `${base}/operations/access`) return json({ can_cash: false, can_serve: true, can_kitchen: false, can_manage: false });
      if (url === `${base}/operations/tables`) return json({ tables: [tableA, tableB] });
      if (url === `${base}/tables/table-a/sessions/session-a/operations`) return responseA.promise;
      if (url === `${base}/tables/table-b/sessions/session-b/operations`) return responseB.promise;
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    await clickNth(host!, '.staff-table-card button.button.quiet', 0);
    await clickNth(host!, '.staff-table-card button.button.quiet', 1);
    await act(async () => { responseB.resolve(json({ session: { id: 'session-b', status: 'OPEN', mode: 'DIRECT_SCAN' }, orders: [staffOrder({ id: 'order-b', table_name: 'T-B' })], receipts: [], close_blockers: [] })); await new Promise((resolve) => setTimeout(resolve, 0)); });
    expect(host?.textContent).toContain('order-b');
    await act(async () => { responseA.resolve(json({ session: { id: 'session-a', status: 'OPEN', mode: 'DIRECT_SCAN' }, orders: [staffOrder({ id: 'order-a', table_name: 'T-A' })], receipts: [], close_blockers: [] })); await new Promise((resolve) => setTimeout(resolve, 0)); });
    expect(host?.textContent).toContain('T-B');
    expect(host?.textContent).toContain('order-b');
    expect(host?.textContent).not.toContain('order-a');
  });

  it('clears the authenticated view and ignores a pending detail response after disconnect', async () => {
    const response = deferred<Response>();
    const routes: Route = async (url) => {
      if (url === `${base}/operations/access`) return json({ can_cash: false, can_serve: true, can_kitchen: false, can_manage: false });
      if (url === `${base}/operations/tables`) return json({ tables: [table] });
      if (url === `${base}/tables/table-1/sessions/session-1/operations`) return response.promise;
      throw new Error(`Unexpected request ${url}`);
    };
    await mountStaff(routes);
    await click(host!, '詳細');
    await click(host!, '切断');
    await act(async () => { response.resolve(json({ session: { id: 'session-1', status: 'OPEN', mode: 'DIRECT_SCAN' }, orders: [staffOrder({ id: 'late-order' })], receipts: [], close_blockers: [] })); await new Promise((resolve) => setTimeout(resolve, 0)); });
    expect(host?.textContent).toContain('接続');
    expect(host?.textContent).not.toContain('late-order');
    expect(sessionStorage.getItem('tojima.staff.access-token')).toBeNull();
    expect(sessionStorage.getItem('tojima.staff.context')).toBeNull();
  });
});

type Route = (url: string, init: RequestInit) => Promise<Response>;

async function mountStaff(route: Route, props: { demo?: boolean; initialView?: 'tables' | 'kds' } = {}) {
  sessionStorage.setItem('tojima.staff.context', JSON.stringify(staffContext));
  sessionStorage.setItem('tojima.staff.access-token', staffToken);
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
  vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL, init: RequestInit = {}) => route(String(input), init)));
  host = document.createElement('div'); document.body.append(host); root = createRoot(host);
  await act(async () => { root?.render(<StaffApp {...props} />); await new Promise((resolve) => setTimeout(resolve, 0)); });
}

async function click(container: Element, label: string) {
  const button = [...container.querySelectorAll('button')].find((item) => item.textContent?.includes(label));
  if (!button) throw new Error(`Missing button ${label}`);
  await act(async () => { button.click(); await new Promise((resolve) => setTimeout(resolve, 0)); });
}

async function clickNth(container: Element, selector: string, index: number) {
  const button = container.querySelectorAll<HTMLButtonElement>(selector).item(index);
  if (!button) throw new Error(`Missing button ${selector} at index ${index}`);
  await act(async () => { button.click(); await new Promise((resolve) => setTimeout(resolve, 0)); });
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

async function setField(container: Element, label: string, value: string) {
  const field = [...container.querySelectorAll('label.field')].find((item) => item.textContent?.includes(label));
  const input = field?.querySelector('input');
  if (!input) throw new Error(`Missing field ${label}`);
  const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')?.set;
  await act(async () => { setter?.call(input, value); input.dispatchEvent(new Event('input', { bubbles: true })); input.dispatchEvent(new Event('change', { bubbles: true })); });
}

function staffOrder(overrides: Partial<StaffOrder> = {}): StaffOrder { return { ...defaultOrder(), ...overrides }; }
function defaultOrder(): StaffOrder {
  return { id: 'order-1', session_id: 'session-1', table_id: 'table-1', table_name: 'T1', generation: 1, payment_method: 'CASH' as const, payment_status: 'UNPAID' as const, fulfillment_status: 'WAITING' as const, requires_staff_acceptance: false, staff_accepted: true, version: 3, total_gross_jpy: '1210', created_at: '2026-10-18T00:59:00Z', lines: [{ product_name: 'ラーメン', quantity: 1, unit_gross_jpy: '1100', line_gross_jpy: '1210', options: [{ group_name: 'サイズ', option_name: '大盛り', price_delta_minor: '110' }] }] };
}
function paymentStatus(state: string) {
  return { order_id: 'order-1', state, last_reconciled_at: '2026-10-18T12:00:00Z', payment_review_required: state === 'REVIEW_REQUIRED', resume_available: false };
}
function json(body: unknown, status = 200) { return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } }); }
