import { bearerHeaders, request as httpRequest } from '../shared/http';

export interface StaffContext { tenantID: string; storeID: string }
export interface Capabilities { can_cash: boolean; can_serve: boolean; can_kitchen: boolean; can_manage: boolean; can_approve_fulfillment?: boolean }
export interface StaffTable { table_id: string; name: string; session_id?: string; status: 'OPEN' | 'CLOSED' | 'IDLE'; mode: 'STAFF_OPEN' | 'SESSION_PIN' | 'DIRECT_SCAN'; expires_at?: string; unpaid_cash_jpy: string; order_count: number }
export interface StaffOrder {
  id: string; session_id: string; table_id: string; table_name: string; generation: number;
  payment_method: 'CASH' | 'STRIPE'; payment_status: 'UNPAID' | 'PAID' | 'PENDING' | 'UNKNOWN' | 'REFUND_PENDING' | 'REFUNDED';
  fulfillment_status: 'WAITING' | 'ACCEPTED' | 'PREPARING' | 'READY' | 'SERVED' | 'CANCELLED';
  requires_staff_acceptance: boolean; staff_accepted: boolean; cash_mode?: 'PREPAID' | 'PAY_LATER'; version: number;
  total_gross_jpy: string; effective_total_gross_jpy?: string; created_at: string; blocked_reason?: string;
  order_updated?: boolean; refunds?: Array<{ id: string; amount_jpy: string; status: 'REQUESTED' | 'PROCESSING' | 'SUCCEEDED' | 'FAILED'; method: 'CASH' | 'STRIPE'; safe_error_code?: 'REFUND_DISPATCH_PENDING' | 'HOLD_RECONCILIATION_NEEDED' }>;
  payment_review_required?: boolean; review_reason_codes?: Array<'COMPANY_HOLD' | 'OPERATIONAL_HOLD' | 'SESSION_CLOSED' | 'PAYMENT_REVIEW' | 'REFUND_UNRESOLVED' | 'STOP_REQUEST_PENDING' | 'STOP_MANUAL_EXCEPTION'>;
  review_decision?: 'UNRESOLVED' | 'APPROVED' | 'CANCELLED';
  cancellation_requests?: CancellationRequest[];
  lines: Array<{ line_index?: number; original_quantity?: number; product_name: string; quantity: number; unit_gross_jpy: string; line_gross_jpy: string; options: Array<{ group_name: string; option_name: string; price_delta_minor: string }> }>;
}
export interface CancellationRequest { id: string; line_index: number; quantity: number; reason: string; requesting_manager: string; kitchen_confirmer?: string; non_delivery_confirmer?: string; status: 'REQUESTED' | 'KITCHEN_CONFIRMED' | 'FINALIZED' | 'REJECTED' | 'WITHDRAWN'; version: number; manual_exception_reason?: string }
export interface AdjustmentLine { line_index: number; cancelled_quantity: number }
export interface AdjustmentRequest { expected_version: number; reason: string; lines: AdjustmentLine[]; cash_hand_back_confirmed: boolean }
export interface AdjustmentPreview { expected_version: number; original_total_gross_jpy: string; effective_total_gross_jpy: string; refund_amount_jpy: string; whole_order_cancel: boolean; lines: Array<{ line_index: number; original_quantity: number; cancelled_quantity: number; effective_quantity: number; effective_total_gross_jpy: string }> }
export interface Receipt {
  id: string; session_id: string; order_ids: string[]; due_jpy: string; tendered_jpy: string; change_jpy: string;
  cashier_id: string; register_session_ref: string; created_at: string;
  tax: { gross_jpy: string; net_jpy: string; tax_jpy: string; groups: Array<{ rate_numerator: string; rate_denominator: string; gross_jpy: string; net_jpy: string; tax_jpy: string }> };
}
export interface SessionOperations { session: { id: string; status: string; mode: string; expires_at?: string }; orders: StaffOrder[]; receipts: Receipt[]; close_blockers: string[] }
export interface CashSettings { enabled: boolean; mode: 'PREPAID' | 'PAY_LATER'; version: number }
export interface CashPreview { session_id: string; fingerprint: string; order_ids: string[]; due_jpy: string; tax: Receipt['tax'] }
export interface OpenedSession { session: { id: string; status: string; mode: string; expires_at?: string }; pin?: string }
export interface GuestOrderStatus { id: string; payment_status: 'UNPAID' | 'PAID' | 'PENDING' | 'UNKNOWN'; fulfillment_status: StaffOrder['fulfillment_status']; requires_staff_acceptance: boolean; staff_accepted: boolean; cash_mode?: 'PREPAID' | 'PAY_LATER' }
export interface GuestOperations { orders: GuestOrderStatus[]; cash_enabled: boolean }

async function request<T>(path: string, token: string, init: RequestInit = {}): Promise<T> {
  return httpRequest<T>(path, { ...init, headers: { ...bearerHeaders(token), ...init.headers } });
}

const json = (body: unknown, key?: string): RequestInit => ({
  method: 'POST',
  headers: { 'content-type': 'application/json', ...(key ? { 'Idempotency-Key': key } : {}) },
  body: JSON.stringify(body),
});

export const operationsClient = {
  access: (context: StaffContext, token: string) => request<Capabilities>(`${base(context)}/operations/access`, token),
  tables: (context: StaffContext, token: string) => request<{ tables: StaffTable[] }>(`${base(context)}/operations/tables`, token),
  session: (context: StaffContext, token: string, tableID: string, sessionID: string) => request<SessionOperations>(`${base(context)}/tables/${enc(tableID)}/sessions/${enc(sessionID)}/operations`, token),
  kds: (context: StaffContext, token: string, signal?: AbortSignal) => request<{ orders: StaffOrder[] }>(`${base(context)}/operations/kds`, token, { signal }),
  reviews: (context: StaffContext, token: string) => request<{ orders: StaffOrder[] }>(`${base(context)}/operations/reviews`, token),
  approveFulfillment: (context: StaffContext, token: string, order: StaffOrder, body: { expected_version: number; reason: string }, key: string) => request<StaffOrder>(`${base(context)}/tables/${enc(order.table_id)}/sessions/${enc(order.session_id)}/orders/${enc(order.id)}/approve-fulfillment`, token, json(body, key)),
  previewAdjustment: (context: StaffContext, token: string, order: StaffOrder, body: AdjustmentRequest) => request<AdjustmentPreview>(`${orderPath(context, order)}/adjustments/preview`, token, json(body)),
  adjustOrder: (context: StaffContext, token: string, order: StaffOrder, body: AdjustmentRequest, key: string) => request<StaffOrder>(`${orderPath(context, order)}/adjustments`, token, json(body, key)),
  requestStop: (context: StaffContext, token: string, order: StaffOrder, body: { expected_version: number; reason: string; lines: AdjustmentLine[] }, key: string) => request<StaffOrder>(`${orderPath(context, order)}/stop-requests`, token, json(body, key)),
  stopRequestAction: (context: StaffContext, token: string, order: StaffOrder, requestID: string, action: 'withdraw' | 'confirm' | 'reject' | 'finalize', body: { expected_version: number; expected_request_version: number; reason?: string; non_delivery_confirmed?: boolean; cash_hand_back_confirmed?: boolean }, key: string) => request<StaffOrder>(`${orderPath(context, order)}/stop-requests/${enc(requestID)}/${action}`, token, json(body, key)),
  retryRefund: (context: StaffContext, token: string, order: StaffOrder, refundID: string, expectedVersion: number, key: string) => request<StaffOrder>(`${orderPath(context, order)}/refunds/${enc(refundID)}/retry`, token, json({ expected_version: expectedVersion }, key)),
  acknowledgeOrderUpdate: (context: StaffContext, token: string, order: StaffOrder, expectedVersion: number, key: string) => request<StaffOrder>(`${orderPath(context, order)}/acknowledge-update`, token, json({ expected_version: expectedVersion }, key)),
  cashSettings: (context: StaffContext, token: string) => request<CashSettings>(`${base(context)}/operations/cash-settings`, token),
  saveCashSettings: (context: StaffContext, token: string, settings: Omit<CashSettings, 'version'> & { expected_version: number }, key: string) => request<CashSettings>(`${base(context)}/operations/cash-settings`, token, { ...json(settings, key), method: 'PUT' }),
  cashPreview: (context: StaffContext, token: string, tableID: string, sessionID: string) => request<CashPreview>(`${sessionPath(context, tableID, sessionID)}/cash/preview`, token, json({})),
  settleCash: (context: StaffContext, token: string, tableID: string, sessionID: string, body: { fingerprint: string; tendered_jpy: string; register_session_ref: string }, key: string) => request<Receipt>(`${sessionPath(context, tableID, sessionID)}/cash/settle`, token, json(body, key)),
  acceptOrder: (context: StaffContext, token: string, tableID: string, sessionID: string, orderID: string, expectedVersion: number, key: string) => request<StaffOrder>(`${sessionPath(context, tableID, sessionID)}/orders/${enc(orderID)}/accept`, token, json({ expected_version: expectedVersion }, key)),
  advanceOrder: (context: StaffContext, token: string, tableID: string, sessionID: string, orderID: string, target: 'PREPARING' | 'READY' | 'SERVED', expectedVersion: number, key: string) => request<StaffOrder>(`${sessionPath(context, tableID, sessionID)}/orders/${enc(orderID)}/fulfillment`, token, json({ expected_version: expectedVersion, target }, key)),
  closeSession: (context: StaffContext, token: string, tableID: string, sessionID: string, key: string) => request<{ closed: boolean }>(`${sessionPath(context, tableID, sessionID)}/close`, token, json({}, key)),
  openSession: (context: StaffContext, token: string, tableID: string, key: string) => request<OpenedSession>(`${base(context)}/tables/${enc(tableID)}/sessions`, token, json({}, key)),
  guest: (sessionID: string, token: string) => request<GuestOperations>(`/v1/guest/sessions/${enc(sessionID)}/operations`, token),
};

function base(context: StaffContext) { return `/v1/staff/tenants/${enc(context.tenantID)}/stores/${enc(context.storeID)}`; }
function sessionPath(context: StaffContext, tableID: string, sessionID: string) { return `${base(context)}/tables/${enc(tableID)}/sessions/${enc(sessionID)}`; }
function orderPath(context: StaffContext, order: StaffOrder) { return `${sessionPath(context, order.table_id, order.session_id)}/orders/${enc(order.id)}`; }
function enc(value: string) { return encodeURIComponent(value); }
