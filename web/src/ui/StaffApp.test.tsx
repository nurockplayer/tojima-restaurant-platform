// @vitest-environment jsdom
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { StaffOrder } from '../api/operations';
import { StaffApp } from './StaffApp';

let root: Root | undefined;
let host: HTMLDivElement | undefined;
const base = '/v1/staff/tenants/tenant-1/stores/store-1';
const staffContext = { tenantID: 'tenant-1', storeID: 'store-1' };
const staffToken = 'mock-token-unit-test-only';
const table = { table_id: 'table-1', name: 'T1', session_id: 'session-1', status: 'OPEN', mode: 'DIRECT_SCAN', unpaid_cash_jpy: '1100', order_count: 1 };

afterEach(async () => {
  if (root) await act(async () => root?.unmount());
  root = undefined; host?.remove(); host = undefined;
  sessionStorage.clear(); vi.unstubAllGlobals();
});

describe('staff operations', () => {
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
      if (url === `${base}/operations/access`) return json({ can_cash: true, can_serve: true, can_kitchen: false, can_manage: true });
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
      if (url === `${base}/operations/access`) return json({ can_cash: false, can_serve: false, can_kitchen: false, can_manage: true });
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
      if (url === `${base}/operations/access`) return json({ can_cash: false, can_serve: false, can_kitchen: false, can_manage: true });
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

async function mountStaff(route: Route) {
  sessionStorage.setItem('tojima.staff.context', JSON.stringify(staffContext));
  sessionStorage.setItem('tojima.staff.access-token', staffToken);
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
  vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL, init: RequestInit = {}) => route(String(input), init)));
  host = document.createElement('div'); document.body.append(host); root = createRoot(host);
  await act(async () => { root?.render(<StaffApp />); await new Promise((resolve) => setTimeout(resolve, 0)); });
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
