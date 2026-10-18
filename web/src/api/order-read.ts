import { bearerHeaders, request } from '../shared/http';

export interface OrderReadGrant {
  order_id: string;
  order_read_token: string;
  expires_at: string;
}
export interface OrderReadStatus {
  version: number;
  payment_status: string;
  fulfillment_status: string;
  display_state: string;
  total_gross_jpy: string;
  currency: 'JPY';
  is_terminal: boolean;
}
export const orderReadClient = {
  status: (grant: OrderReadGrant, signal: AbortSignal) => request<OrderReadStatus>(`/v1/guest/orders/${encodeURIComponent(grant.order_id)}/status`, {
    headers: bearerHeaders(grant.order_read_token), signal, cache: 'no-store', referrerPolicy: 'no-referrer',
  }),
  issue: (sessionID: string, orderID: string, tableJoinToken: string) => request<OrderReadGrant>(`/v1/guest/sessions/${encodeURIComponent(sessionID)}/orders/${encodeURIComponent(orderID)}/read-grant`, {
    method: 'POST', headers: bearerHeaders(tableJoinToken), cache: 'no-store', referrerPolicy: 'no-referrer',
  }),
};

export function orderReadLink(grant: OrderReadGrant): string {
  const url = new URL('/order', window.location.origin);
  url.hash = new URLSearchParams({ order: grant.order_id, grant: grant.order_read_token, expires: grant.expires_at }).toString();
  return url.toString();
}
export function grantFromFragment(fragment: string): OrderReadGrant | null {
  const params = new URLSearchParams(fragment.replace(/^#/, ''));
  const value = { order_id: params.get('order'), order_read_token: params.get('grant'), expires_at: params.get('expires') };
  return isOrderReadGrant(value) ? value : null;
}
export function isOrderReadGrant(value: unknown): value is OrderReadGrant {
  if (!value || typeof value !== 'object') return false;
  const g = value as Partial<OrderReadGrant>;
  return typeof g.order_id === 'string' && /^[a-f0-9]{32}$/.test(g.order_id)
    && typeof g.order_read_token === 'string' && /^[A-Za-z0-9_-]{43}$/.test(g.order_read_token)
    && typeof g.expires_at === 'string' && Number.isFinite(Date.parse(g.expires_at));
}
